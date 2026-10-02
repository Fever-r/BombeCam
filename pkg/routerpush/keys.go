package routerpush

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// KeyComment marks BombeCam's lines in the router's authorized_keys.
const KeyComment = "bombecam-gateway"

// KeySet is BombeCam's own SSH identity for the router: an Ed25519 key, plus
// an RSA key for routers whose SSH server predates Ed25519. The router script
// installs the public halves restricted to its "gate" command, so the keys can
// only turn camera blocking on and off, never open a shell.
type KeySet struct {
	signers []ssh.Signer
	pem     string
}

// NewKeySet generates a fresh key set.
func NewKeySet() (*KeySet, error) {
	_, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	rsaPriv, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return nil, fmt.Errorf("generate rsa key: %w", err)
	}
	var b strings.Builder
	for _, k := range []any{edPriv, rsaPriv} {
		blk, err := ssh.MarshalPrivateKey(k, KeyComment)
		if err != nil {
			return nil, fmt.Errorf("encode key: %w", err)
		}
		b.Write(pem.EncodeToMemory(blk))
	}
	return ParseKeySet(b.String())
}

// ParseKeySet reads a key set written by PEM.
func ParseKeySet(data string) (*KeySet, error) {
	rest := []byte(data)
	ks := &KeySet{pem: data}
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		raw, err := ssh.ParseRawPrivateKey(pem.EncodeToMemory(blk))
		if err != nil {
			return nil, fmt.Errorf("parse router key: %w", err)
		}
		s, err := ssh.NewSignerFromKey(raw)
		if err != nil {
			return nil, fmt.Errorf("parse router key: %w", err)
		}
		ks.signers = append(ks.signers, s)
	}
	if len(ks.signers) == 0 {
		return nil, errors.New("no router key found")
	}
	return ks, nil
}

// PEM returns the private keys (OpenSSH format). Store it encrypted.
func (k *KeySet) PEM() string { return k.pem }

// Signers returns the keys for authentication, Ed25519 first.
func (k *KeySet) Signers() []ssh.Signer { return append([]ssh.Signer(nil), k.signers...) }

// AuthorizedKeys returns the public keys as "TYPE BASE64" (no comment).
func (k *KeySet) AuthorizedKeys() []string {
	out := make([]string, 0, len(k.signers))
	for _, s := range k.signers {
		out = append(out, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey()))))
	}
	return out
}
