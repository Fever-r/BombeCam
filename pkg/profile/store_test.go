package profile

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestFileStore_RoundTrip_RawKey(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	profilePath := filepath.Join(tmpDir, "profile.enc")

	key := []byte("01234567890123456789012345678901") // 32 bytes
	store := NewFileStore(profilePath, key)

	// HasProfile should be false before saving
	has, err := store.HasProfile(ctx)
	if err != nil {
		t.Fatalf("HasProfile failed: %v", err)
	}
	if has {
		t.Fatalf("expected HasProfile to be false initially")
	}

	// Create test profile with sensitive credentials
	orig := &Profile{
		Version:   1,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		UpdatedAt: time.Now().UTC().Truncate(time.Second),
		Privacy:   PrivacySettings{BlockCloudVideo: BoolPtr(true), AppliedCameras: []string{"aa:bb:cc:dd:ee:01"}},
		Credentials: CloudCredentials{
			AccountEmail:   "alice@example.com",
			Password:       SecretString("SecretP@ssword123"),
			AuthToken:      SecretString("VendorAuthToken-987"),
			RefreshToken:   SecretString("RefreshToken-123"),
			TokenExpiresAt: time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second),
			VendorUID:      "uid-alice-456",
			Region:         "us-east",
			Country:        "+1",
			PhoneCode:      "client-phone-code-123",
		},
		Cameras: map[string]CameraProfile{
			"cam-uuid-1": {
				UUID:       "cam-uuid-1",
				Name:       "Living Room",
				Model:      "MiniCam-1080p",
				IPAddress:  "192.168.1.100",
				MACAddress: "AA:BB:CC:DD:EE:FF",
				EnrolledAt: time.Now().UTC().Truncate(time.Second),
				Online:     true,
				TokenCache: SecretString("cam-token-001"),
				StreamConfig: CameraStreamConfig{
					RTSPEnabled: true,
					HLSEnabled:  true,
					RTSPPath:    "/live/cam1",
					CustomFPS:   25,
				},
			},
		},
		Connection: ConnectionParameters{
			RTSPBaseURL:      "rtsp://127.0.0.1:8554",
			HLSBaseURL:       "http://127.0.0.1:8888",
			NetAPIURL:        "http://127.0.0.1:8653",
			ControlTransport: "mqtt",
			MosquittoURL:     "tcp://127.0.0.1:1883",
			StrictManual:     false,
			AllowedSubnets:   []string{"192.168.1.0/24"},
			Timezone:         "America/New_York",
			TimezoneOffset:   -5.0,
		},
	}

	// Save profile
	if err := store.Save(ctx, orig); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// HasProfile should now be true
	has, err = store.HasProfile(ctx)
	if err != nil {
		t.Fatalf("HasProfile failed: %v", err)
	}
	if !has {
		t.Fatalf("expected HasProfile to be true after saving")
	}

	// Verify file permissions (0600) on non-Windows platforms
	fi, err := os.Stat(profilePath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if runtime.GOOS != "windows" {
		if fi.Mode().Perm() != 0600 {
			t.Fatalf("expected profile permissions 0600, got %04o", fi.Mode().Perm())
		}
	}

	// Verify no stray .tmp files left in directory
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("stray temporary file found in directory: %s", e.Name())
		}
	}

	// Load profile back
	loaded, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Assert equality of unencrypted and exposed fields
	if loaded.Version != orig.Version {
		t.Fatalf("Version mismatch: got %d, want %d", loaded.Version, orig.Version)
	}
	if loaded.Privacy.BlockCloudVideo == nil || *loaded.Privacy.BlockCloudVideo != *orig.Privacy.BlockCloudVideo ||
		len(loaded.Privacy.AppliedCameras) != 1 {
		t.Fatalf("Privacy mismatch: got %+v, want %+v", loaded.Privacy, orig.Privacy)
	}
	// The single login of the older format comes back as the pool's only login.
	if len(loaded.Accounts) != 1 || loaded.Credentials.AccountEmail != "" {
		t.Fatalf("logins after load: %+v (legacy field %q)", loaded.Accounts, loaded.Credentials.AccountEmail)
	}
	got := loaded.Accounts[0]
	if got.AccountEmail != orig.Credentials.AccountEmail {
		t.Fatalf("AccountEmail mismatch: got %s, want %s", got.AccountEmail, orig.Credentials.AccountEmail)
	}
	if got.Password.Expose() != orig.Credentials.Password.Expose() {
		t.Fatalf("Password mismatch: got %s, want %s", got.Password.Expose(), orig.Credentials.Password.Expose())
	}
	if got.AuthToken.Expose() != orig.Credentials.AuthToken.Expose() {
		t.Fatalf("AuthToken mismatch: got %s, want %s", got.AuthToken.Expose(), orig.Credentials.AuthToken.Expose())
	}
	if got.VendorUID != orig.Credentials.VendorUID {
		t.Fatalf("VendorUID mismatch: got %s, want %s", got.VendorUID, orig.Credentials.VendorUID)
	}
	if cam := loaded.Cameras["cam-uuid-1"]; cam.AccountEmail != "alice@example.com" {
		t.Fatalf("camera owner after migration: %q", cam.AccountEmail)
	}
	if len(loaded.Cameras) != len(orig.Cameras) {
		t.Fatalf("Cameras count mismatch: got %d, want %d", len(loaded.Cameras), len(orig.Cameras))
	}
	cam := loaded.Cameras["cam-uuid-1"]
	if cam.Name != "Living Room" || cam.IPAddress != "192.168.1.100" {
		t.Fatalf("Camera fields mismatch: %+v", cam)
	}
	if cam.TokenCache.Expose() != "cam-token-001" {
		t.Fatalf("Camera TokenCache mismatch: got %s, want cam-token-001", cam.TokenCache.Expose())
	}
	if loaded.Connection.Timezone != "America/New_York" || loaded.Connection.TimezoneOffset != -5.0 {
		t.Fatalf("Connection parameters mismatch: %+v", loaded.Connection)
	}

	// Delete profile
	if err := store.Delete(ctx); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	has, err = store.HasProfile(ctx)
	if err != nil {
		t.Fatalf("HasProfile after delete failed: %v", err)
	}
	if has {
		t.Fatalf("expected HasProfile to be false after delete")
	}
}

func TestFileStore_RoundTrip_PassphraseArgon2id(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	profilePath := filepath.Join(tmpDir, "profile.enc")

	passphrase := []byte("MyComplexUserPassphrase!99")
	store := NewFileStore(profilePath, passphrase)

	orig := &Profile{
		Version: 1,
		Privacy: PrivacySettings{BlockCloudVideo: BoolPtr(false)},
		Credentials: CloudCredentials{
			AccountEmail: "bob@example.com",
			Password:     SecretString("BobSecretPass123"),
			AuthToken:    SecretString("BobAuthToken"),
		},
	}

	if err := store.Save(ctx, orig); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded.Accounts) != 1 || loaded.Accounts[0].Password.Expose() != "BobSecretPass123" {
		t.Fatalf("password mismatch: got %+v", loaded.Accounts)
	}
	if loaded.Accounts[0].AuthToken.Expose() != "BobAuthToken" {
		t.Fatalf("auth token mismatch: got %s", loaded.Accounts[0].AuthToken.Expose())
	}
	if b := loaded.Privacy.BlockCloudVideo; b == nil || *b {
		t.Fatalf("expected BlockCloudVideo to be false")
	}

	// Loading with wrong passphrase must fail
	wrongStore := NewFileStore(profilePath, []byte("WrongPassphrase"))
	_, err = wrongStore.Load(ctx)
	if err == nil {
		t.Fatalf("expected decryption failure with wrong passphrase")
	}
}

func TestEnsureKeyFile_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "profile.key")

	// 1. Initial generation of keyfile
	key1, err := EnsureKeyFile(keyPath)
	if err != nil {
		t.Fatalf("EnsureKeyFile failed: %v", err)
	}
	if len(key1) != 32 {
		t.Fatalf("expected 32-byte key, got %d", len(key1))
	}

	// Verify permissions
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if runtime.GOOS != "windows" {
		if fi.Mode().Perm() != 0600 {
			t.Fatalf("expected keyfile permissions 0600, got %04o", fi.Mode().Perm())
		}
	}

	// 2. Second invocation reads the same key
	key2, err := EnsureKeyFile(keyPath)
	if err != nil {
		t.Fatalf("EnsureKeyFile second call failed: %v", err)
	}
	if string(key1) != string(key2) {
		t.Fatalf("EnsureKeyFile mismatch between runs: got %x, want %x", key2, key1)
	}
}

func TestResolveKey_Precedence(t *testing.T) {
	tmpDir := t.TempDir()
	keyFile := filepath.Join(tmpDir, "test.key")

	// 1. Direct key takes highest precedence
	direct := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	k, err := ResolveKey(direct, keyFile)
	if err != nil {
		t.Fatalf("ResolveKey failed: %v", err)
	}
	if len(k) != 32 {
		t.Fatalf("expected 32 bytes from 64-char hex string, got %d", len(k))
	}

	// 2. Env variable precedence
	t.Setenv("BOMBECAM_PROFILE_KEY", "env-secret-passphrase-12345")
	k, err = ResolveKey("", keyFile)
	if err != nil {
		t.Fatalf("ResolveKey env failed: %v", err)
	}
	if string(k) != "env-secret-passphrase-12345" {
		t.Fatalf("expected env key, got %s", string(k))
	}

	// 3. Fallback to keyfile
	t.Setenv("BOMBECAM_PROFILE_KEY", "")
	k, err = ResolveKey("", keyFile)
	if err != nil {
		t.Fatalf("ResolveKey keyfile failed: %v", err)
	}
	if len(k) != 32 {
		t.Fatalf("expected 32-byte key from auto-generated keyfile, got %d", len(k))
	}
}
