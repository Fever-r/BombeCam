package profile

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// ProfileManager defines high-level profile lifecycle and persistence operations.
type ProfileManager interface {
	HasProfile(ctx context.Context) (bool, error)
	Load(ctx context.Context) (*Profile, error)
	LoadProfile(ctx context.Context) (*Profile, error)
	Save(ctx context.Context, p *Profile) error
	SaveProfile(ctx context.Context, p *Profile) error
	Delete(ctx context.Context) error
	DeleteProfile(ctx context.Context) error
	GetProfile() *Profile
	GetPublicView() *PublicProfileView
	RedactSecrets() *PublicProfileView
	EnrollCamera(ctx context.Context, cam CameraProfile) error
	UnenrollCamera(ctx context.Context, camUUID string) error
	Update(ctx context.Context, fn func(p *Profile) error) (*Profile, error)
}

// defaultProfileManager is the thread-safe implementation of ProfileManager.
type defaultProfileManager struct {
	mu       sync.RWMutex
	modifyMu sync.Mutex
	store    Store
	cached   *Profile
}

// NewProfileManager creates a ProfileManager backed by the provided Store.
func NewProfileManager(store Store) ProfileManager {
	return &defaultProfileManager{
		store: store,
	}
}

// NewManager creates a ProfileManager backed by a FileStore at path with the specified key.
func NewManager(path string, key []byte) ProfileManager {
	store := NewFileStore(path, key)
	return NewProfileManager(store)
}

// NewDefaultManager creates a ProfileManager resolving keys automatically via flags, env, or keyfile.
func NewDefaultManager(profilePath, keyFlag, keyFileFlag string) (ProfileManager, error) {
	key, err := ResolveKey(keyFlag, keyFileFlag)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve profile key: %w", err)
	}
	return NewManager(profilePath, key), nil
}

// ProtectKey securely protects a master key using Windows DPAPI if running on Windows,
// or falls back to writing a file key with secure permissions (0600).
func ProtectKey(key []byte, keyPath string) ([]byte, error) {
	if len(key) == 0 {
		return nil, fmt.Errorf("cannot protect an empty profile key")
	}
	if runtime.GOOS == "windows" && DPAPIAvailable() {
		blob, err := ProtectKeyDPAPI(key)
		if err != nil {
			return nil, err
		}
		if err := writeNewProtectedKey(keyPath+".dpapi", blob); err != nil {
			return nil, err
		}
		return blob, nil
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(key)+"\n"), 0600); err != nil {
		return nil, err
	}
	return key, nil
}

// UnprotectKey loads and unprotects a master key from Windows DPAPI if on Windows,
// or falls back to reading the file key with secure permissions.
func UnprotectKey(keyPath string) ([]byte, error) {
	return ResolveOrEnsureProtectedKey(keyPath)
}

// HasProfile returns true if an encrypted profile exists on disk.
func (m *defaultProfileManager) HasProfile(ctx context.Context) (bool, error) {
	return m.store.HasProfile(ctx)
}

// Load loads and decrypts the profile from disk, caching it in memory.
func (m *defaultProfileManager) Load(ctx context.Context) (*Profile, error) {
	m.modifyMu.Lock()
	defer m.modifyMu.Unlock()

	p, err := m.store.Load(ctx)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.cached = p
	m.mu.Unlock()

	return p.Clone(), nil
}

// LoadProfile is an alias for Load.
func (m *defaultProfileManager) LoadProfile(ctx context.Context) (*Profile, error) {
	return m.Load(ctx)
}

// saveLocked persists the profile to disk and updates the in-memory cache.
// Caller MUST hold m.modifyMu.
func (m *defaultProfileManager) saveLocked(ctx context.Context, p *Profile) error {
	if p == nil {
		return ErrCorruptedProfile
	}

	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	p.UpdatedAt = time.Now().UTC()
	if p.Version == 0 {
		p.Version = CurrentSchemaVersion
	}
	if p.Cameras == nil {
		p.Cameras = make(map[string]CameraProfile)
	}
	p.Normalize()

	if err := m.store.Save(ctx, p); err != nil {
		return err
	}

	m.mu.Lock()
	m.cached = p
	m.mu.Unlock()

	return nil
}

// Save persists the profile to disk and updates the in-memory cache.
func (m *defaultProfileManager) Save(ctx context.Context, p *Profile) error {
	if p == nil {
		return ErrCorruptedProfile
	}

	m.modifyMu.Lock()
	defer m.modifyMu.Unlock()

	toSave := p.Clone()
	return m.saveLocked(ctx, toSave)
}

// SaveProfile is an alias for Save.
func (m *defaultProfileManager) SaveProfile(ctx context.Context, p *Profile) error {
	return m.Save(ctx, p)
}

// Delete removes the profile from disk and clears the cache.
func (m *defaultProfileManager) Delete(ctx context.Context) error {
	m.modifyMu.Lock()
	defer m.modifyMu.Unlock()

	if err := m.store.Delete(ctx); err != nil {
		return err
	}

	m.mu.Lock()
	m.cached = nil
	m.mu.Unlock()

	return nil
}

// DeleteProfile is an alias for Delete.
func (m *defaultProfileManager) DeleteProfile(ctx context.Context) error {
	return m.Delete(ctx)
}

// GetProfile returns a reference to the active cached in-memory profile.
func (m *defaultProfileManager) GetProfile() *Profile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cached == nil {
		return nil
	}
	return m.cached.Clone()
}

// GetPublicView returns a sanitized view of the cached profile safe for logging and API serialization.
func (m *defaultProfileManager) GetPublicView() *PublicProfileView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cached == nil {
		return nil
	}
	return m.cached.GetPublicView()
}

// RedactSecrets is an alias for GetPublicView.
func (m *defaultProfileManager) RedactSecrets() *PublicProfileView {
	return m.GetPublicView()
}

// EnrollCamera adds or updates an enrolled camera and persists the profile.
func (m *defaultProfileManager) EnrollCamera(ctx context.Context, cam CameraProfile) error {
	return m.modify(ctx, func(p *Profile) error {
		if p.Cameras == nil {
			p.Cameras = make(map[string]CameraProfile)
		}
		if cam.EnrolledAt.IsZero() {
			cam.EnrolledAt = time.Now().UTC()
		}
		p.Cameras[cam.UUID] = cam
		return nil
	})
}

// UnenrollCamera removes an enrolled camera and persists the profile.
func (m *defaultProfileManager) UnenrollCamera(ctx context.Context, camUUID string) error {
	return m.modify(ctx, func(p *Profile) error {
		if p.Cameras != nil {
			delete(p.Cameras, camUUID)
		}
		return nil
	})
}

// Update allows callers to execute arbitrary mutations on the active profile atomically.
func (m *defaultProfileManager) Update(ctx context.Context, fn func(p *Profile) error) (*Profile, error) {
	if err := m.modify(ctx, fn); err != nil {
		return nil, err
	}
	return m.GetProfile(), nil
}

// modify loads the profile if not in cache, applies fn, and saves.
func (m *defaultProfileManager) modify(ctx context.Context, fn func(p *Profile) error) error {
	m.modifyMu.Lock()
	defer m.modifyMu.Unlock()

	m.mu.RLock()
	var p *Profile
	preexisting := false
	if m.cached != nil {
		p = m.cached.Clone()
		preexisting = true
	}
	m.mu.RUnlock()

	if p == nil {
		has, err := m.store.HasProfile(ctx)
		if err != nil {
			return err
		}
		if has {
			loaded, err := m.store.Load(ctx)
			if err != nil {
				return err
			}
			p = loaded
			preexisting = true
		} else {
			p = &Profile{
				Version:   CurrentSchemaVersion,
				CreatedAt: time.Now().UTC(),
				Cameras:   make(map[string]CameraProfile),
			}
		}
	}

	if err := fn(p); err != nil {
		return err
	}

	// Do not resurrect a deleted profile: if none existed and the callback did
	// not add any credentials, accounts, or cameras, there is nothing worth
	// persisting. This stops detached background writers (e.g. a stream goroutine
	// recording a camera IP) from recreating an empty save file after a delete.
	if !preexisting && p.Credentials.AccountEmail == "" && len(p.Accounts) == 0 && len(p.Cameras) == 0 && len(p.Users) == 0 {
		return nil
	}

	p.Normalize()
	return m.saveLocked(ctx, p)
}
