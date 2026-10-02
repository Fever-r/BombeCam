package main

import (
	"os"
	"strings"
	"testing"
)

func TestGetEnvOrDefault(t *testing.T) {
	key := "TEST_POLICY_ENV_VAR"
	os.Unsetenv(key)
	if val := getEnvOrDefault(key, "fallback_policy"); val != "fallback_policy" {
		t.Fatalf("expected fallback_policy, got %q", val)
	}

	os.Setenv(key, "custom_policy")
	defer os.Unsetenv(key)
	if val := getEnvOrDefault(key, "fallback_policy"); val != "custom_policy" {
		t.Fatalf("expected custom_policy, got %q", val)
	}
}

func TestRunTargets(t *testing.T) {
	var out, errb strings.Builder
	if code := run([]string{"-block", "yes", "-camera", "Test Camera=AA:BB:CC:DD:EE:01", "-target", "json"}, &out, &errb); code != 0 {
		t.Fatalf("json: %d %s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"host": "mqtts02-us.osaio.net"`) {
		t.Fatalf("json output: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"-block", "yes", "-camera", "Test Camera=AA:BB:CC:DD:EE:01", "-target", "openwrt-command"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	want := "sh /tmp/bombecam-router.sh apply yes --camera 'Test Camera=aa:bb:cc:dd:ee:01' --allow-stream-setup"
	if strings.TrimSpace(out.String()) != want {
		t.Fatalf("command = %q", out.String())
	}
	out.Reset()
	if code := run([]string{"-block", "no", "-camera", "aa:bb:cc:dd:ee:01", "-target", "nftables"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	if strings.Contains(out.String(), "chain") {
		t.Fatal("No must render no chains")
	}
	out.Reset()
	if code := run([]string{"-target", "openwrt-script"}, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "#!/bin/sh") {
		t.Fatal("script target failed")
	}
	if code := run([]string{"-block", "maybe", "-camera", "aa:bb:cc:dd:ee:01"}, &out, &errb); code == 0 {
		t.Fatal("bad -block accepted")
	}
	if code := run([]string{"-block", "yes", "-camera", "x=zz"}, &out, &errb); code == 0 {
		t.Fatal("bad MAC accepted")
	}
}
