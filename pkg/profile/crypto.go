package profile

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/pbkdf2"
)

// Sentinel errors for cryptographic and profile persistence operations.
var (
	ErrDecryptionFailed    = errors.New("profile decryption failed: corrupted ciphertext or incorrect key")
	ErrCorruptedProfile    = errors.New("profile payload corrupted or tampered")
	ErrInvalidKeyLength    = errors.New("encryption key must be exactly 32 bytes")
	ErrEmptyPassphrase     = errors.New("encryption passphrase cannot be empty")
	ErrProfileNotFound     = errors.New("profile not found")
	ErrInsecurePermissions = errors.New("insecure file permissions: must be 0600 or stricter")
	ErrMissingCredentials  = errors.New("missing credentials: email and password are required")
)

// Supported Key Derivation Function (KDF) algorithms.
const (
	KDFArgon2id = "argon2id"
	KDFPBKDF2   = "pbkdf2_sha256"
	KDFRawKey   = "raw_key"
)

// KDFParams defines parameters used for key derivation and envelope binding.
type KDFParams struct {
	Algorithm   string `json:"algorithm"`
	Salt        string `json:"salt"` // Base64 encoded
	Iterations  uint32 `json:"iterations,omitempty"`
	MemoryKiB   uint32 `json:"memory_kib,omitempty"`
	Parallelism uint8  `json:"parallelism,omitempty"`
	KeyLength   uint32 `json:"key_length"`
}

// DefaultArgon2idParams returns robust defaults for Argon2id key derivation.
func DefaultArgon2idParams(salt []byte) KDFParams {
	return KDFParams{
		Algorithm:   KDFArgon2id,
		Salt:        base64.StdEncoding.EncodeToString(salt),
		Iterations:  3,
		MemoryKiB:   64 * 1024, // 64 MiB
		Parallelism: 2,
		KeyLength:   32,
	}
}

// DefaultPBKDF2Params returns robust defaults for PBKDF2-HMAC-SHA256 key derivation.
func DefaultPBKDF2Params(salt []byte) KDFParams {
	return KDFParams{
		Algorithm:  KDFPBKDF2,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Iterations: 100000,
		KeyLength:  32,
	}
}

// EncryptedEnvelope represents the authenticated at-rest container stored on disk.
type EncryptedEnvelope struct {
	Magic      string    `json:"magic"`      // "BOMBE_ENC_V1"
	Version    int       `json:"version"`    // Envelope version
	KDF        KDFParams `json:"kdf"`        // KDF parameters
	Nonce      string    `json:"nonce"`      // Base64-encoded 12-byte nonce
	Ciphertext string    `json:"ciphertext"` // Base64-encoded ciphertext + 16-byte GCM tag
	CreatedAt  time.Time `json:"created_at"`
}

// GenerateSalt creates cryptographically secure random bytes for KDF salting.
func GenerateSalt(length int) ([]byte, error) {
	if length <= 0 {
		length = 16
	}
	s := make([]byte, length)
	if _, err := io.ReadFull(rand.Reader, s); err != nil {
		return nil, fmt.Errorf("failed to generate random salt: %w", err)
	}
	return s, nil
}

// ComputeAAD binds envelope metadata into Additional Authenticated Data for AEAD integrity.
func ComputeAAD(magic string, version int, algorithm string) []byte {
	return []byte(fmt.Sprintf("%s|v%d|%s", magic, version, algorithm))
}

// DeriveKey derives a 32-byte AES key from a passphrase and KDF parameters.
func DeriveKey(passphrase []byte, params KDFParams) ([]byte, error) {
	if params.Algorithm == KDFRawKey {
		if len(passphrase) != 32 {
			return nil, ErrInvalidKeyLength
		}
		k := make([]byte, 32)
		copy(k, passphrase)
		return k, nil
	}

	if len(passphrase) == 0 {
		return nil, ErrEmptyPassphrase
	}

	salt, err := base64.StdEncoding.DecodeString(params.Salt)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid salt encoding", ErrCorruptedProfile)
	}

	keyLen := params.KeyLength
	if keyLen == 0 {
		keyLen = 32
	}

	switch params.Algorithm {
	case KDFArgon2id:
		iters := params.Iterations
		if iters == 0 {
			iters = 3
		}
		mem := params.MemoryKiB
		if mem == 0 {
			mem = 64 * 1024
		}
		par := params.Parallelism
		if par == 0 {
			par = 2
		}
		return argon2.IDKey(passphrase, salt, iters, mem, par, keyLen), nil

	case KDFPBKDF2:
		iters := params.Iterations
		if iters == 0 {
			iters = 100000
		}
		return pbkdf2.Key(passphrase, salt, int(iters), int(keyLen), sha256.New), nil

	default:
		return nil, fmt.Errorf("%w: unsupported KDF algorithm %q", ErrCorruptedProfile, params.Algorithm)
	}
}

// EncryptPayload encrypts plaintext using AES-256-GCM with AAD binding.
func EncryptPayload(plaintext []byte, key []byte, params KDFParams) (*EncryptedEnvelope, error) {
	if len(key) != 32 {
		return nil, ErrInvalidKeyLength
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	aad := ComputeAAD(EnvelopeMagicHeader, CurrentSchemaVersion, params.Algorithm)
	sealed := gcm.Seal(nil, nonce, plaintext, aad)

	return &EncryptedEnvelope{
		Magic:      EnvelopeMagicHeader,
		Version:    CurrentSchemaVersion,
		KDF:        params,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(sealed),
		CreatedAt:  time.Now().UTC(),
	}, nil
}

// DecryptPayload decrypts and verifies an EncryptedEnvelope using AES-256-GCM.
func DecryptPayload(env *EncryptedEnvelope, key []byte) ([]byte, error) {
	if env == nil {
		return nil, ErrCorruptedProfile
	}
	if env.Magic != EnvelopeMagicHeader {
		return nil, fmt.Errorf("%w: unrecognized magic header", ErrCorruptedProfile)
	}
	if len(key) != 32 {
		return nil, ErrInvalidKeyLength
	}

	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil || len(nonce) == 0 {
		return nil, fmt.Errorf("%w: invalid nonce", ErrCorruptedProfile)
	}

	ciphertext, err := base64.StdEncoding.DecodeString(env.Ciphertext)
	if err != nil || len(ciphertext) < 16 {
		return nil, fmt.Errorf("%w: invalid ciphertext payload", ErrCorruptedProfile)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("%w: incorrect nonce size", ErrCorruptedProfile)
	}

	aad := ComputeAAD(env.Magic, env.Version, env.KDF.Algorithm)
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, ErrDecryptionFailed
	}

	return plaintext, nil
}
