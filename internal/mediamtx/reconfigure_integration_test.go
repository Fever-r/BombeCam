//go:build integration

package mediamtx

import (
	"context"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This job requires a real MediaMTX binary. Missing prerequisites are failures.
func TestManagedReadPolicyReconfiguration(t *testing.T) {
	binary := os.Getenv("BOMBECAM_TEST_MEDIAMTX")
	if binary == "" {
		t.Fatal("set BOMBECAM_TEST_MEDIAMTX to the real executable")
	}
	cfg := testConfig(t)
	cfg.BinaryPath = binary
	cfg.CacheDir = t.TempDir()
	cfg.RTSPPort = freePort(t)
	cfg.HTTPPort = freePort(t)
	cfg.WebRTCPort = freePort(t)
	cfg.APIPort = freePort(t)
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.WebRTCICEPort = packet.LocalAddr().(*net.UDPAddr).Port
	packet.Close()
	cfg.LogDir = t.TempDir()
	sup := NewSupervisor(cfg)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sup.Stop(); err != nil {
			t.Error(err)
		}
	}()
	if err := sup.VerifyReadPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sup.Reconfigure(context.Background(), func(c *Config) { c.ReadUser = "synthetic"; c.ReadPass = "synthetic-password" }); err != nil {
		t.Fatal(err)
	}
	if err := sup.VerifyReadPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	protected := sup.Config()
	t.Run("blocked replacement port rolls back", func(t *testing.T) {
		hold, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		defer hold.Close()
		err = sup.Reconfigure(context.Background(), func(c *Config) { c.HTTPPort = hold.Addr().(*net.TCPAddr).Port; c.ReadUser = ""; c.ReadPass = "" })
		if err == nil || !strings.Contains(err.Error(), "previous media configuration restored") {
			t.Fatalf("missing truthful rollback error: %v", err)
		}
		if !reflect.DeepEqual(protected, sup.Config()) {
			t.Fatal("failed change altered effective settings")
		}
		if err := sup.VerifyReadPolicy(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("cancelled request still restores policy", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := sup.Reconfigure(ctx, func(c *Config) { c.ReadUser = ""; c.ReadPass = "" })
		if err == nil {
			t.Fatal("cancelled change succeeded")
		}
		if !reflect.DeepEqual(protected, sup.Config()) {
			t.Fatal("cancellation lost the protected policy")
		}
		if err := sup.VerifyReadPolicy(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing taskkill uses owned process handle", func(t *testing.T) {
		if os.PathSeparator == '\\' {
			t.Setenv("PATH", t.TempDir())
		}
		pid := sup.GetPID()
		started := time.Now()
		if err := sup.Stop(); err != nil {
			t.Fatal(err)
		}
		if time.Since(started) > 8*time.Second {
			t.Fatal("stop was not bounded")
		}
		if sup.GetPID() != 0 || sup.IsManaged() || CheckPortListening(cfg.RTSPPort, 200*time.Millisecond) {
			t.Fatalf("owned process %d or its listener remained", pid)
		}
	})
}
