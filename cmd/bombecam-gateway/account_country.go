package main

import (
	"fmt"
	"github.com/Fever-r/BombeCam/pkg/profile"
	"regexp"
	"strings"
)

var countryCodeFormat = regexp.MustCompile(`^[1-9][0-9]{0,2}$`)

// Normalize syntax, not an account's location: only the user/vendor can confirm
// where that account was registered. A language alone is not a country.
func normalizedCountry(value string) (string, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "+")
	return value, countryCodeFormat.MatchString(value)
}

func resolveAccountCountry(explicit, email string, saved *profile.Profile, sm *SessionManager) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		if country, ok := normalizedCountry(explicit); ok {
			return country, nil
		}
		return "", fmt.Errorf("Enter the account's country calling code: 1 to 3 digits, optionally beginning with +.")
	}
	if saved != nil {
		if account, found := saved.FindAccount(email); found {
			if country, ok := normalizedCountry(account.Country); ok {
				return country, nil
			}
		}
	}
	// A configured first-run country or this same account's active country can
	// be reused. Another account's country is not evidence for a new login.
	if sm != nil && (profile.NormalizeEmail(sm.Email()) == profile.NormalizeEmail(email) || (sm.Email() == "" && saved == nil)) {
		if country, ok := normalizedCountry(sm.Country()); ok {
			return country, nil
		}
	}
	if country := detectHostCountry(); country != "" {
		return country, nil
	}
	return "", fmt.Errorf("Enter the country calling code used for this Osaio login; its country could not be determined.")
}
