//go:build windows

package profile

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProtectedKey_InvalidExistingBlobPreserved(t *testing.T) {
	for _, original := range [][]byte{nil, []byte("synthetic-invalid-DPAPI-key")} {
		t.Run(string(original), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profile.key")
			if err := os.WriteFile(path+".dpapi", original, 0600); err != nil {
				t.Fatal(err)
			}
			// A valid legacy file must not mask a broken protected key.
			legacy := []byte("0123456789abcdef0123456789abcdef")
			if err := os.WriteFile(path, legacy, 0600); err != nil {
				t.Fatal(err)
			}
			for _, resolve := range []func(string) ([]byte, error){ResolveOrEnsureProtectedKey, UnprotectKey} {
				key, err := resolve(path)
				if key != nil || !errors.Is(err, ErrKeyRecoveryRequired) {
					t.Fatalf("wanted recovery error with no replacement key: %v", err)
				}
				after, err := os.ReadFile(path + ".dpapi")
				if err != nil || !bytes.Equal(after, original) {
					t.Fatal("existing protected key changed")
				}
				after, err = os.ReadFile(path)
				if err != nil || !bytes.Equal(after, legacy) {
					t.Fatal("legacy key was changed or removed")
				}
			}
		})
	}
}

func TestProtectedKey_CannotReadExistingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.key")
	if err := os.Mkdir(path+".dpapi", 0700); err != nil {
		t.Fatal(err)
	}
	key, err := ResolveOrEnsureProtectedKey(path)
	if key != nil || !errors.Is(err, ErrKeyRecoveryRequired) {
		t.Fatalf("wanted recovery error: %v", err)
	}
	if stat, err := os.Stat(path + ".dpapi"); err != nil || !stat.IsDir() {
		t.Fatal("unreadable key path was replaced")
	}
}

func TestProtectedKey_EmptyLegacyIsNotReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.key")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if key, err := ResolveOrEnsureProtectedKey(path); key != nil || !errors.Is(err, ErrKeyRecoveryRequired) {
		t.Fatalf("wanted recovery error: %v", err)
	}
	if _, err := os.Stat(path + ".dpapi"); !os.IsNotExist(err) {
		t.Fatal("replacement key was created")
	}
}

func TestProtectedKey_MigratedPassphraseDecryptsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	keyPath := filepath.Join(root, "profile.key")
	passphrase := []byte("synthetic legacy passphrase")
	profilePath := filepath.Join(root, "profile.enc")
	original := &Profile{Credentials: CloudCredentials{AccountEmail: "key@example.test", Password: "synthetic-secret"}}
	if err := NewFileStore(profilePath, passphrase).Save(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, passphrase, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		key, err := ResolveOrEnsureProtectedKey(keyPath)
		if err != nil || !bytes.Equal(key, passphrase) {
			t.Fatalf("passphrase lost during migration/restart: %v", err)
		}
		after, err := NewFileStore(profilePath, key).Load(context.Background())
		if err != nil || len(after.Accounts) != 1 || after.Accounts[0].Password != original.Credentials.Password {
			t.Fatalf("profile no longer decrypts: %v", err)
		}
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("plaintext key remains after migration")
	}
}

func TestProtectedKey_ExplicitWriteDoesNotReplaceExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.key")
	original := []byte("0123456789abcdef0123456789abcdef")
	if _, err := ProtectKey(original, path); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(path + ".dpapi")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProtectKey([]byte("different synthetic passphrase"), path); !errors.Is(err, ErrKeyRecoveryRequired) {
		t.Fatalf("existing key accepted overwrite: %v", err)
	}
	after, err := os.ReadFile(path + ".dpapi")
	if err != nil || !bytes.Equal(blob, after) {
		t.Fatal("key was overwritten")
	}
}
