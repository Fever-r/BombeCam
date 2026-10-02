package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("a short password was accepted")
	}
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "argon2id$v=19$m=19456,t=2,p=1$") || strings.Contains(h, "correct horse") {
		t.Fatalf("unexpected hash format: %s", h)
	}
	if !VerifyPassword(h, "correct horse battery") || VerifyPassword(h, "correct horse batterY") || VerifyPassword(h, "") {
		t.Fatal("verification")
	}
	h2, _ := HashPassword("correct horse battery")
	if h2 == h {
		t.Fatal("hashes must be salted")
	}
	for _, bad := range []string{"", "argon2id$", "bcrypt$x$y$z$w", "argon2id$v=19$m=0,t=2,p=1$AAAA$AAAA"} {
		if VerifyPassword(bad, "correct horse battery") {
			t.Fatalf("malformed hash accepted: %q", bad)
		}
	}
}

// RFC 6238 appendix B test vectors (SHA-1), last 6 digits.
func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	for _, tc := range []struct {
		unix int64
		code string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}} {
		step, ok := VerifyTOTP(secret, tc.code, time.Unix(tc.unix, 0), 0)
		if !ok || step != tc.unix/30 {
			t.Errorf("t=%d code %s: ok=%v step=%d", tc.unix, tc.code, ok, step)
		}
	}
}

func TestTOTPReplayAndSkew(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil || len(secret) != 32 {
		t.Fatalf("secret %q %v", secret, err)
	}
	key, _ := b32.DecodeString(secret)
	now := time.Unix(1_800_000_000, 0)
	cur := now.Unix() / 30
	code := totpAt(key, cur)
	step, ok := VerifyTOTP(secret, code, now, 0)
	if !ok || step != cur {
		t.Fatal("current code refused")
	}
	if _, ok := VerifyTOTP(secret, code, now, step); ok {
		t.Fatal("a code must work only once")
	}
	if _, ok := VerifyTOTP(secret, totpAt(key, cur-1), now, 0); !ok {
		t.Fatal("the previous step should be accepted for clock drift")
	}
	if _, ok := VerifyTOTP(secret, totpAt(key, cur-2), now, 0); ok {
		t.Fatal("a code a minute old must be refused")
	}
	if _, ok := VerifyTOTP(secret, "12345", now, 0); ok {
		t.Fatal("wrong length accepted")
	}
	if _, ok := VerifyTOTP(secret, " "+code[:3]+" "+code[3:], now, 0); !ok {
		t.Fatal("spaces in a typed code should be ignored")
	}
}

func TestTOTPURI(t *testing.T) {
	u := TOTPURI("ABC", "admin", "BombeCam")
	if !strings.HasPrefix(u, "otpauth://totp/BombeCam:admin?") || !strings.Contains(u, "secret=ABC") || !strings.Contains(u, "issuer=BombeCam") {
		t.Fatalf("uri %s", u)
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, hashes, err := NewRecoveryCodes()
	if err != nil || len(codes) != RecoveryCodeCount || len(hashes) != RecoveryCodeCount {
		t.Fatalf("%v %v", codes, err)
	}
	if len(codes[0]) != 12 || codes[0][4] != '-' || codes[0][9] != '-' {
		t.Fatalf("format %q", codes[0])
	}
	left, ok := UseRecoveryCode(hashes, " "+strings.ToUpper(codes[3])+" ")
	if !ok || len(left) != RecoveryCodeCount-1 {
		t.Fatal("a typed code (any case, spaces) should work")
	}
	if _, ok := UseRecoveryCode(left, codes[3]); ok {
		t.Fatal("a recovery code must work only once")
	}
	if _, ok := UseRecoveryCode(hashes, "aaaa-bbbb-cc"); ok {
		t.Fatal("unknown code accepted")
	}
}

func TestRevokeSessions(t *testing.T) {
	om := NewOperatorManager(OperatorConfig{})
	op, _ := om.CreateSession()
	op2, _ := om.CreateSession()
	visitor, _ := om.CreateVisitorSession()
	om.RevokeSession(op.Token)
	if _, ok := om.VerifySession(op.Token); ok {
		t.Fatal("revoked session still valid")
	}
	om.RevokeOperatorSessions()
	if _, ok := om.VerifySession(op2.Token); ok {
		t.Fatal("operator sessions should all end")
	}
	if _, ok := om.VerifySession(visitor.Token); !ok {
		t.Fatal("visitor sessions stay")
	}
}
