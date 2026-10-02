package main

import (
	"encoding/json"
	"github.com/Fever-r/BombeCam/pkg/bridge"
	"net/http"
	"sync/atomic"
)

var localControlTestActive atomic.Bool

func isLocalControl(cc bridge.ControlChannel) bool {
	_, ok := cc.(*bridge.MQTTControlChannel)
	return ok
}

func handleControlStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cc := GatewayControlChannel()
	w.Header().Set("Content-Type", "application/json")
	if local, ok := cc.(*bridge.MQTTControlChannel); ok {
		_ = json.NewEncoder(w).Encode(map[string]any{"test_mode": localControlTestActive.Load(), "control": local.LocalStatus(r.URL.Query().Get("camera_id")), "firewall_verified": false})
		return
	}
	route := "cloud"
	if cc == nil {
		route = "unavailable"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"test_mode": false, "control": map[string]any{"route": route}})
}
