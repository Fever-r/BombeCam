package bridge

import (
	"net"
	"testing"
)

func TestViewer_AllowedSubnetsAndVerification(t *testing.T) {
	v := &Viewer{}

	// Initially with no allowed subnets configured, valid RFC 1918 private IPs are accepted
	if !v.VerifyCandidateIP(net.ParseIP("192.168.1.100")) {
		t.Errorf("expected 192.168.1.100 to be accepted by default")
	}
	if !v.VerifyCandidateIP(net.ParseIP("10.50.0.25")) {
		t.Errorf("expected 10.50.0.25 to be accepted by default")
	}
	// Public WAN IP should be rejected
	if v.VerifyCandidateIP(net.ParseIP("8.8.8.8")) {
		t.Errorf("expected 8.8.8.8 to be rejected")
	}

	// Now configure an explicit subnet (e.g. 10.50.0.0/24)
	err := v.SetAllowedSubnet("10.50.0.0/24")
	if err != nil {
		t.Fatalf("SetAllowedSubnet failed: %v", err)
	}

	// 10.50.0.15 should be accepted
	if !v.VerifyCandidateIP(net.ParseIP("10.50.0.15")) {
		t.Errorf("expected 10.50.0.15 to be accepted in 10.50.0.0/24")
	}
	// 192.168.30.50 must now be REJECTED because it's not in 10.50.0.0/24
	if v.VerifyCandidateIP(net.ParseIP("192.168.30.50")) {
		t.Errorf("expected 192.168.30.50 to be rejected when subnet is 10.50.0.0/24")
	}
	// 192.168.1.1 must also be rejected
	if v.VerifyCandidateIP(net.ParseIP("192.168.1.1")) {
		t.Errorf("expected 192.168.1.1 to be rejected when subnet is 10.50.0.0/24")
	}

	// Now configure multiple subnets: 172.16.10.0/24 and 192.168.100.0/24
	err = v.SetAllowedSubnets([]string{"172.16.10.0/24", "192.168.100.0/24"})
	if err != nil {
		t.Fatalf("SetAllowedSubnets failed: %v", err)
	}

	if !v.VerifyCandidateIP(net.ParseIP("172.16.10.5")) {
		t.Errorf("expected 172.16.10.5 to be accepted")
	}
	if !v.VerifyCandidateIP(net.ParseIP("192.168.100.200")) {
		t.Errorf("expected 192.168.100.200 to be accepted")
	}
	if v.VerifyCandidateIP(net.ParseIP("10.50.0.15")) {
		t.Errorf("expected 10.50.0.15 to be rejected after reconfiguration")
	}
}

func TestCloud_DynamicTimezone(t *testing.T) {
	c := NewCloud("US", "+1", testServerKey)
	if c.TimezoneName == "" {
		t.Errorf("expected non-empty TimezoneName")
	}

	// Test override
	c.TimezoneName = "America/New_York"
	c.ZoneOffset = -5.0
	if c.TimezoneName != "America/New_York" || c.ZoneOffset != -5.0 {
		t.Errorf("failed to override timezone")
	}
}
