package routerpush

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeRouter is a minimal SSH server that accepts one password and answers
// every exec request with "cmd=<command> stdin=<n>" and exit status 0 (or 3
// when the command contains "fail").
type fakeRouter struct {
	addr     string
	hostKey  ssh.Signer
	password string
	lastCmd  chan string
	keys     map[string]bool // authorized public keys (wire format)
}

func newFakeRouter(t *testing.T, password string) *fakeRouter {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	fr := &fakeRouter{addr: ln.Addr().String(), hostKey: signer, password: password, lastCmd: make(chan string, 8), keys: map[string]bool{}}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == fr.password {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if fr.keys[string(k.Marshal())] {
				return nil, nil
			}
			return nil, errors.New("unknown key")
		},
	}
	cfg.AddHostKey(signer)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go fr.serve(c, cfg)
		}
	}()
	return fr
}

func (fr *fakeRouter) serve(c net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range creqs {
				if req.Type != "exec" {
					_ = req.Reply(false, nil)
					continue
				}
				n := binary.BigEndian.Uint32(req.Payload[:4])
				cmd := string(req.Payload[4 : 4+n])
				_ = req.Reply(true, nil)
				fr.lastCmd <- cmd
				in, _ := io.ReadAll(ch)
				fmt.Fprintf(ch, "cmd=%s stdin=%d\n", cmd, len(in))
				status := uint32(0)
				if strings.Contains(cmd, "fail") {
					status = 3
				}
				b := make([]byte, 4)
				binary.BigEndian.PutUint32(b, status)
				_, _ = ch.SendRequest("exit-status", false, b)
				ch.Close()
			}
		}()
	}
}

func TestRunTrustOnFirstUseThenPinned(t *testing.T) {
	fr := newFakeRouter(t, "s3cret")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Run(ctx, Target{Address: fr.addr, Password: "s3cret"}, "sh /tmp/x apply yes", []byte("#!/bin/sh\n"))
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Output, "cmd=sh /tmp/x apply yes stdin=10") {
		t.Fatalf("unexpected result %+v", res)
	}
	want := ssh.FingerprintSHA256(fr.hostKey.PublicKey())
	if res.HostKey != want {
		t.Fatalf("host key %q, want %q", res.HostKey, want)
	}
	// Pinned and matching.
	if _, err := Run(ctx, Target{Address: fr.addr, Password: "s3cret", PinnedHostKey: want}, "true", nil); err != nil {
		t.Fatal(err)
	}
	// Pinned and different: refused before any password is sent.
	_, err = Run(ctx, Target{Address: fr.addr, Password: "s3cret", PinnedHostKey: "SHA256:somethingelse"}, "true", nil)
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("expected ErrHostKeyChanged, got %v", err)
	}
}

func TestRunBadPasswordAndExitCode(t *testing.T) {
	fr := newFakeRouter(t, "right")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := Run(ctx, Target{Address: fr.addr, Password: "wrong"}, "true", nil); !errors.Is(err, ErrAuth) {
		t.Fatalf("expected ErrAuth, got %v", err)
	}
	res, err := Run(ctx, Target{Address: fr.addr, Password: "right"}, "fail please", nil)
	if err != nil || res.ExitCode != 3 {
		t.Fatalf("exit code: %+v %v", res, err)
	}
}

func TestRunUnreachable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	_, err := Run(context.Background(), Target{Address: addr, DialTimeout: time.Second}, "true", nil)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("expected ErrUnreachable, got %v", err)
	}
}

func TestNormalizeAddress(t *testing.T) {
	ok := map[string]string{
		"192.168.8.1":      "192.168.8.1:22",
		"192.168.8.1:2222": "192.168.8.1:2222",
		"router.lan":       "router.lan:22",
		"fe80::1":          "[fe80::1]:22",
	}
	for in, want := range ok {
		got, err := NormalizeAddress(in)
		if err != nil || got != want {
			t.Errorf("NormalizeAddress(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "http://192.168.8.1", "root@192.168.8.1", "a b", "bad_host!"} {
		if _, err := NormalizeAddress(bad); err == nil {
			t.Errorf("NormalizeAddress(%q) accepted", bad)
		}
	}
}

var testKeys *KeySet

func keySetForTest(t *testing.T) *KeySet {
	t.Helper()
	if testKeys == nil {
		ks, err := NewKeySet()
		if err != nil {
			t.Fatal(err)
		}
		testKeys = ks
	}
	return testKeys
}

func TestKeySetRoundTrip(t *testing.T) {
	ks := keySetForTest(t)
	pubs := ks.AuthorizedKeys()
	if len(pubs) != 2 || !strings.HasPrefix(pubs[0], "ssh-ed25519 ") || !strings.HasPrefix(pubs[1], "ssh-rsa ") {
		t.Fatalf("authorized keys = %q", pubs)
	}
	for _, p := range pubs {
		if strings.Contains(p, "\n") || len(strings.Fields(p)) != 2 {
			t.Errorf("want \"TYPE BASE64\", got %q", p)
		}
	}
	again, err := ParseKeySet(ks.PEM())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(again.AuthorizedKeys(), ",") != strings.Join(pubs, ",") {
		t.Fatal("key set did not survive PEM round trip")
	}
	if _, err := ParseKeySet("not a key"); err == nil {
		t.Fatal("garbage parsed as a key set")
	}
}

func TestRunWithKey(t *testing.T) {
	fr := newFakeRouter(t, "s3cret")
	ks := keySetForTest(t)
	ctx := context.Background()

	// Not authorized yet, no password: authentication error.
	if _, err := Run(ctx, Target{Address: fr.addr, Signers: ks.Signers()}, "status", nil); !errors.Is(err, ErrAuth) {
		t.Fatalf("unauthorized key: got %v, want ErrAuth", err)
	}
	// Key rejected, password accepted: falls back to the password.
	if _, err := Run(ctx, Target{Address: fr.addr, Signers: ks.Signers(), Password: "s3cret"}, "status", nil); err != nil {
		t.Fatalf("password fallback failed: %v", err)
	}
	<-fr.lastCmd
	// Only the RSA key authorized (an SSH server without Ed25519): still works.
	fr.keys[string(ks.Signers()[1].PublicKey().Marshal())] = true
	res, err := Run(ctx, Target{Address: fr.addr, Signers: ks.Signers()}, "apply", []byte("camera aa:bb:cc:dd:ee:01 Test Camera\n"))
	if err != nil {
		t.Fatalf("key auth failed: %v", err)
	}
	if got := <-fr.lastCmd; got != "apply" {
		t.Fatalf("command = %q", got)
	}
	if !strings.Contains(res.Output, "stdin=37") {
		t.Fatalf("stdin not delivered: %q", res.Output)
	}
	if _, err := Run(ctx, Target{Address: fr.addr}, "status", nil); !errors.Is(err, ErrAuth) {
		t.Fatalf("no credentials: got %v, want ErrAuth", err)
	}
}

func TestHostKeyAlgorithms(t *testing.T) {
	if got := hostKeyAlgorithms("", ""); got[0] != ssh.KeyAlgoED25519 {
		t.Errorf("new router: %v, want Ed25519 first", got)
	}
	if got := hostKeyAlgorithms("SHA256:x", ""); got != nil {
		t.Errorf("pin of unknown type: %v, want Go's default order (nil)", got)
	}
	if got := hostKeyAlgorithms("SHA256:x", ssh.KeyAlgoRSA); got[0] != ssh.KeyAlgoRSASHA256 || !contains(got, ssh.KeyAlgoED25519) {
		t.Errorf("rsa pin: %v", got)
	}
	if got := hostKeyAlgorithms("SHA256:x", ssh.KeyAlgoECDSA256); got[0] != ssh.KeyAlgoECDSA256 || len(got) != len(hostKeyOrder) {
		t.Errorf("ecdsa pin: %v", got)
	}
}
