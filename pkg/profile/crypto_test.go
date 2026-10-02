package profile

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
)

func TestCrypto_RoundTrip_RawKey(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	plaintext := []byte("secret profile payload to be encrypted at rest 12345")
	params := KDFParams{
		Algorithm: KDFRawKey,
		KeyLength: 32,
	}

	env, err := EncryptPayload(plaintext, key, params)
	if err != nil {
		t.Fatalf("EncryptPayload failed: %v", err)
	}

	decrypted, err := DecryptPayload(env, key)
	if err != nil {
		t.Fatalf("DecryptPayload failed: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted payload mismatch: got %q, want %q", string(decrypted), string(plaintext))
	}
}

func TestCrypto_RoundTrip_Argon2id(t *testing.T) {
	passphrase := []byte("UserProvidedPassphrase!@#123")
	salt, err := GenerateSalt(16)
	if err != nil {
		t.Fatalf("GenerateSalt failed: %v", err)
	}

	params := DefaultArgon2idParams(salt)
	key, err := DeriveKey(passphrase, params)
	if err != nil {
		t.Fatalf("DeriveKey failed: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("derived key length is %d, expected 32", len(key))
	}

	plaintext := []byte("argon2id encrypted test data")
	env, err := EncryptPayload(plaintext, key, params)
	if err != nil {
		t.Fatalf("EncryptPayload failed: %v", err)
	}

	// Re-derive key using envelope's salt
	derivedKey2, err := DeriveKey(passphrase, env.KDF)
	if err != nil {
		t.Fatalf("DeriveKey from envelope failed: %v", err)
	}

	decrypted, err := DecryptPayload(env, derivedKey2)
	if err != nil {
		t.Fatalf("DecryptPayload failed: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted payload mismatch: got %q, want %q", string(decrypted), string(plaintext))
	}
}

func TestCrypto_RoundTrip_PBKDF2(t *testing.T) {
	passphrase := []byte("Pbkdf2TestPassphrase789$")
	salt, err := GenerateSalt(16)
	if err != nil {
		t.Fatalf("GenerateSalt failed: %v", err)
	}

	params := DefaultPBKDF2Params(salt)
	key, err := DeriveKey(passphrase, params)
	if err != nil {
		t.Fatalf("DeriveKey failed: %v", err)
	}

	plaintext := []byte("pbkdf2 encrypted test data")
	env, err := EncryptPayload(plaintext, key, params)
	if err != nil {
		t.Fatalf("EncryptPayload failed: %v", err)
	}

	derivedKey2, err := DeriveKey(passphrase, env.KDF)
	if err != nil {
		t.Fatalf("DeriveKey from envelope failed: %v", err)
	}

	decrypted, err := DecryptPayload(env, derivedKey2)
	if err != nil {
		t.Fatalf("DecryptPayload failed: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted mismatch: got %q, want %q", string(decrypted), string(plaintext))
	}
}

func TestCrypto_TamperRejection_CiphertextBitFlip(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	plaintext := []byte("tamper test payload")
	params := KDFParams{Algorithm: KDFRawKey, KeyLength: 32}

	env, err := EncryptPayload(plaintext, key, params)
	if err != nil {
		t.Fatalf("EncryptPayload failed: %v", err)
	}

	rawCiphertext, err := base64.StdEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		t.Fatalf("DecodeString failed: %v", err)
	}

	// Flip a bit in the ciphertext / tag
	rawCiphertext[len(rawCiphertext)/2] ^= 0x40
	env.Ciphertext = base64.StdEncoding.EncodeToString(rawCiphertext)

	_, err = DecryptPayload(env, key)
	if err == nil {
		t.Fatalf("expected decryption error on tampered ciphertext, got nil")
	}
	if !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got: %v", err)
	}
}

func TestCrypto_TamperRejection_NonceTampered(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	plaintext := []byte("nonce tamper test payload")
	params := KDFParams{Algorithm: KDFRawKey, KeyLength: 32}

	env, err := EncryptPayload(plaintext, key, params)
	if err != nil {
		t.Fatalf("EncryptPayload failed: %v", err)
	}

	rawNonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		t.Fatalf("DecodeString failed: %v", err)
	}

	rawNonce[0] ^= 0x01
	env.Nonce = base64.StdEncoding.EncodeToString(rawNonce)

	_, err = DecryptPayload(env, key)
	if err == nil {
		t.Fatalf("expected decryption error on tampered nonce, got nil")
	}
	if !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got: %v", err)
	}
}

func TestCrypto_TamperRejection_AADMetadata(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	plaintext := []byte("aad tamper test payload")
	params := KDFParams{Algorithm: KDFRawKey, KeyLength: 32}

	// 1. Corrupt magic header
	env, _ := EncryptPayload(plaintext, key, params)
	env.Magic = "CORRUPTED_MAGIC"
	_, err := DecryptPayload(env, key)
	if err == nil {
		t.Fatalf("expected error on tampered magic header")
	}

	// 2. Corrupt envelope version (causes AAD mismatch)
	env, _ = EncryptPayload(plaintext, key, params)
	env.Version = 999
	_, err = DecryptPayload(env, key)
	if err == nil {
		t.Fatalf("expected decryption error on tampered version")
	}
	if !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed on tampered version, got: %v", err)
	}

	// 3. Corrupt KDF algorithm in AAD (causes AAD mismatch)
	env, _ = EncryptPayload(plaintext, key, params)
	env.KDF.Algorithm = "argon2id_tampered"
	_, err = DecryptPayload(env, key)
	if err == nil {
		t.Fatalf("expected decryption error on tampered KDF algorithm")
	}
	if !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed on tampered KDF algorithm, got: %v", err)
	}
}

func TestCrypto_WrongKey_Rejection(t *testing.T) {
	key1 := make([]byte, 32)
	_, _ = rand.Read(key1)

	key2 := make([]byte, 32)
	_, _ = rand.Read(key2)

	plaintext := []byte("wrong key test")
	params := KDFParams{Algorithm: KDFRawKey, KeyLength: 32}

	env, err := EncryptPayload(plaintext, key1, params)
	if err != nil {
		t.Fatalf("EncryptPayload failed: %v", err)
	}

	_, err = DecryptPayload(env, key2)
	if err == nil {
		t.Fatalf("expected decryption failure with wrong key, got nil")
	}
	if !errors.Is(err, ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got: %v", err)
	}
}

func TestCrypto_InvalidKeyLengths(t *testing.T) {
	plaintext := []byte("invalid key length test")
	params := KDFParams{Algorithm: KDFRawKey, KeyLength: 32}

	// Too short (16 bytes)
	shortKey := make([]byte, 16)
	_, err := EncryptPayload(plaintext, shortKey, params)
	if !errors.Is(err, ErrInvalidKeyLength) {
		t.Fatalf("expected ErrInvalidKeyLength, got: %v", err)
	}

	// Too long (64 bytes)
	longKey := make([]byte, 64)
	_, err = EncryptPayload(plaintext, longKey, params)
	if !errors.Is(err, ErrInvalidKeyLength) {
		t.Fatalf("expected ErrInvalidKeyLength, got: %v", err)
	}
}

func TestCrypto_EmptyPassphrase_Rejection(t *testing.T) {
	salt, _ := GenerateSalt(16)
	params := DefaultArgon2idParams(salt)

	_, err := DeriveKey(nil, params)
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("expected ErrEmptyPassphrase, got: %v", err)
	}

	_, err = DeriveKey([]byte(""), params)
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("expected ErrEmptyPassphrase, got: %v", err)
	}
}
