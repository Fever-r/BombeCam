package profile

import (
	"context"
	"path/filepath"
	"testing"
)

// After a profile is deleted, a background-style Update that touches a
// non-existent camera must NOT recreate the save file.
func TestUpdateDoesNotResurrectDeletedProfile(t *testing.T) {
	ctx := context.Background()
	key := []byte("0123456789abcdef0123456789abcdef") // 32 bytes
	pm := NewManager(filepath.Join(t.TempDir(), "p.enc"), key)

	// Create a profile with a login and a camera.
	_, err := pm.Update(ctx, func(p *Profile) error {
		p.UpsertAccount(CloudCredentials{AccountEmail: "owner@example.com", Password: "pw"})
		p.Cameras = map[string]CameraProfile{"cam1": {UUID: "cam1", AccountEmail: "owner@example.com"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if has, _ := pm.HasProfile(ctx); !has {
		t.Fatal("profile should exist after create")
	}

	// Delete everything.
	if err := pm.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if has, _ := pm.HasProfile(ctx); has {
		t.Fatal("profile should be gone after delete")
	}
	if pm.GetProfile() != nil {
		t.Fatal("cache should be cleared after delete")
	}

	// Simulate the detached stream IP-writer running after delete.
	_, err = pm.Update(ctx, func(p *Profile) error {
		if cam, ok := p.Cameras["cam1"]; ok {
			cam.IPAddress = "10.0.0.5"
			p.Cameras["cam1"] = cam
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if has, _ := pm.HasProfile(ctx); has {
		t.Fatal("background update must not resurrect the deleted profile")
	}
	if pm.GetProfile() != nil {
		t.Fatal("background update must not repopulate the cache")
	}
}
