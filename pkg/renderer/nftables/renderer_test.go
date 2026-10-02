package nftables

import (
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/Fever-r/BombeCam/pkg/policy"
)

var cams = []policy.Subject{
	{ID: "cam1", Name: "Test Camera", MAC: "AA:BB:CC:DD:EE:01"},
	{ID: "cam2", Name: "Porch", MAC: "aa:bb:cc:dd:ee:02"},
}

var resolved = map[string][]net.IP{
	policy.ControlHost:     {net.ParseIP("3.1.2.3"), net.ParseIP("3.1.2.4"), net.ParseIP("2600::1")},
	policy.StreamSetupHost: {net.ParseIP("52.9.9.9"), net.ParseIP("10.1.1.1")},
}

func compile(t *testing.T, block bool, opts policy.Options) string {
	t.Helper()
	doc, err := policy.Compile(cams, policy.Setting{BlockCloudVideo: block}, opts)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRenderer(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Compile(doc, resolved)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBlockYesRuleset(t *testing.T) {
	out := compile(t, true, policy.Options{})
	mustContain := []string{
		"table inet bombecam",
		"delete table inet bombecam",
		"elements = { aa:bb:cc:dd:ee:01, aa:bb:cc:dd:ee:02 }",
		"priority filter - 5",
		"ether saddr @cameras jump camera_out",
		"ip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 } return",
		"set control_v4",
		"elements = { 3.1.2.3, 3.1.2.4 }",
		"ip daddr @control_v4 tcp dport 8883 counter goto capped",
		"ip daddr @stream_setup_v4 tcp dport 443 counter goto capped",
		"meta nfproto ipv4 udp dport 53 counter goto capped",
		"meta nfproto ipv4 tcp dport 53 counter goto capped",
		"meta nfproto ipv4 udp dport 123 counter goto capped",
		`counter drop comment "everything else, IPv4 and IPv6"`,
		"ether saddr aa:bb:cc:dd:ee:01 limit rate over 4096 bytes/second burst 8192 bytes counter drop",
		"ether saddr aa:bb:cc:dd:ee:02 limit rate over 4096 bytes/second burst 8192 bytes counter drop",
		"counter accept",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("ruleset missing %q\n%s", s, out)
		}
	}
	// No IPv6 or private addresses from DNS make it into allow sets.
	for _, bad := range []string{"2600::1", "10.1.1.1"} {
		if strings.Contains(out, bad) {
			t.Errorf("ruleset contains %s", bad)
		}
	}
	// Nothing but the named hosts may reach 443 or 8883.
	for _, line := range strings.Split(out, "\n") {
		if (strings.Contains(line, "dport 443") || strings.Contains(line, "dport 8883")) && !strings.Contains(line, "ip daddr @") {
			t.Errorf("web/TLS port open to any destination: %s", line)
		}
		for _, verb := range []string{"dnat", "snat", "redirect", "masquerade", "reject", "mangle", "set mark", "tproxy"} {
			if strings.Contains(line, verb) && !strings.HasPrefix(strings.TrimSpace(line), "#") {
				t.Errorf("ruleset must only allow/deny/rate-limit, found %q in: %s", verb, line)
			}
		}
	}
}

func TestBlockNoRemovesTable(t *testing.T) {
	out := compile(t, false, policy.Options{})
	if !strings.Contains(out, "delete table inet bombecam") {
		t.Fatal("No must delete BombeCam's table")
	}
	if strings.Contains(out, "chain") {
		t.Fatalf("No must not install any chain:\n%s", out)
	}
}

func TestBlockStreamSetupOption(t *testing.T) {
	out := compile(t, true, policy.Options{BlockStreamSetup: true})
	if strings.Contains(out, "stream_setup") || strings.Contains(out, "dport 443") {
		t.Fatalf("stream setup still allowed:\n%s", out)
	}
}

func TestUnresolvedHostFailsClosed(t *testing.T) {
	doc, _ := policy.Compile(cams, policy.Setting{BlockCloudVideo: true}, policy.Options{})
	r, _ := NewRenderer(DefaultConfig())
	out, err := r.Compile(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings()) != 2 {
		t.Fatalf("expected 2 warnings, got %v", r.Warnings())
	}
	if strings.Contains(out, "elements = { 3.") {
		t.Fatal("unexpected addresses")
	}
}

func TestConfigValidation(t *testing.T) {
	for _, cfg := range []Config{
		{TableName: "counter"},
		{TableName: "bad name"},
		{TableName: "t", EnableMasq: true, WANInterface: `eth0"; flush ruleset`, CameraSubnet: "10.0.0.0/24"},
		{TableName: "t", EnableMasq: true, WANInterface: "eth0", CameraSubnet: "nope"},
	} {
		if _, err := NewRenderer(cfg); err == nil {
			t.Errorf("config %+v accepted", cfg)
		}
	}
}

// TestNftSyntax loads the ruleset with `nft -c` inside a throwaway network
// namespace when the test runs as root on Linux with nft installed.
func TestNftSyntax(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("needs root on Linux")
	}
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft not installed")
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not installed")
	}
	for _, block := range []bool{true, false} {
		out := compile(t, block, policy.Options{})
		cmd := exec.Command("unshare", "-n", "nft", "-c", "-f", "-")
		cmd.Stdin = strings.NewReader(out)
		if b, err := cmd.CombinedOutput(); err != nil {
			if strings.Contains(string(b), "Operation not permitted") {
				t.Skip("namespaces not permitted here")
			}
			t.Fatalf("nft -c rejected block=%v ruleset: %v\n%s\n%s", block, err, b, out)
		}
	}
}
