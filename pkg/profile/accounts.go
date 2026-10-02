package profile

import (
	"strings"
	"time"
)

// NormalizeEmail lowercases and trims an account email for stable keying.
func NormalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// Normalize enforces the login-pool invariants and migrates older profiles.
// It is safe to call repeatedly and is invoked on load and before save.
//
//   - Accounts is the only list of logins; no login is the "main" one.
//   - A profile from an older version kept its first login in Credentials:
//     that login moves into Accounts (its copy there may be older) and
//     Credentials is emptied. Cameras saved without an owner came from it.
//   - Accounts are de-duplicated by normalized email.
func (p *Profile) Normalize() {
	if p == nil {
		return
	}
	if p.Version == 0 {
		p.Version = CurrentSchemaVersion
	}

	// De-duplicate accounts by normalized email, keeping the latest entry.
	seen := make(map[string]int, len(p.Accounts))
	deduped := make([]CloudCredentials, 0, len(p.Accounts))
	for _, a := range p.Accounts {
		a.AccountEmail = NormalizeEmail(a.AccountEmail)
		if a.AccountEmail == "" {
			continue
		}
		if idx, ok := seen[a.AccountEmail]; ok {
			deduped[idx] = a
			continue
		}
		seen[a.AccountEmail] = len(deduped)
		deduped = append(deduped, a)
	}
	p.Accounts = deduped

	// Older profiles: move the single Credentials login into the pool.
	legacy := NormalizeEmail(p.Credentials.AccountEmail)
	if legacy != "" {
		c := p.Credentials
		c.AccountEmail = legacy
		if idx, ok := seen[legacy]; ok {
			p.Accounts[idx] = c
		} else {
			p.Accounts = append(p.Accounts, c)
		}
	}
	p.Credentials = CloudCredentials{}

	for uuid, c := range p.Cameras {
		c.AccountEmail = NormalizeEmail(c.AccountEmail)
		if c.AccountEmail == "" {
			switch {
			case legacy != "":
				c.AccountEmail = legacy
			case len(p.Accounts) == 1:
				c.AccountEmail = p.Accounts[0].AccountEmail
			}
		}
		p.Cameras[uuid] = c
	}
}

// UpsertAccount adds a login to the pool or replaces its stored details.
func (p *Profile) UpsertAccount(c CloudCredentials) {
	c.AccountEmail = NormalizeEmail(c.AccountEmail)
	if c.AccountEmail == "" {
		return
	}
	p.UpdatedAt = time.Now().UTC()
	for i, a := range p.Accounts {
		if NormalizeEmail(a.AccountEmail) == c.AccountEmail {
			p.Accounts[i] = c
			return
		}
	}
	p.Accounts = append(p.Accounts, c)
}

// FindAccount returns the stored details of a login, if present.
func (p *Profile) FindAccount(email string) (CloudCredentials, bool) {
	email = NormalizeEmail(email)
	if email == "" {
		return CloudCredentials{}, false
	}
	for _, a := range p.Accounts {
		if NormalizeEmail(a.AccountEmail) == email {
			return a, true
		}
	}
	if NormalizeEmail(p.Credentials.AccountEmail) == email {
		return p.Credentials, true // an older profile not yet normalized
	}
	return CloudCredentials{}, false
}

// RemoveAccount drops a stored login. Cameras are not touched here.
func (p *Profile) RemoveAccount(email string) {
	email = NormalizeEmail(email)
	if email == "" {
		return
	}
	filtered := p.Accounts[:0]
	for _, a := range p.Accounts {
		if NormalizeEmail(a.AccountEmail) != email {
			filtered = append(filtered, a)
		}
	}
	p.Accounts = filtered
	if NormalizeEmail(p.Credentials.AccountEmail) == email {
		p.Credentials = CloudCredentials{}
	}
}

// AccountForCamera returns the stored login a camera was added with.
func (p *Profile) AccountForCamera(uuid string) (CloudCredentials, bool) {
	if c, ok := p.Cameras[uuid]; ok {
		return p.FindAccount(c.AccountEmail)
	}
	return CloudCredentials{}, false
}

// AccountEmails lists the stored login emails.
func (p *Profile) AccountEmails() []string {
	out := make([]string, 0, len(p.Accounts))
	for _, a := range p.Accounts {
		out = append(out, a.AccountEmail)
	}
	return out
}

// CamerasForAccount lists the enrolled camera UUIDs owned by a login.
func (p *Profile) CamerasForAccount(email string) []string {
	email = NormalizeEmail(email)
	var out []string
	for uuid, c := range p.Cameras {
		if NormalizeEmail(c.AccountEmail) == email {
			out = append(out, uuid)
		}
	}
	return out
}
