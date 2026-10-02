package e2e

import (
	"fmt"
	"net"
	"runtime"
	"testing"
	"time"
)

func TestE2E_GatewayCleanupWithoutTaskkill(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()
	if runtime.GOOS == "windows" {
		t.Setenv("PATH", t.TempDir())
	}
	started := time.Now()
	h.KillGateway()
	if time.Since(started) > 8*time.Second {
		t.Fatal("gateway cleanup exceeded its deadline")
	}
	if h.Cmd != nil {
		t.Fatal("gateway exit was not confirmed")
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", h.Port), 200*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("owned gateway listener remained open")
	}
}
