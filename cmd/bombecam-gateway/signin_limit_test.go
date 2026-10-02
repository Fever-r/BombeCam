package main

import (
	"testing"
	"time"
)

func TestSignInLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := &signInLimiter{now: func() time.Time { return now }, fails: map[string]signInFailures{}}
	for i := 0; i < signInMaxFailures; i++ {
		if l.blocked("192.168.1.20") {
			t.Fatalf("blocked after %d failures", i)
		}
		l.failed("192.168.1.20")
	}
	if !l.blocked("192.168.1.20") {
		t.Fatal("not blocked after the maximum number of failures")
	}
	if l.blocked("192.168.1.21") {
		t.Fatal("another address was blocked")
	}
	now = now.Add(signInWindow)
	if l.blocked("192.168.1.20") {
		t.Fatal("still blocked after the window ended")
	}
	l.failed("192.168.1.22")
	l.succeeded("192.168.1.22")
	if _, ok := l.fails["192.168.1.22"]; ok {
		t.Fatal("a successful sign-in did not clear the failures")
	}
}
