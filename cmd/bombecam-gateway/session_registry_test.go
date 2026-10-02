package main

import "testing"

func signedIn(email string) *SessionManager {
	sm := NewSessionManager("1", "test", testServerKey)
	sm.mu.Lock()
	sm.email = email
	sm.status = SessionStatusAuthenticated
	sm.mu.Unlock()
	return sm
}

// Every login gets its own session, made from the template; the template is
// never a login's session, so no login is the "main" one.
func TestRegistryGivesEveryLoginItsOwnSession(t *testing.T) {
	template := NewSessionManager("1", "test", testServerKey)
	template.Cloud().GlobalBase = "http://fake-osaio.test"
	reg := NewSessionRegistry(template, "1", "test", testServerKey)

	a := reg.Ensure("A@Example.com")
	b := reg.Ensure("b@example.com")
	if a == template || b == template || a == b {
		t.Fatal("each login needs its own session, separate from the template")
	}
	if got, ok := reg.Get("a@example.com"); !ok || got != a {
		t.Fatal("Get should find a login by its normalized email")
	}
	if reg.Ensure("a@example.com") != a {
		t.Fatal("Ensure created a duplicate session for a login")
	}
	if a.Cloud() == template.Cloud() || a.Cloud().GlobalBase != "http://fake-osaio.test" {
		t.Fatal("a login's session should copy the template's client, not share it")
	}
	if a.Email() != "a@example.com" {
		t.Fatalf("session email = %q", a.Email())
	}
}

func TestRegistryRemoveAndClear(t *testing.T) {
	template := NewSessionManager("1", "test", testServerKey)
	reg := NewSessionRegistry(template, "1", "test", testServerKey)
	reg.Ensure("a@example.com")
	reg.Ensure("b@example.com")

	reg.Remove("a@example.com")
	if _, ok := reg.Get("a@example.com"); ok {
		t.Fatal("Remove kept the login")
	}
	if _, ok := reg.Get("b@example.com"); !ok {
		t.Fatal("Remove dropped another login")
	}
	reg.Clear()
	if len(reg.Sessions()) != 0 || reg.Template() != template {
		t.Fatal("Clear should forget every login and keep the template")
	}
}

// The page's status: signed in as soon as any login is; otherwise what most
// needs the user; with no login, the gateway's own state.
func TestRegistryStatusSumsUpLogins(t *testing.T) {
	template := NewSessionManager("1", "test", testServerKey)
	template.SetStatus(SessionStatusCredentialsMissing, "first run")
	reg := NewSessionRegistry(template, "1", "test", testServerKey)
	if st := reg.Status(); st.Status != SessionStatusCredentialsMissing {
		t.Fatalf("no logins: %+v", st)
	}
	reg.SetRestoring(true)
	if st := reg.Status(); st.Status != SessionStatusAuthenticating {
		t.Fatalf("restoring: %+v", st)
	}
	reg.SetRestoring(false)

	bad := reg.Ensure("bad@example.com")
	bad.SetStatus(SessionStatusInvalidCredentials, "refused")
	down := reg.Ensure("down@example.com")
	down.SetStatus(SessionStatusNetworkTimeout, "timeout")
	if st := reg.Status(); st.Status != SessionStatusInvalidCredentials || st.AccountEmail != "" {
		t.Fatalf("a refused login should be reported, without naming it: %+v", st)
	}
	if reg.AnyAuthenticated() {
		t.Fatal("nobody is signed in")
	}
	good := reg.Ensure("good@example.com")
	good.SetStatus(SessionStatusAuthenticated, "")
	if st := reg.Status(); st.Status != SessionStatusAuthenticated || !reg.AnyAuthenticated() {
		t.Fatalf("one working login is enough: %+v", st)
	}

	template.SetStatus(SessionStatusServerKeyMissing, serverKeyMissingMessage)
	if st := reg.Status(); st.Status != SessionStatusServerKeyMissing {
		t.Fatalf("a missing server key concerns every login: %+v", st)
	}
}

// CloudFor only hands out a signed-in login's cloud; a camera whose login is
// not signed in waits instead of using another login.
func TestRegistryCloudForNeedsSignedInLogin(t *testing.T) {
	reg := NewSessionRegistry(NewSessionManager("1", "test", testServerKey), "1", "test", testServerKey)
	a := reg.Ensure("a@example.com")
	if reg.CloudFor("a@example.com") != nil {
		t.Fatal("a signed-out login has no cloud to stream with")
	}
	a.SetStatus(SessionStatusAuthenticated, "")
	if reg.CloudFor("a@example.com") != a.Cloud() {
		t.Fatal("a signed-in login's cloud")
	}
	if reg.CloudFor("nobody@example.com") != nil {
		t.Fatal("unknown login")
	}
}
