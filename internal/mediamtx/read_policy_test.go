package mediamtx

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestVerifyReadPolicy(t *testing.T) {
	tests := []struct {
		name, method, users string
		status              int
		closed, ok          bool
	}{
		{"password", "internal", `[{"user":"any","pass":"","ips":["127.0.0.1","::1"],"permissions":[{"action":"read"}]},{"user":"synthetic","pass":%q,"ips":[],"permissions":[{"action":"read"}]}]`, 200, true, true},
		{"public", "internal", `[{"user":"any","pass":"","ips":[],"permissions":[{"action":"read"}]}]`, 200, false, true},
		{"password missing", "internal", `[{"user":"any","pass":"","ips":[],"permissions":[{"action":"read"}]}]`, 200, true, false},
		{"alternate public reader", "internal", `[{"user":"synthetic","pass":%q,"ips":[],"permissions":[{"action":"read"}]},{"user":"any","pass":"","ips":[],"permissions":[{"action":"playback"}]}]`, 200, true, false},
		{"nonloopback bypass", "internal", `[{"user":"any","pass":"","ips":["192.0.2.10"],"permissions":[{"action":"read"}]}]`, 200, true, false},
		{"external auth", "http", `[]`, 200, true, false},
		{"http application error", "internal", `[]`, 503, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users := tt.users
			if tt.closed && (tt.name == "password" || tt.name == "alternate public reader") {
				users = fmt.Sprintf(users, hashedPass("synthetic-password"))
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tt.status)
				fmt.Fprintf(w, `{"authMethod":%q,"authInternalUsers":%s}`, tt.method, users)
			}))
			defer server.Close()
			cfg := DefaultConfig()
			cfg.APIPort = server.Listener.Addr().(*net.TCPAddr).Port
			if tt.closed {
				cfg.ReadUser = "synthetic"
				cfg.ReadPass = "synthetic-password"
			}
			err := NewSupervisor(cfg).VerifyReadPolicy(context.Background())
			if (err == nil) != tt.ok {
				t.Fatalf("wanted verified=%v, got %v", tt.ok, err)
			}
			if calls != 1 {
				t.Fatalf("genuine HTTP status retried %d times", calls)
			}
		})
	}
}

func TestReconfigureDoesNotMutateCustomConfiguration(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ConfigPath = "custom.yml"
	sup := NewSupervisor(cfg)
	before := sup.Config()
	err := sup.Reconfigure(context.Background(), func(c *Config) { c.ReadUser = "synthetic"; c.ReadPass = "password" })
	if err == nil || !reflect.DeepEqual(before, sup.Config()) {
		t.Fatal("custom configuration mutated or accepted a false change")
	}
}

func TestReconfigureLeavesExternalListenerAndConfigAlone(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cfg := DefaultConfig()
	cfg.RTSPPort = listener.Addr().(*net.TCPAddr).Port
	cfg.APIPort = freePort(t)
	sup := NewSupervisor(cfg)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sup.Stop()
	before := sup.Config()
	if err := sup.Reconfigure(context.Background(), func(c *Config) { c.ReadUser = "synthetic" }); err == nil {
		t.Fatal("external listener accepted reconfiguration")
	}
	if !reflect.DeepEqual(before, sup.Config()) || !CheckPortListening(cfg.RTSPPort, cfg.ProbeTimeout) {
		t.Fatal("external listener or configuration changed")
	}
}
