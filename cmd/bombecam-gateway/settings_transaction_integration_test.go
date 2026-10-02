//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Fever-r/BombeCam/internal/mediamtx"
	"github.com/Fever-r/BombeCam/pkg/profile"
)

type failingSettingsStore struct{ profile.ProfileManager }

func (p failingSettingsStore) Update(context.Context, func(*profile.Profile) error) (*profile.Profile, error) {
	return nil, errors.New("synthetic persistence failure")
}

func TestSettingsTransaction_RealMediaRollback(t *testing.T) {
	sm, _, pm := settingsEnv(t)
	oldSup, oldAPI := currentMediaRuntime()
	defer setMediaRuntime(oldSup, oldAPI)
	cfg := mediamtx.DefaultConfig()
	cfg.BinaryPath = os.Getenv("BOMBECAM_TEST_MEDIAMTX")
	if cfg.BinaryPath == "" {
		t.Fatal("set BOMBECAM_TEST_MEDIAMTX to the real executable")
	}
	cfg.CacheDir = t.TempDir()
	cfg.LogDir = t.TempDir()
	free := func() int {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	cfg.RTSPPort = free()
	cfg.HTTPPort = free()
	cfg.WebRTCPort = free()
	cfg.APIPort = free()
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.WebRTCICEPort = udp.LocalAddr().(*net.UDPAddr).Port
	udp.Close()
	sup := mediamtx.NewSupervisor(cfg)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := sup.Stop(); err != nil {
			t.Error(err)
		}
	}()
	setMediaRuntime(sup, sup.APIBase())
	setPortsLocked(false, "")
	baseline := profile.IntegrationSettings{RTSPPort: cfg.RTSPPort, HLSPort: cfg.HTTPPort, WebRTCPort: cfg.WebRTCPort, WebRTCICEPort: cfg.WebRTCICEPort}
	if _, err := pm.Update(context.Background(), func(p *profile.Profile) error { p.Integrations = baseline; return nil }); err != nil {
		t.Fatal(err)
	}
	setStreamManagerPorts(sm, baseline)
	applyIntegrationRuntime(sm, baseline)
	t.Run("storage failure restores public policy", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/integrations/settings", strings.NewReader(`{"stream_auth":true}`))
		response := httptest.NewRecorder()
		handleIntegrationSettingsUpdate(response, req, sm, failingSettingsStore{pm})
		if response.Code != 500 {
			t.Fatalf("want save failure, got %d %s", response.Code, response.Body.String())
		}
		if pm.GetProfile().Integrations.StreamAuth || currentIntegrationSettings().StreamAuth {
			t.Fatal("failed save changed preference or advertised policy")
		}
		if sup.Config().ReadUser != "" {
			t.Fatal("failed save left the new media password active")
		}
		if err := sup.VerifyReadPolicy(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("failed media restart never saves preference", func(t *testing.T) {
		hold, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer hold.Close()
		body, _ := json.Marshal(map[string]any{"stream_auth": true, "ports": integrationPorts{RTSP: cfg.RTSPPort, HLS: hold.Addr().(*net.TCPAddr).Port, WebRTC: cfg.WebRTCPort, WebRTCUDP: cfg.WebRTCICEPort}})
		req := httptest.NewRequest("POST", "/api/v1/integrations/settings", strings.NewReader(string(body)))
		response := httptest.NewRecorder()
		handleIntegrationSettingsUpdate(response, req, sm, pm)
		if response.Code != 503 {
			t.Fatalf("want media failure, got %d %s", response.Code, response.Body.String())
		}
		if pm.GetProfile().Integrations.StreamAuth || currentIntegrationSettings().StreamAuth || sup.Config().ReadUser != "" {
			t.Fatal("failed restart saved or advertised new protection")
		}
		if err := sup.VerifyReadPolicy(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}
