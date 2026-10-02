package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// signInLimiter slows password guessing on /api/v1/operator/token: after
// signInMaxFailures wrong passwords from one address within signInWindow,
// that address is refused until the window ends.
const (
	signInMaxFailures = 5
	signInWindow      = 10 * time.Minute
)

type signInLimiter struct {
	mu    sync.Mutex
	now   func() time.Time
	fails map[string]signInFailures
}

type signInFailures struct {
	count int
	since time.Time
}

var operatorSignIns = &signInLimiter{now: time.Now, fails: map[string]signInFailures{}}

// requestAddress is the caller's IP address without the port.
func requestAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return strings.Trim(host, "[]")
}

// blocked reports whether addr has used up its attempts for now.
func (l *signInLimiter) blocked(addr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[addr]
	if !ok {
		return false
	}
	if l.now().Sub(f.since) >= signInWindow {
		delete(l.fails, addr)
		return false
	}
	return f.count >= signInMaxFailures
}

func (l *signInLimiter) failed(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	f, ok := l.fails[addr]
	if !ok || now.Sub(f.since) >= signInWindow {
		f = signInFailures{since: now}
	}
	f.count++
	l.fails[addr] = f
	// Keep the table small: forget windows that have ended.
	if len(l.fails) > 1024 {
		for a, v := range l.fails {
			if now.Sub(v.since) >= signInWindow {
				delete(l.fails, a)
			}
		}
	}
}

func (l *signInLimiter) succeeded(addr string) {
	l.mu.Lock()
	delete(l.fails, addr)
	l.mu.Unlock()
}
