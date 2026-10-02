package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// BombeCam's own sign-in: the administrator's password, an optional
// authenticator-app code (TOTP, RFC 6238) and one-time recovery codes.
// Nothing here is related to Osaio logins.

// MinPasswordLength is the shortest administrator password accepted.
const MinPasswordLength = 8

// Argon2id cost for password hashes (OWASP's recommended minimum: 19 MiB,
// 2 passes, 1 lane), kept in each hash so it can be raised later.
const (
	pwTime    = 2
	pwMemory  = 19 * 1024
	pwThreads = 1
	pwKeyLen  = 32
)

// ErrWeakPassword: the password is shorter than MinPasswordLength.
var ErrWeakPassword = fmt.Errorf("use at least %d characters", MinPasswordLength)

// HashPassword returns an encoded Argon2id hash of password:
// "argon2id$v=19$m=<KiB>,t=<passes>,p=<lanes>$<salt>$<hash>".
func HashPassword(password string) (string, error) {
	if len([]rune(password)) < MinPasswordLength {
		return "", ErrWeakPassword
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(password), salt, pwTime, pwMemory, pwThreads, pwKeyLen)
	return fmt.Sprintf("argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, pwMemory, pwTime, pwThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(sum)), nil
}

// VerifyPassword reports whether password matches an encoded hash.
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false
	}
	var version int
	var mem, iters uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[1], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &mem, &iters, &threads); err != nil || mem == 0 || iters == 0 || threads == 0 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[3])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[4])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iters, mem, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// TOTP: 6 digits, 30-second steps, HMAC-SHA1 — what every authenticator
// app supports by default.
const (
	totpDigits = 6
	totpPeriod = 30
	// totpSkew accepts the previous and next step too, for clocks a little
	// apart.
	totpSkew = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit secret, base32-encoded as
// authenticator apps expect.
func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// TOTPURI is the otpauth:// address an authenticator app reads from the QR
// code.
func TOTPURI(secret, account, issuer string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// totpAt computes the code for a time step.
func totpAt(key []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, v%mod)
}

// VerifyTOTP checks a code against secret at now. A code is accepted only
// for a step later than lastStep, so each code works once; on success it
// returns the step to remember as the new lastStep.
func VerifyTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(key) == 0 {
		return 0, false
	}
	cur := now.Unix() / totpPeriod
	for d := -totpSkew; d <= totpSkew; d++ {
		step := cur + int64(d)
		if step <= lastStep {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(totpAt(key, step)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// RecoveryCodeCount is how many one-time recovery codes are issued at once.
const RecoveryCodeCount = 10

// NewRecoveryCodes returns fresh codes to show once ("xxxx-xxxx-xx") and
// the hashes to store.
func NewRecoveryCodes() (codes, hashes []string, err error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789" // no look-alikes
	for i := 0; i < RecoveryCodeCount; i++ {
		b := make([]byte, 10)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		var sb strings.Builder
		for j, c := range b {
			if j == 4 || j == 8 {
				sb.WriteByte('-')
			}
			sb.WriteByte(alphabet[int(c)%len(alphabet)])
		}
		codes = append(codes, sb.String())
		hashes = append(hashes, HashRecoveryCode(sb.String()))
	}
	return codes, hashes, nil
}

// HashRecoveryCode normalizes a typed recovery code and hashes it. The codes
// carry about 49 bits each and sign-in is rate-limited, so a plain SHA-256
// is enough to keep them out of the stored profile.
func HashRecoveryCode(code string) string {
	norm := strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	sum := sha256.Sum256([]byte("bombecam-recovery:" + norm))
	return hex.EncodeToString(sum[:])
}

// UseRecoveryCode looks code up in hashes; if found it returns the hashes
// left after using it.
func UseRecoveryCode(hashes []string, code string) ([]string, bool) {
	h := HashRecoveryCode(code)
	for i, stored := range hashes {
		if subtle.ConstantTimeCompare([]byte(stored), []byte(h)) == 1 {
			left := append(append([]string(nil), hashes[:i]...), hashes[i+1:]...)
			return left, true
		}
	}
	return hashes, false
}

// RevokeSession ends one session (sign out).
func (om *OperatorManager) RevokeSession(token string) {
	om.mu.Lock()
	defer om.mu.Unlock()
	delete(om.sessions, token)
}

// RevokeOperatorSessions ends every signed-in session (after a password
// change, or when the administrator is deleted). Visitor sessions stay.
func (om *OperatorManager) RevokeOperatorSessions() {
	om.mu.Lock()
	defer om.mu.Unlock()
	for tok, s := range om.sessions {
		if s != nil && s.IsOperator {
			delete(om.sessions, tok)
		}
	}
}

// TOTPCode is the authenticator code for secret at t (for tests and tools).
func TOTPCode(secret string, t time.Time) string {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return ""
	}
	return totpAt(key, t.Unix()/totpPeriod)
}
