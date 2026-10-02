// Package obscure keeps the Osaio values that builds include (the server key
// and the app ID) from appearing as plain text in BombeCam binaries.
//
// It is obfuscation, not protection: the code that reads the values back is
// open source and the app ID travels to Osaio with every request. It only
// keeps the values out of a casual look with strings or grep. The output is
// the same for the same input, so release builds stay reproducible.
package obscure

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

const prefix = "obs1."

// Hide returns value in obscured form, or "" for "". name tells values apart,
// so two values never share a mask.
func Hide(name, value string) string {
	if value == "" {
		return ""
	}
	return prefix + base64.RawURLEncoding.EncodeToString(xor(name, []byte(value)))
}

// Reveal undoes Hide. A value without Hide's prefix is returned as it is, and
// one that can't be decoded gives "".
func Reveal(name, hidden string) string {
	if !strings.HasPrefix(hidden, prefix) {
		return hidden
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(hidden, prefix))
	if err != nil {
		return ""
	}
	return string(xor(name, b))
}

// xor masks b with a SHA-256 chain seeded by name.
func xor(name string, b []byte) []byte {
	out := make([]byte, len(b))
	block := sha256.Sum256([]byte("BombeCam/" + name))
	for i := range b {
		if i > 0 && i%len(block) == 0 {
			block = sha256.Sum256(block[:])
		}
		out[i] = b[i] ^ block[i%len(block)]
	}
	return out
}
