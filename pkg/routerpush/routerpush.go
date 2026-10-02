// Package routerpush runs BombeCam's router script on the user's router over
// SSH, so "Block cloud video" can be applied from BombeCam.
//
// The router's password is used for one connection and never stored. That
// connection installs BombeCam's own key (see KeySet), restricted on the
// router to the script's "gate" command; later updates use the key. The
// router's host key is pinned the first time (trust on first use); a later
// connection that presents a different key is refused, so another device on
// the network cannot pose as the router and collect the password.
package routerpush

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Errors the caller can show as specific advice.
var (
	ErrHostKeyChanged = errors.New("the router's identity (SSH host key) changed since BombeCam last connected")
	ErrAuth           = errors.New("the router rejected the username or password")
	ErrUnreachable    = errors.New("could not reach the router over SSH")
	ErrBadAddress     = errors.New("router address must be an IP address or host name, optionally with :port")
)

// Target is the router to connect to.
type Target struct {
	Address       string       // "192.168.8.1" or "192.168.8.1:22"
	User          string       // default "root"
	Password      string       // used when set
	Signers       []ssh.Signer // BombeCam's key(s), tried before the password
	PinnedHostKey string       // "SHA256:..."; empty trusts the first key presented
	// PinnedHostKeyType is the pinned key's type (e.g. "ssh-ed25519"). The
	// router is asked for that key type first, so it presents the pinned key.
	// Empty with a pin means an older pin of unknown type: Go's default order
	// is used, which is what made that pin.
	PinnedHostKeyType string
	DialTimeout       time.Duration
}

// Result is what the command produced.
type Result struct {
	Output      string
	HostKey     string // SHA256 fingerprint of the key the router presented
	HostKeyType string // that key's type, e.g. "ssh-ed25519"
	ExitCode    int
}

// hostKeyOrder lists host key algorithms with Ed25519 first. (Dropbear
// 2022.83 with only an Ed25519 host key aborts the handshake when a client
// prefers RSA, as Go's default order does.)
var hostKeyOrder = []string{
	ssh.KeyAlgoED25519,
	ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
	ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSA,
}

// hostKeyAlgorithms returns the preference order for a connection.
func hostKeyAlgorithms(pinned, pinnedType string) []string {
	if pinned != "" && pinnedType == "" {
		return nil // Go's default order, which produced the pin
	}
	if pinnedType == "" {
		return hostKeyOrder
	}
	first := []string{pinnedType}
	if pinnedType == ssh.KeyAlgoRSA {
		first = []string{ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSA}
	}
	out := append([]string(nil), first...)
	for _, a := range hostKeyOrder {
		if !contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

const maxOutput = 256 << 10

// NormalizeAddress validates a router address and adds the default SSH port.
func NormalizeAddress(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" || strings.ContainsAny(addr, "/@ \t") {
		return "", ErrBadAddress
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = strings.Trim(addr, "[]"), "22"
	}
	if host == "" {
		return "", ErrBadAddress
	}
	if net.ParseIP(host) == nil {
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || strings.Trim(label, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") != "" {
				return "", ErrBadAddress
			}
		}
	}
	return net.JoinHostPort(host, port), nil
}

// Run connects, runs command with stdin, and returns its combined output.
// A non-zero exit status is reported in Result.ExitCode with a nil error;
// errors mean the command could not be run at all.
func Run(ctx context.Context, t Target, command string, stdin []byte) (Result, error) {
	var res Result
	addr, err := NormalizeAddress(t.Address)
	if err != nil {
		return res, err
	}
	user := t.User
	if user == "" {
		user = "root"
	}
	timeout := t.DialTimeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}

	var auth []ssh.AuthMethod
	if len(t.Signers) > 0 {
		auth = append(auth, ssh.PublicKeys(t.Signers...))
	}
	if t.Password != "" || len(auth) == 0 {
		auth = append(auth,
			ssh.Password(t.Password),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = t.Password
				}
				return answers, nil
			}))
	}
	keyChanged := false
	cfg := &ssh.ClientConfig{
		User:              user,
		Auth:              auth,
		HostKeyAlgorithms: hostKeyAlgorithms(t.PinnedHostKey, t.PinnedHostKeyType),
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			res.HostKey = ssh.FingerprintSHA256(key)
			res.HostKeyType = key.Type()
			if t.PinnedHostKey != "" && res.HostKey != t.PinnedHostKey {
				keyChanged = true
				return ErrHostKeyChanged
			}
			return nil
		},
		Timeout: timeout,
	}

	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return res, fmt.Errorf("%w (%s): %v", ErrUnreachable, addr, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		switch {
		case keyChanged:
			return res, fmt.Errorf("%w: expected %s, got %s", ErrHostKeyChanged, t.PinnedHostKey, res.HostKey)
		case strings.Contains(err.Error(), "unable to authenticate"):
			return res, ErrAuth
		default:
			return res, fmt.Errorf("%w (%s): %v", ErrUnreachable, addr, err)
		}
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return res, fmt.Errorf("could not open an SSH session: %w", err)
	}
	defer sess.Close()
	var out limitedBuffer
	sess.Stdout = &out
	sess.Stderr = &out
	sess.Stdin = bytes.NewReader(stdin)

	err = sess.Run(command)
	res.Output = out.String()
	var exitErr *ssh.ExitError
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return res, fmt.Errorf("router command timed out: %w", ctx.Err())
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitStatus()
	default:
		var missing *ssh.ExitMissingError
		if errors.As(err, &missing) {
			res.ExitCode = -1
			return res, nil
		}
		return res, fmt.Errorf("router command failed: %w", err)
	}
	return res, nil
}

// limitedBuffer collects stdout and stderr. x/crypto/ssh copies the two
// streams in separate goroutines, so writes are serialised. (bytes.Buffer is
// a named field, not embedded: embedding would promote ReadFrom and io.Copy
// would bypass the lock.)
type limitedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if room := maxOutput - b.buf.Len(); room < len(p) {
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return n, nil
	}
	b.buf.Write(p)
	return n, nil
}

var _ io.Writer = (*limitedBuffer)(nil)
