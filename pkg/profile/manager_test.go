package profile

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProfileManager_Lifecycle(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	profilePath := filepath.Join(tmpDir, "profile.enc")

	key := []byte("12345678901234567890123456789012")
	mgr := NewManager(profilePath, key)

	// 1. Initial state
	has, err := mgr.HasProfile(ctx)
	if err != nil {
		t.Fatalf("HasProfile failed: %v", err)
	}
	if has {
		t.Fatalf("expected HasProfile to be false")
	}
	if p := mgr.GetProfile(); p != nil {
		t.Fatalf("expected GetProfile() to be nil initially")
	}

	// 2. Save profile
	now := time.Now().UTC()
	initialProfile := &Profile{
		Version:   1,
		CreatedAt: now,
		Privacy:   PrivacySettings{BlockCloudVideo: BoolPtr(true)},
		Credentials: CloudCredentials{
			AccountEmail: "carol@example.com",
			Password:     SecretString("SecretPassCarol!"),
			AuthToken:    SecretString("CarolAuthToken"),
			VendorUID:    "uid-carol-123",
		},
	}

	if err := mgr.Save(ctx, initialProfile); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	has, err = mgr.HasProfile(ctx)
	if err != nil || !has {
		t.Fatalf("expected HasProfile to be true after save")
	}

	// 3. GetPublicView / RedactSecrets
	view := mgr.GetPublicView()
	if view == nil {
		t.Fatalf("expected non-nil PublicProfileView")
	}
	if len(view.Accounts) != 1 || view.Accounts[0].AccountEmail != "carol@example.com" {
		t.Fatalf("view.Accounts mismatch: %+v", view.Accounts)
	}
	if !view.Accounts[0].HasPassword || !view.Accounts[0].HasToken {
		t.Fatalf("expected HasPassword and HasToken to be true in view")
	}

	// 4. EnrollCamera
	cam := CameraProfile{
		UUID:       "cam-carol-1",
		Name:       "Porch Camera",
		Model:      "Outdoor-Cam",
		IPAddress:  "192.168.1.120",
		Online:     true,
		TokenCache: SecretString("cam-token-secret-123"),
	}
	if err := mgr.EnrollCamera(ctx, cam); err != nil {
		t.Fatalf("EnrollCamera failed: %v", err)
	}

	p := mgr.GetProfile()
	if p == nil || len(p.Cameras) != 1 {
		t.Fatalf("expected 1 enrolled camera in cached profile")
	}
	if p.Cameras["cam-carol-1"].Name != "Porch Camera" {
		t.Fatalf("camera name mismatch: %s", p.Cameras["cam-carol-1"].Name)
	}

	// 5. Record the Block cloud video setting as applied
	if _, err := mgr.Update(ctx, func(p *Profile) error {
		p.Privacy = PrivacySettings{BlockCloudVideo: BoolPtr(false), RouterAddress: "192.168.8.1", RouterHostKey: "SHA256:abc"}
		return nil
	}); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if b := mgr.GetProfile().Privacy.BlockCloudVideo; b == nil || *b {
		t.Fatalf("expected BlockCloudVideo to be false")
	}

	// 6. Reload from fresh manager instance (simulating restart)
	restartMgr := NewManager(profilePath, key)
	loaded, err := restartMgr.Load(ctx)
	if err != nil {
		t.Fatalf("Load on restartMgr failed: %v", err)
	}
	if b := loaded.Privacy.BlockCloudVideo; b == nil || *b || loaded.Privacy.RouterHostKey != "SHA256:abc" {
		t.Fatalf("expected the privacy setting to survive a reload, got %+v", loaded.Privacy)
	}
	if len(loaded.Cameras) != 1 || loaded.Cameras["cam-carol-1"].Name != "Porch Camera" {
		t.Fatalf("camera not preserved across reload: %+v", loaded.Cameras)
	}
	if len(loaded.Accounts) != 1 || loaded.Accounts[0].Password.Expose() != "SecretPassCarol!" {
		t.Fatalf("password not preserved across reload")
	}

	// 7. UnenrollCamera
	if err := restartMgr.UnenrollCamera(ctx, "cam-carol-1"); err != nil {
		t.Fatalf("UnenrollCamera failed: %v", err)
	}
	if len(restartMgr.GetProfile().Cameras) != 0 {
		t.Fatalf("expected zero cameras after unenroll")
	}

	// 8. Delete
	if err := restartMgr.Delete(ctx); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	has, err = restartMgr.HasProfile(ctx)
	if err != nil || has {
		t.Fatalf("expected HasProfile to be false after delete")
	}
}

func TestProtectKey_DPAPI_Roundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "profile.key")
	origKey := []byte("0123456789abcdef0123456789abcdef")

	_, err := ProtectKey(origKey, keyPath)
	if err != nil {
		t.Fatalf("ProtectKey failed: %v", err)
	}

	loadedKey, err := UnprotectKey(keyPath)
	if err != nil {
		t.Fatalf("UnprotectKey failed: %v", err)
	}

	if string(loadedKey) != string(origKey) {
		t.Fatalf("key mismatch after DPAPI roundtrip: expected %q, got %q", string(origKey), string(loadedKey))
	}
}

func TestResolveKey_DPAPI_Integration_And_Migration(t *testing.T) {
	if !DPAPIAvailable() {
		t.Skip("DPAPI not available on this platform")
	}

	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "profile.key")
	dpapiPath := keyPath + ".dpapi"

	// 1. Fresh resolution: should create .dpapi file and NO plaintext key file
	resolvedKey1, err := ResolveKey("", keyPath)
	if err != nil {
		t.Fatalf("fresh ResolveKey failed: %v", err)
	}
	if len(resolvedKey1) != 32 {
		t.Fatalf("expected 32-byte key, got %d bytes", len(resolvedKey1))
	}
	if _, err := os.Stat(dpapiPath); err != nil {
		t.Fatalf("expected DPAPI key file to exist at %s: %v", dpapiPath, err)
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("expected plaintext key file NOT to exist at %s", keyPath)
	}

	// 2. Restart/reload: should unprotect and return exact same key
	resolvedKey2, err := ResolveKey("", keyPath)
	if err != nil {
		t.Fatalf("reloaded ResolveKey failed: %v", err)
	}
	if string(resolvedKey1) != string(resolvedKey2) {
		t.Fatalf("key mismatch across reload: %x vs %x", resolvedKey1, resolvedKey2)
	}

	// 3. Migration test: legacy plaintext hex file should be migrated to .dpapi and removed
	migDir := t.TempDir()
	migKeyPath := filepath.Join(migDir, "profile.key")
	migDpapiPath := migKeyPath + ".dpapi"
	legacyHexKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n"
	if err := os.WriteFile(migKeyPath, []byte(legacyHexKey), 0600); err != nil {
		t.Fatalf("failed to write legacy key: %v", err)
	}

	migratedKey, err := ResolveKey("", migKeyPath)
	if err != nil {
		t.Fatalf("migration ResolveKey failed: %v", err)
	}
	if hex.EncodeToString(migratedKey) != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("migrated key mismatch: %x", migratedKey)
	}
	if _, err := os.Stat(migDpapiPath); err != nil {
		t.Fatalf("expected migrated .dpapi file to exist: %v", err)
	}
	if _, err := os.Stat(migKeyPath); !os.IsNotExist(err) {
		t.Fatalf("expected legacy plaintext key file to be removed after migration")
	}
}
