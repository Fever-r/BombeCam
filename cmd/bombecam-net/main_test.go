package main

import (
	"os"
	"testing"
)

func TestGetEnvOrDefault(t *testing.T) {
	key := "TEST_BOMBECAM_VAR_XYZ"
	os.Unsetenv(key)
	if val := getEnvOrDefault(key, "fallback"); val != "fallback" {
		t.Fatalf("expected fallback, got %q", val)
	}

	os.Setenv(key, "custom_val")
	defer os.Unsetenv(key)
	if val := getEnvOrDefault(key, "fallback"); val != "custom_val" {
		t.Fatalf("expected custom_val, got %q", val)
	}
}

func TestResolveNetworkParams(t *testing.T) {
	camIf := "eth_test"
	camSubnet := "10.99.0.0/24"
	gwIP := "10.99.0.1"
	wanIf := "wan_test"

	resolveNetworkParams(&camIf, &camSubnet, &gwIP, &wanIf)

	if camIf != "eth_test" || camSubnet != "10.99.0.0/24" || gwIP != "10.99.0.1" || wanIf != "wan_test" {
		t.Fatalf("explicit params were modified: if=%s, subnet=%s, gw=%s, wan=%s", camIf, camSubnet, gwIP, wanIf)
	}
}

func TestResolveNetworkParamsAutoDiscovery(t *testing.T) {
	var camIf, camSubnet, gwIP, wanIf string
	resolveNetworkParams(&camIf, &camSubnet, &gwIP, &wanIf)
	// On hosts with network interfaces, some or all may be discovered.
	// The important thing is it does not crash or inject hardcoded 192.168.30.
	if camSubnet == "192.168.30.0/24" || gwIP == "192.168.30.1" || camIf == "eth0.30" {
		t.Fatalf("hardcoded network parameters detected: if=%s, subnet=%s, gw=%s", camIf, camSubnet, gwIP)
	}
}

func TestGetEnvIntOrDefault(t *testing.T) {
	key := "TEST_BOMBECAM_INT_VAR_XYZ"
	os.Unsetenv(key)
	if val := getEnvIntOrDefault(key, 8653); val != 8653 {
		t.Fatalf("expected default 8653, got %d", val)
	}

	os.Setenv(key, "9999")
	if val := getEnvIntOrDefault(key, 8653); val != 9999 {
		t.Fatalf("expected custom 9999, got %d", val)
	}

	os.Setenv(key, "not_an_int")
	if val := getEnvIntOrDefault(key, 8653); val != 8653 {
		t.Fatalf("expected fallback 8653 on invalid int, got %d", val)
	}
	os.Unsetenv(key)
}

func TestAPIHostAndPortDefaults(t *testing.T) {
	os.Unsetenv("BOMBECAM_NET_API_HOST")
	os.Unsetenv("BOMBECAM_NET_API_PORT")

	defaultHost := getEnvOrDefault("BOMBECAM_NET_API_HOST", "0.0.0.0")
	if defaultHost != "0.0.0.0" {
		t.Fatalf("expected default host '0.0.0.0', got %q", defaultHost)
	}

	defaultPort := getEnvIntOrDefault("BOMBECAM_NET_API_PORT", 8653)
	if defaultPort != 8653 {
		t.Fatalf("expected default port 8653, got %d", defaultPort)
	}

	os.Setenv("BOMBECAM_NET_API_HOST", "192.168.1.1")
	os.Setenv("BOMBECAM_NET_API_PORT", "9653")
	defer func() {
		os.Unsetenv("BOMBECAM_NET_API_HOST")
		os.Unsetenv("BOMBECAM_NET_API_PORT")
	}()

	customHost := getEnvOrDefault("BOMBECAM_NET_API_HOST", "0.0.0.0")
	if customHost != "192.168.1.1" {
		t.Fatalf("expected custom host '192.168.1.1', got %q", customHost)
	}

	customPort := getEnvIntOrDefault("BOMBECAM_NET_API_PORT", 8653)
	if customPort != 9653 {
		t.Fatalf("expected custom port 9653, got %d", customPort)
	}
}

func TestParseYesNo(t *testing.T) {
	for in, want := range map[string]bool{"yes": true, "Y": true, "on": true, "1": true, "no": false, "OFF": false, "0": false} {
		got, err := parseYesNo(in)
		if err != nil || got != want {
			t.Errorf("parseYesNo(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := parseYesNo("maybe"); err == nil {
		t.Error("maybe accepted")
	}
}

func TestCameraEntries(t *testing.T) {
	os.Setenv("CAMERA_MAC", "Test Camera=aa:bb:cc:dd:ee:01, aa:bb:cc:dd:ee:02")
	defer os.Unsetenv("CAMERA_MAC")
	got := cameraEntries(nil)
	if len(got) != 2 || got[0] != "Test Camera=aa:bb:cc:dd:ee:01" {
		t.Fatalf("got %v", got)
	}
	if got := cameraEntries([]string{"x=aa:bb:cc:dd:ee:03"}); len(got) != 1 {
		t.Fatalf("flags must win over env: %v", got)
	}
}
