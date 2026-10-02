package netstack

import (
	"strings"
	"testing"
)

func TestNATManager_Configuration(t *testing.T) {
	natMgr := NewNATManager("eth_cam", "10.100.0.0/24", "eth_wan")

	if natMgr.CameraInterface != "eth_cam" {
		t.Errorf("expected eth_cam, got %s", natMgr.CameraInterface)
	}
	if natMgr.CameraSubnet != "10.100.0.0/24" {
		t.Errorf("expected 10.100.0.0/24, got %s", natMgr.CameraSubnet)
	}
	if natMgr.WANInterface != "eth_wan" {
		t.Errorf("expected eth_wan, got %s", natMgr.WANInterface)
	}

	rules := natMgr.RenderNFTNATRules()
	if !strings.Contains(rules, "10.100.0.0/24") {
		t.Errorf("expected rules to contain 10.100.0.0/24, got:\n%s", rules)
	}
	if !strings.Contains(rules, `oifname "eth_wan"`) {
		t.Errorf("expected rules to contain oifname \"eth_wan\", got:\n%s", rules)
	}

	iptablesRules := natMgr.RenderIPTablesNATRules()
	if len(iptablesRules) != 1 || !strings.Contains(iptablesRules[0], "-s 10.100.0.0/24 -o eth_wan") {
		t.Errorf("unexpected iptables rules: %v", iptablesRules)
	}

	err := ValidateCameraSubnet("10.100.0.0/24")
	if err != nil {
		t.Errorf("expected valid CIDR, got error: %v", err)
	}

	err = ValidateCameraSubnet("invalid-cidr")
	if err == nil {
		t.Errorf("expected error for invalid CIDR")
	}
}
