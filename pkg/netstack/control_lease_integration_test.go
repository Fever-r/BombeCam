//go:build integration

package netstack

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Run this binary inside unshare --net, never on the host/router namespace.
func TestKernelCanaryLeaseExpiresWithNoRestorationProcess(t *testing.T) {
	self, errSelf := os.Readlink("/proc/self/ns/net")
	init, errInit := os.Readlink("/proc/1/ns/net")
	if errSelf != nil || errInit != nil || self == init {
		t.Fatal("refusing to modify the host network namespace; use unshare --net")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("Linux isolated network namespace required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	m, err := NewRulesetManager(RulesetConfig{Cameras: []string{"02:00:00:00:00:01", "02:00:00:00:00:02"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyTemporaryControl(ctx, "02:00:00:00:00:02", "camtest", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	// Do not call ApplyContext or run a restoration timer. The kernel must expire it.
	deadline := time.Now().Add(6 * time.Second)
	for {
		out, err := exec.CommandContext(ctx, "nft", "-j", "list", "set", "inet", "bombecam", "verification_canary").CombinedOutput()
		if err != nil {
			t.Fatalf("set read: %v %s", err, out)
		}
		if !strings.Contains(string(out), `"val": "02:00:00:00:00:02"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("canary did not expire: %s", out)
		}
		time.Sleep(100 * time.Millisecond)
	}
	out, err := exec.CommandContext(ctx, "nft", "list", "table", "inet", "bombecam").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "ether saddr @cameras jump camera_out") || !strings.Contains(string(out), "counter packets") || !strings.Contains(string(out), "drop") {
		t.Fatalf("blocking policy missing after expiry: %s", out)
	}
	if err := m.ApplyContext(ctx, true); err != nil {
		t.Fatal(err)
	}
	out, err = exec.CommandContext(ctx, "nft", "list", "table", "inet", "bombecam").CombinedOutput()
	if err != nil || strings.Contains(string(out), "verification_canary") {
		t.Fatalf("restore retained bypass: %v %s", err, out)
	}
}
