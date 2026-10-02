package profile

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeMigratesLegacySingleAccount(t *testing.T) {
	p := &Profile{
		Credentials: CloudCredentials{AccountEmail: "Legacy@Example.com", Password: "pw"},
		Cameras: map[string]CameraProfile{
			"cam1": {UUID: "cam1"}, // no owner: it came from the old single login
		},
	}
	p.Normalize()
	if len(p.Accounts) != 1 || p.Accounts[0].AccountEmail != "legacy@example.com" || p.Accounts[0].Password.Expose() != "pw" {
		t.Fatalf("expected one normalized account, got %+v", p.Accounts)
	}
	if p.Credentials.AccountEmail != "" {
		t.Fatal("the old single-login field should be emptied once migrated")
	}
	if got := p.Cameras["cam1"].AccountEmail; got != "legacy@example.com" {
		t.Fatalf("camera not attributed to its login: %q", got)
	}
}

func TestMultiAccountRoundTripAndOwnerLookup(t *testing.T) {
	p := &Profile{}
	p.UpsertAccount(CloudCredentials{AccountEmail: "a@x.com", Password: "p1"})
	p.UpsertAccount(CloudCredentials{AccountEmail: "b@x.com", Password: "p2"})
	p.Cameras = map[string]CameraProfile{
		"c1": {UUID: "c1", AccountEmail: "a@x.com"},
		"c2": {UUID: "c2", AccountEmail: "b@x.com"},
	}
	p.Normalize()

	b, err := encodeInternal(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeInternal(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Accounts) != 2 {
		t.Fatalf("want 2 accounts after round-trip, got %d", len(got.Accounts))
	}
	if creds, ok := got.AccountForCamera("c2"); !ok || creds.AccountEmail != "b@x.com" {
		t.Fatalf("owner lookup failed for c2: %+v ok=%v", creds, ok)
	}
	if got.Accounts[1].Password.Expose() != "p2" {
		t.Fatalf("secret not persisted for secondary account")
	}
}

// The old single login may be older in Accounts (renewals only updated the
// single-login field), so the migrated copy wins.
func TestNormalizeLegacyLoginReplacesItsOlderCopy(t *testing.T) {
	p := &Profile{
		Credentials: CloudCredentials{AccountEmail: "a@x.com", AuthToken: "fresh"},
		Accounts:    []CloudCredentials{{AccountEmail: "a@x.com", AuthToken: "stale"}, {AccountEmail: "b@x.com"}},
	}
	p.Normalize()
	if len(p.Accounts) != 2 || p.Accounts[0].AuthToken.Expose() != "fresh" {
		t.Fatalf("accounts: %+v", p.Accounts)
	}
}

// Logins are equal: removing one leaves the others as they were, and a
// camera without a stored login has no fallback owner.
func TestRemoveAccountLeavesOthersAlone(t *testing.T) {
	p := &Profile{Cameras: map[string]CameraProfile{"c1": {UUID: "c1", AccountEmail: "a@x.com"}}}
	p.UpsertAccount(CloudCredentials{AccountEmail: "a@x.com"})
	p.UpsertAccount(CloudCredentials{AccountEmail: "b@x.com"})
	p.Normalize()
	p.RemoveAccount("a@x.com")
	p.Normalize()
	if len(p.Accounts) != 1 || p.Accounts[0].AccountEmail != "b@x.com" {
		t.Fatalf("expected only b@x.com left, got %+v", p.Accounts)
	}
	if _, ok := p.AccountForCamera("c1"); ok {
		t.Fatal("a camera whose login was removed must not fall back to another login")
	}
}

// The administrator is stored only in the encrypted file, with its secrets,
// and never appears in the public view or in formatted output.
func TestAdminRoundTripAndRedaction(t *testing.T) {
	p := &Profile{Security: SecuritySettings{RequireLocalSignIn: true}}
	p.Users = []UserProfile{{ID: "u1", Name: "admin", Role: RoleAdmin, PasswordHash: "argon2id$secret-hash",
		TOTPSecret: "TOTPSECRETVALUE", TOTPLastStep: 7, RecoveryCodes: []string{"h1", "h2"}}}
	b, err := encodeInternal(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeInternal(b)
	if err != nil {
		t.Fatal(err)
	}
	a, ok := got.Admin()
	if !ok || a.PasswordHash.Expose() != "argon2id$secret-hash" || !a.TwoStep() || a.TOTPLastStep != 7 || len(a.RecoveryCodes) != 2 || !got.Security.RequireLocalSignIn {
		t.Fatalf("admin after round trip: %+v %+v", a, got.Security)
	}
	view, _ := json.Marshal(got)
	for _, leak := range []string{"secret-hash", "TOTPSECRETVALUE", "h1"} {
		if strings.Contains(string(view), leak) || strings.Contains(fmt.Sprintf("%+v %v %#v", got, *got, got), leak) {
			t.Fatalf("admin secret %q leaked", leak)
		}
	}
	cp := got.Clone()
	cp.UpdateAdmin(func(u *UserProfile) { u.RecoveryCodes[0] = "changed" })
	if a, _ := got.Admin(); a.RecoveryCodes[0] != "h1" {
		t.Fatal("Clone must copy the recovery codes")
	}
}
