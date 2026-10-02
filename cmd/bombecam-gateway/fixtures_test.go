package main

import (
	"context"
	"time"

	"github.com/Fever-r/BombeCam/pkg/auth"
	"github.com/Fever-r/BombeCam/pkg/bridge"
	"github.com/Fever-r/BombeCam/pkg/profile"
	"github.com/Fever-r/BombeCam/pkg/serverkey"
)

// testServerKey is a made-up key; tests run against mock clouds only.
const testServerKey = serverkey.Static("synthetic-test-key-0001")

// Fixture injection is deliberately absent from production builds.
//
// Called on the gateway's template session with an email, it sets up that
// login's session in the pool (sharing the template's cloud, so a test's
// fake Osaio address applies), which is where a signed-in login lives.
func (sm *SessionManager) SetStateForTest(status SessionStatus, email, uid, errStr string, devs []bridge.Device) {
	if email != "" {
		reg := GatewaySessions()
		if reg == nil || (reg.Template() != sm && !reg.holds(sm)) {
			reg = NewSessionRegistry(sm, sm.Country(), sm.PhoneCode(), nil)
			SetGatewaySessions(reg)
		}
		if reg.Template() == sm {
			login := reg.Ensure(email)
			login.mu.Lock()
			login.cloud = sm.Cloud()
			login.mu.Unlock()
			sm = login
		}
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.status = status
	sm.email = email
	if sm.cloud != nil {
		sm.cloud.UID = uid
	}
	sm.lastError = errStr
	if status == SessionStatusAuthenticated {
		sm.lastLoginTS = time.Now().UTC()
	}
	sm.cachedDevs = devs
	sm.discoveryTS = time.Now().UTC()
}

// holds reports whether sm is one of the pool's login sessions.
func (r *SessionRegistry) holds(sm *SessionManager) bool {
	for _, s := range r.Sessions() {
		if s == sm {
			return true
		}
	}
	return false
}

// seedAdmin saves a BombeCam administrator into pm's profile.
func seedAdmin(t interface {
	Helper()
	Fatal(...any)
}, pm profile.ProfileManager, name, password string, requireLocal bool) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pm.Update(context.Background(), func(p *profile.Profile) error {
		p.Users = []profile.UserProfile{{ID: "admin-1", Name: name, Role: profile.RoleAdmin, PasswordHash: profile.SecretString(hash)}}
		p.Security.RequireLocalSignIn = requireLocal
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
