package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Fever-r/BombeCam/pkg/netstack"
	"github.com/Fever-r/BombeCam/pkg/profile"
)

// ----------------------------------------------------------------------------
// Test Helpers & Assertions
// ----------------------------------------------------------------------------

func setupRandomKey32(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("failed to generate random key: %v", err)
	}
	return k
}

func assertZeroSecrets(t *testing.T, payload string, secrets []string, locationDesc string) {
	t.Helper()
	for _, s := range secrets {
		if s == "" {
			continue
		}
		if strings.Contains(payload, s) {
			t.Fatalf("SECURITY VIOLATION: Secret %q leaked in %s:\n%s", s, locationDesc, payload)
		}
	}
}

// ----------------------------------------------------------------------------
// Group 1: Feature Coverage (AC1 & AC2)
// ----------------------------------------------------------------------------

func TestE2E_Group1_DynamicSubnetDetectionAndBinding(t *testing.T) {
	_, subA, _ := net.ParseCIDR("10.240.0.0/16")
	_, subB, _ := net.ParseCIDR("172.25.10.0/24")
	_, subC, _ := net.ParseCIDR("192.168.10.4/30") // /30 edge subnet
	_, subDown, _ := net.ParseCIDR("192.168.99.0/24")
	_, subLoop, _ := net.ParseCIDR("127.0.0.0/8")

	mock := netstack.NewMockDiscovery([]netstack.NetworkInterface{
		{Name: "eth0", Subnets: []*net.IPNet{subA}, IsUp: true, IsLoopback: false},
		{Name: "eth1", Subnets: []*net.IPNet{subB}, IsUp: true, IsLoopback: false},
		{Name: "cam0", Subnets: []*net.IPNet{subC}, IsUp: true, IsLoopback: false},
		{Name: "wlan0", Subnets: []*net.IPNet{subDown}, IsUp: false, IsLoopback: false}, // Down interface
		{Name: "lo", Subnets: []*net.IPNet{subLoop}, IsUp: true, IsLoopback: true},      // Loopback interface
	})

	// 1. Assert host subnets derive dynamically without loopback or down interfaces
	subnets, err := mock.DetectHostSubnets()
	if err != nil {
		t.Fatalf("DetectHostSubnets failed: %v", err)
	}
	if len(subnets) != 3 {
		t.Fatalf("expected exactly 3 active subnets, got %d", len(subnets))
	}

	// 2. Assert dynamic interface binding for arbitrary camera IPs
	iface, sn, err := mock.FindInterfaceForIP(net.ParseIP("10.240.55.100"))
	if err != nil || iface.Name != "eth0" || sn.String() != "10.240.0.0/16" {
		t.Fatalf("failed to bind 10.240.55.100: iface=%v, sn=%v, err=%v", iface, sn, err)
	}

	iface, sn, err = mock.FindInterfaceForIP(net.ParseIP("192.168.10.5"))
	if err != nil || iface.Name != "cam0" || sn.String() != "192.168.10.4/30" {
		t.Fatalf("failed to bind /30 camera IP: iface=%v, sn=%v, err=%v", iface, sn, err)
	}

	// 3. Assert out-of-subnet IP is not found
	if _, _, err := mock.FindInterfaceForIP(net.ParseIP("192.168.10.8")); err == nil {
		t.Fatalf("expected 192.168.10.8 to be rejected (outside /30 subnet)")
	}

	// 4. Assert down interface IP is rejected
	if _, _, err := mock.FindInterfaceForIP(net.ParseIP("192.168.99.10")); err == nil {
		t.Fatalf("expected down interface IP to be rejected")
	}
}

func TestE2E_Group1_DynamicICECandidateFiltering_NonStandardLAN(t *testing.T) {
	_, subEnterprise, _ := net.ParseCIDR("10.240.0.0/16")
	_, subCustom, _ := net.ParseCIDR("172.31.50.0/24")

	mock := netstack.NewMockDiscovery([]netstack.NetworkInterface{
		{Name: "eth0", Subnets: []*net.IPNet{subEnterprise, subCustom}, IsUp: true, IsLoopback: false},
	})

	filter, err := netstack.NewICECandidateFilter([]string{"10.240.0.0/16", "172.31.50.0/24"}, mock)
	if err != nil {
		t.Fatalf("failed to create ICECandidateFilter: %v", err)
	}

	// Valid host UDP candidates in dynamically detected subnets
	validCandidates := []struct {
		sdp      string
		expected string
	}{
		{"candidate:842163049 1 udp 1677729535 10.240.1.55 54321 typ host", "10.240.1.55"},
		{"a=candidate:842163049 1 udp 1677729535 172.31.50.99 5000 typ host", "172.31.50.99"},
		{"10.240.99.10", "10.240.99.10"},
	}

	for _, tc := range validCandidates {
		ip, ok := filter.ValidateCandidate(tc.sdp)
		if !ok || ip == nil || ip.String() != tc.expected {
			t.Fatalf("expected candidate %q to be allowed as %s, got ip=%v, ok=%v", tc.sdp, tc.expected, ip, ok)
		}
	}

	// Invalid candidates: WAN IPs, non-host types, TCP transport, out-of-subnet
	invalidCandidates := []struct {
		name string
		sdp  string
	}{
		{"Public WAN IP", "candidate:1 1 udp 1000 8.8.8.8 5000 typ host"},
		{"Server Reflexive (srflx)", "candidate:1 1 udp 1000 10.240.1.55 5000 typ srflx"},
		{"Relay candidate", "candidate:1 1 udp 1000 10.240.1.55 5000 typ relay"},
		{"Peer Reflexive (prflx)", "candidate:1 1 udp 1000 10.240.1.55 5000 typ prflx"},
		{"TCP candidate", "candidate:1 1 tcp 1000 10.240.1.55 5000 typ host"},
		{"Out of subnet RFC1918", "candidate:1 1 udp 1000 192.168.1.50 5000 typ host"},
		{"Loopback candidate", "candidate:1 1 udp 1000 127.0.0.1 5000 typ host"},
	}

	for _, tc := range invalidCandidates {
		ip, ok := filter.ValidateCandidate(tc.sdp)
		if ok || ip != nil {
			t.Fatalf("expected candidate %s (%s) to be rejected, got ip=%v, ok=%v", tc.name, tc.sdp, ip, ok)
		}
	}
}

func TestE2E_Group1_ProfileCreation_AES256GCM_AtRestEncryption(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	profilePath := filepath.Join(tmpDir, "test_profile.enc")
	masterKey := setupRandomKey32(t)

	canaryPass := "SuperSecretPassword#123!"
	canaryAuth := "CanaryAuthToken-XYZ-987"
	canaryRefresh := "CanaryRefreshToken-ABC-654"
	canaryCamToken := "CameraTokenCacheSecret#1"

	canaries := []string{canaryPass, canaryAuth, canaryRefresh, canaryCamToken, string(masterKey), hex.EncodeToString(masterKey)}

	mgr := profile.NewManager(profilePath, masterKey)
	now := time.Now().UTC()

	prof := &profile.Profile{
		Version:   profile.CurrentSchemaVersion,
		CreatedAt: now,
		Privacy:   profile.PrivacySettings{BlockCloudVideo: profile.BoolPtr(true)},
		Credentials: profile.CloudCredentials{
			AccountEmail: "user@example.com",
			Password:     profile.SecretString(canaryPass),
			AuthToken:    profile.SecretString(canaryAuth),
			RefreshToken: profile.SecretString(canaryRefresh),
			VendorUID:    "vendor-uid-100",
		},
		Cameras: map[string]profile.CameraProfile{
			"cam-001": {
				UUID:       "cam-001",
				Name:       "Driveway",
				Model:      "WS03",
				IPAddress:  "10.240.1.150",
				TokenCache: profile.SecretString(canaryCamToken),
				Online:     true,
			},
		},
		Connection: profile.ConnectionParameters{
			AllowedSubnets: []string{"10.240.0.0/16"},
			Timezone:       "UTC",
		},
	}

	if err := mgr.Save(ctx, prof); err != nil {
		t.Fatalf("Save profile failed: %v", err)
	}

	// 1. Assert file exists and permissions are 0600 on POSIX
	fi, err := os.Stat(profilePath)
	if err != nil {
		t.Fatalf("profile file not found on disk: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0600 {
		t.Fatalf("expected file permission 0600, got %04o", fi.Mode().Perm())
	}

	// 2. Assert raw disk bytes are encrypted and contain zero secrets
	rawBytes, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("failed to read raw profile from disk: %v", err)
	}
	rawStr := string(rawBytes)

	assertZeroSecrets(t, rawStr, canaries, "at-rest disk profile")

	// 3. Assert envelope structure on disk
	if !strings.Contains(rawStr, profile.EnvelopeMagicHeader) {
		t.Fatalf("missing magic header %s in disk profile", profile.EnvelopeMagicHeader)
	}
	var env profile.EncryptedEnvelope
	if err := json.Unmarshal(rawBytes, &env); err != nil {
		t.Fatalf("failed to unmarshal disk envelope JSON: %v", err)
	}
	if env.Magic != profile.EnvelopeMagicHeader || env.Version != profile.CurrentSchemaVersion {
		t.Fatalf("invalid envelope header or version: %+v", env)
	}
}

func TestE2E_Group1_ColdDaemonRestartRecovery_ActiveCameras(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	profilePath := filepath.Join(tmpDir, "restart_profile.enc")
	masterKey := setupRandomKey32(t)

	// Phase 1: Initialize, configure and save profile
	mgr1 := profile.NewManager(profilePath, masterKey)
	now := time.Now().UTC()

	initProf := &profile.Profile{
		Version:   profile.CurrentSchemaVersion,
		CreatedAt: now,
		UpdatedAt: now,
		Privacy:   profile.PrivacySettings{BlockCloudVideo: profile.BoolPtr(true)},
		Credentials: profile.CloudCredentials{
			AccountEmail:   "restart-user@example.com",
			Password:       profile.SecretString("SecretPassword123!"),
			AuthToken:      profile.SecretString("CachedAuthToken999"),
			TokenExpiresAt: now.Add(24 * time.Hour),
			VendorUID:      "uid-restart-42",
		},
		Cameras: map[string]profile.CameraProfile{
			"cam-porch-01": {
				UUID:       "cam-porch-01",
				Name:       "Front Porch",
				Model:      "WS03",
				IPAddress:  "192.168.10.150",
				TokenCache: profile.SecretString("PorchTokenSecret#1"),
				EnrolledAt: now,
				Online:     true,
			},
			"cam-yard-02": {
				UUID:       "cam-yard-02",
				Name:       "Back Yard",
				Model:      "WS04",
				IPAddress:  "192.168.10.151",
				TokenCache: profile.SecretString("YardTokenSecret#2"),
				EnrolledAt: now,
				Online:     true,
			},
		},
		Connection: profile.ConnectionParameters{
			AllowedSubnets: []string{"192.168.10.0/24"},
			Timezone:       "America/New_York",
			TimezoneOffset: -5.0,
		},
	}

	if err := mgr1.Save(ctx, initProf); err != nil {
		t.Fatalf("Save failed in Phase 1: %v", err)
	}

	// Simulate Cold Process Shutdown: drop all in-memory references
	mgr1 = nil
	runtime.GC()

	// Phase 2: Start new daemon instance and load from disk
	mgr2 := profile.NewManager(profilePath, masterKey)

	has, err := mgr2.HasProfile(ctx)
	if err != nil || !has {
		t.Fatalf("HasProfile failed after cold restart: has=%v, err=%v", has, err)
	}

	restored, err := mgr2.Load(ctx)
	if err != nil {
		t.Fatalf("Load failed after cold restart: %v", err)
	}

	// Assert complete restoration of cameras and configuration
	if len(restored.Accounts) != 1 || restored.Accounts[0].AccountEmail != "restart-user@example.com" {
		t.Fatalf("restored login mismatch: %+v", restored.Accounts)
	}
	if restored.Accounts[0].Password.Expose() != "SecretPassword123!" {
		t.Fatalf("restored password mismatch")
	}
	if restored.Accounts[0].AuthToken.Expose() != "CachedAuthToken999" {
		t.Fatalf("restored auth token mismatch")
	}
	if b := restored.Privacy.BlockCloudVideo; b == nil || !*b {
		t.Fatalf("expected the Block cloud video setting to be restored as Yes")
	}
	if len(restored.Cameras) != 2 {
		t.Fatalf("expected 2 restored cameras, got %d", len(restored.Cameras))
	}
	c1, ok1 := restored.Cameras["cam-porch-01"]
	c2, ok2 := restored.Cameras["cam-yard-02"]
	if !ok1 || !ok2 {
		t.Fatalf("missing camera profiles after restart")
	}
	if c1.IPAddress != "192.168.10.150" || c2.IPAddress != "192.168.10.151" {
		t.Fatalf("restored camera LAN IPs mismatch: %s, %s", c1.IPAddress, c2.IPAddress)
	}
	if c1.TokenCache.Expose() != "PorchTokenSecret#1" || c2.TokenCache.Expose() != "YardTokenSecret#2" {
		t.Fatalf("restored camera token caches mismatch")
	}
	if len(restored.Connection.AllowedSubnets) != 1 || restored.Connection.AllowedSubnets[0] != "192.168.10.0/24" {
		t.Fatalf("restored subnets mismatch: %+v", restored.Connection.AllowedSubnets)
	}
	if restored.Connection.Timezone != "America/New_York" || restored.Connection.TimezoneOffset != -5.0 {
		t.Fatalf("restored timezone mismatch: %s (%f)", restored.Connection.Timezone, restored.Connection.TimezoneOffset)
	}
}

func TestE2E_Group1_ZeroSecretLeakage_AllFormattersAndJSON(t *testing.T) {
	secretPass := "CanaryPass999!DoNotLeak"
	secretToken := "CanaryToken888#DoNotLeak"
	secretKeyBytes := setupRandomKey32(t)

	secStr := profile.SecretString(secretPass)
	secBytes := profile.SecretBytes(secretKeyBytes)

	creds := profile.CloudCredentials{
		AccountEmail: "audit@example.com",
		Password:     secStr,
		AuthToken:    profile.SecretString(secretToken),
		VendorUID:    "uid-audit",
	}

	prof := &profile.Profile{
		Version:     profile.CurrentSchemaVersion,
		Privacy:     profile.PrivacySettings{BlockCloudVideo: profile.BoolPtr(true)},
		Credentials: creds,
		Cameras: map[string]profile.CameraProfile{
			"cam-1": {
				UUID:       "cam-1",
				Name:       "Test Cam",
				TokenCache: profile.SecretString("CamTokenSecret777"),
			},
		},
	}

	canaries := []string{secretPass, secretToken, "CamTokenSecret777", string(secretKeyBytes), hex.EncodeToString(secretKeyBytes)}

	// 1. Formatting verbs: %s, %v, %+v, %#v, %q
	for _, verb := range []string{"%s", "%v", "%+v", "%#v", "%q"} {
		sOut := fmt.Sprintf(verb, secStr)
		assertZeroSecrets(t, sOut, canaries, fmt.Sprintf("SecretString %s", verb))
		if !strings.Contains(sOut, profile.RedactedPlaceholder) {
			t.Fatalf("expected [REDACTED] in SecretString %s output, got %s", verb, sOut)
		}

		bOut := fmt.Sprintf(verb, secBytes)
		assertZeroSecrets(t, bOut, canaries, fmt.Sprintf("SecretBytes %s", verb))

		pOut := fmt.Sprintf(verb, prof)
		assertZeroSecrets(t, pOut, canaries, fmt.Sprintf("Profile %s", verb))
	}

	// 2. Structured logging (slog Text and JSON handlers)
	bufText := &bytes.Buffer{}
	loggerText := slog.New(slog.NewTextHandler(bufText, nil))
	loggerText.Info("test log", "profile", prof, "pass", secStr, "key", secBytes)
	assertZeroSecrets(t, bufText.String(), canaries, "slog TextHandler")

	bufJSON := &bytes.Buffer{}
	loggerJSON := slog.New(slog.NewJSONHandler(bufJSON, nil))
	loggerJSON.Info("test log json", "profile", prof, "pass", secStr, "key", secBytes)
	assertZeroSecrets(t, bufJSON.String(), canaries, "slog JSONHandler")

	// 3. JSON marshaling
	jsonBytes, err := json.Marshal(prof)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	assertZeroSecrets(t, string(jsonBytes), canaries, "json.Marshal(Profile)")

	var pubView profile.PublicProfileView
	if err := json.Unmarshal(jsonBytes, &pubView); err != nil {
		t.Fatalf("failed to unmarshal into PublicProfileView: %v", err)
	}
	if len(pubView.Accounts) != 1 || !pubView.Accounts[0].HasPassword || !pubView.Accounts[0].HasToken {
		t.Fatalf("expected HasPassword=true and HasToken=true in sanitized view")
	}
}

// ----------------------------------------------------------------------------
// Group 2: Boundary & Corner Cases (AC1 & AC2)
// ----------------------------------------------------------------------------

func TestE2E_Group2_TamperedCiphertext_AEADAuthenticationFailure(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	profilePath := filepath.Join(tmpDir, "tampered.enc")
	masterKey := setupRandomKey32(t)

	store := profile.NewFileStore(profilePath, masterKey)
	p := &profile.Profile{
		Version:     profile.CurrentSchemaVersion,
		Credentials: profile.CloudCredentials{AccountEmail: "tamper@example.com", Password: profile.SecretString("Secret123")},
	}
	if err := store.Save(ctx, p); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	rawDisk, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	var env profile.EncryptedEnvelope
	if err := json.Unmarshal(rawDisk, &env); err != nil {
		t.Fatalf("Unmarshal envelope failed: %v", err)
	}

	// 1. Bit-flip in ciphertext authentication tag
	rawCt, _ := base64.StdEncoding.DecodeString(env.Ciphertext)
	rawCt[len(rawCt)-1] ^= 0x55 // Flip byte in 16-byte tag
	tamperedEnv := env
	tamperedEnv.Ciphertext = base64.StdEncoding.EncodeToString(rawCt)

	tamperedBytes, _ := json.Marshal(tamperedEnv)
	_ = os.WriteFile(profilePath, tamperedBytes, 0600)

	_, err = store.Load(ctx)
	if err == nil || !errors.Is(err, profile.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed on tampered ciphertext, got: %v", err)
	}

	// 2. AAD Tampering: alter envelope Magic header
	tamperedEnv2 := env
	tamperedEnv2.Magic = "BOMBE_ENC_V2"
	tamperedBytes2, _ := json.Marshal(tamperedEnv2)
	_ = os.WriteFile(profilePath, tamperedBytes2, 0600)

	_, err = store.Load(ctx)
	if err == nil || !errors.Is(err, profile.ErrCorruptedProfile) {
		t.Fatalf("expected ErrCorruptedProfile on tampered magic header, got: %v", err)
	}

	// 3. AAD Tampering: alter envelope Version
	tamperedEnv3 := env
	tamperedEnv3.Version = 99
	tamperedBytes3, _ := json.Marshal(tamperedEnv3)
	_ = os.WriteFile(profilePath, tamperedBytes3, 0600)

	_, err = store.Load(ctx)
	if err == nil || !errors.Is(err, profile.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed on altered version, got: %v", err)
	}
}

func TestE2E_Group2_InvalidAndMismatchedKeyRejection(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	profilePath := filepath.Join(tmpDir, "keys.enc")
	realKey := setupRandomKey32(t)
	wrongKey := setupRandomKey32(t)

	store := profile.NewFileStore(profilePath, realKey)
	_ = store.Save(ctx, &profile.Profile{
		Version:     profile.CurrentSchemaVersion,
		Credentials: profile.CloudCredentials{AccountEmail: "keys@example.com"},
	})

	// 1. Mismatched 32-byte key
	wrongStore := profile.NewFileStore(profilePath, wrongKey)
	_, err := wrongStore.Load(ctx)
	if err == nil || !errors.Is(err, profile.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed on wrong key, got: %v", err)
	}

	// 2. Invalid raw key lengths (16, 24, 31, 33, 64 bytes)
	for _, l := range []int{16, 24, 31, 33, 64} {
		badStore := profile.NewFileStoreWithKDF(profilePath, make([]byte, l), profile.KDFRawKey)
		_, err := badStore.Load(ctx)
		if !errors.Is(err, profile.ErrInvalidKeyLength) {
			t.Fatalf("expected ErrInvalidKeyLength for raw key length %d, got: %v", l, err)
		}
	}

	// 3. Empty passphrase
	emptyStore := profile.NewFileStore(profilePath, nil)
	_, err = emptyStore.Load(ctx)
	if !errors.Is(err, profile.ErrEmptyPassphrase) {
		t.Fatalf("expected ErrEmptyPassphrase, got: %v", err)
	}
}

func TestE2E_Group2_ProfileManager_MultiGoroutineConcurrencyStress(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	profilePath := filepath.Join(tmpDir, "concurrency.enc")
	masterKey := setupRandomKey32(t)

	mgr := profile.NewManager(profilePath, masterKey)
	_ = mgr.Save(ctx, &profile.Profile{
		Version:     profile.CurrentSchemaVersion,
		Privacy:     profile.PrivacySettings{BlockCloudVideo: profile.BoolPtr(true)},
		Credentials: profile.CloudCredentials{AccountEmail: "concurrent@example.com", Password: profile.SecretString("Pass!1")},
		Cameras:     make(map[string]profile.CameraProfile),
	})

	const workers = 10
	const ops = 30
	var wg sync.WaitGroup
	errCh := make(chan error, workers*ops*3)

	for w := 0; w < workers; w++ {
		workerID := w
		wg.Add(3)

		// Group A: Enroll and Unenroll
		go func() {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				uuid := fmt.Sprintf("cam-w%d-%d", workerID, i)
				cam := profile.CameraProfile{
					UUID:       uuid,
					Name:       fmt.Sprintf("Cam %s", uuid),
					IPAddress:  fmt.Sprintf("192.168.1.%d", 10+i%200),
					TokenCache: profile.SecretString(fmt.Sprintf("tok-%d", i)),
				}
				if err := mgr.EnrollCamera(ctx, cam); err != nil {
					errCh <- err
					return
				}
				if i > 5 {
					_ = mgr.UnenrollCamera(ctx, fmt.Sprintf("cam-w%d-%d", workerID, i-5))
				}
			}
		}()

		// Group B: Reads
		go func() {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				p := mgr.GetProfile()
				if p != nil && (len(p.Accounts) != 1 || p.Accounts[0].AccountEmail != "concurrent@example.com") {
					errCh <- fmt.Errorf("corrupt logins in profile: %+v", p.Accounts)
					return
				}
				_ = mgr.GetPublicView()
				_, _ = mgr.HasProfile(ctx)
				time.Sleep(200 * time.Microsecond)
			}
		}()

		// Group C: Updates and Mode Toggles
		go func() {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				_, _ = mgr.Update(ctx, func(p *profile.Profile) error {
					p.Privacy.BlockCloudVideo = profile.BoolPtr(i%2 == 0)
					return nil
				})
				_, _ = mgr.Update(ctx, func(p *profile.Profile) error {
					p.Connection.Timezone = "UTC"
					return nil
				})
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("Concurrency stress error: %v", err)
	}

	finalP, err := mgr.Load(ctx)
	if err != nil {
		t.Fatalf("Load failed after concurrency stress: %v", err)
	}
	if len(finalP.Accounts) != 1 || finalP.Accounts[0].AccountEmail != "concurrent@example.com" {
		t.Fatalf("final profile corrupted: %+v", finalP)
	}
}

func TestE2E_Group2_EmptyNilMalformedProfilePayloads(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	masterKey := setupRandomKey32(t)

	// 1. 0-byte file
	emptyPath := filepath.Join(tmpDir, "empty.enc")
	_ = os.WriteFile(emptyPath, []byte(""), 0600)
	emptyStore := profile.NewFileStore(emptyPath, masterKey)
	has, err := emptyStore.HasProfile(ctx)
	if err != nil || has {
		t.Fatalf("0-byte file HasProfile should be false, got %v, err=%v", has, err)
	}
	_, err = emptyStore.Load(ctx)
	if !errors.Is(err, profile.ErrCorruptedProfile) {
		t.Fatalf("expected ErrCorruptedProfile on 0-byte file, got: %v", err)
	}

	// 2. Truncated JSON
	truncPath := filepath.Join(tmpDir, "trunc.enc")
	_ = os.WriteFile(truncPath, []byte(`{"magic":"BOMBE_ENC_V1", "cipher`), 0600)
	truncStore := profile.NewFileStore(truncPath, masterKey)
	_, err = truncStore.Load(ctx)
	if !errors.Is(err, profile.ErrCorruptedProfile) {
		t.Fatalf("expected ErrCorruptedProfile on truncated JSON, got: %v", err)
	}

	// 3. Corrupt base64 Nonce
	corruptNonceEnv := profile.EncryptedEnvelope{
		Magic:      profile.EnvelopeMagicHeader,
		Version:    profile.CurrentSchemaVersion,
		Nonce:      "%%%NotBase64%%%",
		Ciphertext: base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
	_, err = profile.DecryptPayload(&corruptNonceEnv, masterKey)
	if !errors.Is(err, profile.ErrCorruptedProfile) {
		t.Fatalf("expected ErrCorruptedProfile for invalid nonce base64, got: %v", err)
	}

	// 4. Ciphertext shorter than GCM 16-byte tag
	shortCtEnv := profile.EncryptedEnvelope{
		Magic:      profile.EnvelopeMagicHeader,
		Version:    profile.CurrentSchemaVersion,
		Nonce:      base64.StdEncoding.EncodeToString(make([]byte, 12)),
		Ciphertext: base64.StdEncoding.EncodeToString(make([]byte, 10)),
	}
	_, err = profile.DecryptPayload(&shortCtEnv, masterKey)
	if !errors.Is(err, profile.ErrCorruptedProfile) {
		t.Fatalf("expected ErrCorruptedProfile for short ciphertext, got: %v", err)
	}

	// 5. Nil profile pointer operations
	var nilP *profile.Profile
	if nilP.RedactSecrets() != nil || nilP.Clone() != nil {
		t.Fatalf("nil Profile methods should return nil")
	}
	if err := emptyStore.Save(ctx, nil); !errors.Is(err, profile.ErrCorruptedProfile) {
		t.Fatalf("Save(nil) must return ErrCorruptedProfile, got: %v", err)
	}
}

func TestE2E_Group2_NonStandardAndEdgeSubnets(t *testing.T) {
	filter, err := netstack.NewICECandidateFilter(nil, nil)
	if err != nil {
		t.Fatalf("failed to create ICECandidateFilter: %v", err)
	}

	// 1. Prohibited and dangerous IP addresses
	dangerousIPs := []string{
		// IPv6
		"2001:db8::1",
		"::1",
		"fe80::1ff:fe00:3a60",
		"ff02::1",
		"::",
		"::ffff:127.0.0.1",
		"::ffff:8.8.8.8",
		// Loopback
		"127.0.0.1",
		"127.255.255.254",
		// Link-Local / Cloud Metadata
		"169.254.169.254",
		"169.254.0.1",
		// Multicast & Broadcast
		"224.0.0.1",
		"239.255.255.250",
		"0.0.0.0",
		"255.255.255.255",
		// Public WAN & CGNAT
		"8.8.8.8",
		"1.1.1.1",
		"100.64.0.1",
	}

	for _, ipStr := range dangerousIPs {
		parsed := net.ParseIP(ipStr)
		if parsed != nil && filter.ValidateIP(parsed) {
			t.Errorf("SECURITY FLAW: ValidateIP accepted prohibited IP: %s", ipStr)
		}
		sdp := fmt.Sprintf("candidate:1 1 udp 1000 %s 5000 typ host", ipStr)
		if ip, ok := filter.ValidateCandidate(sdp); ok || ip != nil {
			t.Errorf("SECURITY FLAW: ValidateCandidate accepted prohibited IP: %s", ipStr)
		}
	}

	// 2. Single-host /32 subnet matching
	filter32, err := netstack.NewICECandidateFilter([]string{"192.168.1.100/32"}, nil)
	if err != nil {
		t.Fatalf("NewICECandidateFilter with /32 failed: %v", err)
	}

	if !filter32.ValidateIP(net.ParseIP("192.168.1.100")) {
		t.Errorf("expected 192.168.1.100 to match /32 subnet")
	}
	if filter32.ValidateIP(net.ParseIP("192.168.1.101")) {
		t.Errorf("expected 192.168.1.101 to be rejected outside /32 subnet")
	}

	// 3. RFC 5245 Boundary Parsing: component, port, and priority bounds
	validEdgeSDPs := []string{
		"candidate:1 1 udp 1 192.168.1.150 1 typ host",                // Minimum component, priority, port
		"candidate:1 256 udp 2147483647 192.168.1.150 65535 typ host", // Maximum component (256), priority (2^31-1), port (65535)
	}
	for _, sdp := range validEdgeSDPs {
		if c, err := netstack.ParseCandidate(sdp); err != nil || c == nil {
			t.Errorf("expected edge candidate to parse cleanly: %q, err=%v", sdp, err)
		}
	}

	invalidEdgeSDPs := []string{
		"candidate:1 0 udp 1000 192.168.1.150 5000 typ host",       // Component 0 (< 1)
		"candidate:1 257 udp 1000 192.168.1.150 5000 typ host",     // Component 257 (> 256)
		"candidate:1 1 udp 0 192.168.1.150 5000 typ host",          // Priority 0 (< 1)
		"candidate:1 1 udp 2147483648 192.168.1.150 5000 typ host", // Priority overflow (> 2^31-1)
		"candidate:1 1 udp 1000 192.168.1.150 0 typ host",          // Port 0 (< 1)
		"candidate:1 1 udp 1000 192.168.1.150 65536 typ host",      // Port overflow (> 65535)
	}
	for _, sdp := range invalidEdgeSDPs {
		if _, err := netstack.ParseCandidate(sdp); err == nil {
			t.Errorf("expected ParseCandidate to reject out-of-range candidate: %q", sdp)
		}
	}
}
