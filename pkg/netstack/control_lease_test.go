package netstack

import (
	"strings"
	"testing"
	"time"
)

func TestTemporaryControlIsEnrolledCanaryOnlyAndBounded(t *testing.T) {
	m, err := NewRulesetManager(RulesetConfig{Cameras: []string{"02:00:00:00:00:01", "02:00:00:00:00:02"}})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := m.RenderTemporaryControl("02:00:00:00:00:02", "camtest", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"flags timeout", "02:00:00:00:00:02 timeout 30s", `iifname "camtest" ether saddr @verification_canary accept`, "ether saddr @cameras jump camera_out", "counter drop"} {
		if !strings.Contains(rules, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(rules, "02:00:00:00:00:01 timeout") {
		t.Fatal("real camera bypassed")
	}
	for _, tc := range []struct {
		mac, iface string
		ttl        time.Duration
	}{
		{"02:00:00:00:00:03", "camtest", time.Second}, {"02:00:00:00:00:02", `bad"; accept`, time.Second},
		{"02:00:00:00:00:02", "camtest", 0}, {"02:00:00:00:00:02", "camtest", 31 * time.Second},
	} {
		if _, err := m.RenderTemporaryControl(tc.mac, tc.iface, tc.ttl); err == nil {
			t.Fatalf("unsafe lease accepted: %+v", tc)
		}
	}
	ordinary, err := m.Render(true)
	if err != nil || strings.Contains(ordinary, "verification_canary") {
		t.Fatalf("lease contaminated ordinary policy: %v", err)
	}
}

func TestLeaseReadbackRequiresExpiringBoundedSingleCanary(t *testing.T) {
	valid := `{"nftables":[{"set":{"family":"inet","table":"bombecam","name":"verification_canary","type":"ether_addr","flags":["timeout"],"elem":[{"elem":{"val":"02:00:00:00:00:02","timeout":30,"expires":29}}]}}]}`
	if err := validateCanaryLease([]byte(valid), "02:00:00:00:00:02", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"{}", strings.Replace(valid, `"expires":29`, `"expires":0`, 1), strings.Replace(valid, `"timeout":30`, `"timeout":30000`, 1), strings.Replace(valid, `["timeout"]`, `[]`, 1), strings.Replace(valid, "02:00:00:00:00:02", "02:00:00:00:00:01", 1)} {
		if err := validateCanaryLease([]byte(raw), "02:00:00:00:00:02", 30*time.Second); err == nil {
			t.Fatalf("unsafe readback accepted: %s", raw)
		}
	}
}
