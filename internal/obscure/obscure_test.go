package obscure

import (
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	for _, v := range []string{"a", "synthetic-app-id", strings.Repeat("0123456789abcdef", 9)} {
		h := Hide("server_key", v)
		if strings.Contains(h, v) || !strings.HasPrefix(h, prefix) {
			t.Fatalf("Hide(%q) = %q shows the value", v, h)
		}
		if got := Reveal("server_key", h); got != v {
			t.Fatalf("Reveal(Hide(%q)) = %q", v, got)
		}
		if Hide("server_key", v) != h {
			t.Fatal("Hide must give the same output for the same input (reproducible builds)")
		}
	}
}

func TestNamesUseDifferentMasks(t *testing.T) {
	if Hide("server_key", "same-value") == Hide("app_id", "same-value") {
		t.Fatal("two names share a mask")
	}
	if Reveal("app_id", Hide("server_key", "value")) == "value" {
		t.Fatal("a value revealed under the wrong name")
	}
}

func TestEdges(t *testing.T) {
	if Hide("x", "") != "" || Reveal("x", "") != "" {
		t.Fatal("empty must stay empty")
	}
	if Reveal("x", "plain-value") != "plain-value" {
		t.Fatal("a value without the prefix must pass through")
	}
	if Reveal("x", prefix+"!!!") != "" {
		t.Fatal("undecodable input must give nothing")
	}
	// Builds pass the hidden value through make, sh and PowerShell unquoted.
	h := Hide("app_id", "made-up-value/with+chars=")
	for _, r := range h {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			t.Fatalf("hidden value has %q, which needs quoting", r)
		}
	}
}
