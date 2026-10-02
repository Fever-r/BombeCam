package profile

import "testing"

// Signing a login in again (a new password) or signing in another login only
// touches that login: every other stored login keeps its password and
// cameras, and none of them becomes special.
func TestSignInKeepsOtherLogins(t *testing.T) {
	existing := &Profile{}
	existing.UpsertAccount(CloudCredentials{AccountEmail: "first@x.com", Password: "old"})
	existing.UpsertAccount(CloudCredentials{AccountEmail: "second@x.com", Password: "p2"})
	existing.Cameras = map[string]CameraProfile{
		"c1": {UUID: "c1", AccountEmail: "first@x.com"},
		"c2": {UUID: "c2", AccountEmail: "second@x.com"},
	}
	existing.Normalize()

	got := existing.Clone()
	got.UpsertAccount(CloudCredentials{AccountEmail: "First@X.com", Password: "new"})
	got.Normalize()
	if len(got.Accounts) != 2 {
		t.Fatalf("password update dropped a login: %d accounts", len(got.Accounts))
	}
	if a, _ := got.FindAccount("first@x.com"); a.Password.Expose() != "new" {
		t.Fatalf("password not updated")
	}
	if a, ok := got.AccountForCamera("c2"); !ok || a.AccountEmail != "second@x.com" || a.Password.Expose() != "p2" {
		t.Fatalf("second login's camera lost its owner credentials")
	}

	got2 := existing.Clone()
	got2.UpsertAccount(CloudCredentials{AccountEmail: "third@x.com", Password: "p3"})
	got2.Normalize()
	if len(got2.Accounts) != 3 || got2.Credentials.AccountEmail != "" {
		t.Fatalf("expected 3 equal logins, got %d (legacy field %q)", len(got2.Accounts), got2.Credentials.AccountEmail)
	}
	if a, ok := got2.FindAccount("first@x.com"); !ok || a.Password.Expose() != "old" {
		t.Fatalf("an earlier login was dropped")
	}
}
