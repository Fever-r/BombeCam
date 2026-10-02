package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Fever-r/BombeCam/pkg/auth"
	"github.com/Fever-r/BombeCam/pkg/bridge"
	shadowshim "github.com/Fever-r/BombeCam/pkg/bridge/shadow_shim"
)

// TestIntegrations_EmbeddedUI_StaticAssetServing verifies:
// 1. Root / serves index.html with HTTP 200 and text/html.
// 2. /index.html serves index.html with HTTP 200.
// 3. /style.css serves CSS with HTTP 200 and text/css.
// 4. /app.js serves JavaScript with HTTP 200 and application/javascript.
// 5. Non-existent path returns HTTP 404.
func TestIntegrations_EmbeddedUI_StaticAssetServing(t *testing.T) {
	_, _, _, _, mux, _ := setupFullTestEnvironment(t)

	tests := []struct {
		path         string
		expectedCode int
		expectedType string
		contains     string
	}{
		{"/", http.StatusOK, "text/html", "<title>BombeCam</title>"},
		{"/index.html", http.StatusOK, "text/html", "<title>BombeCam</title>"},
		{"/style.css", http.StatusOK, "text/css", "--bg-body"},
		{"/app.js", http.StatusOK, "javascript", "fetchCSRFToken"},
		{"/hls.min.js", http.StatusOK, "javascript", "Hls"},
		{"/missing_file.xyz", http.StatusNotFound, "text/plain", "404 page not found"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != tt.expectedCode {
			t.Errorf("path %s: expected status %d, got %d", tt.path, tt.expectedCode, rec.Code)
		}
		contentType := rec.Header().Get("Content-Type")
		if !strings.Contains(contentType, tt.expectedType) {
			t.Errorf("path %s: expected Content-Type containing %q, got %q", tt.path, tt.expectedType, contentType)
		}
		body := rec.Body.String()
		if !strings.Contains(body, tt.contains) {
			t.Errorf("path %s: expected body containing %q", tt.path, tt.contains)
		}
	}
}

// TestIntegrations_CameraShadowEndpoint verifies:
// 1. GET /api/v1/cameras/{id}/shadow returns 404 for unknown camera.
// 2. GET /api/v1/cameras/{id}/shadow returns authentic shadow state with mock channel.
// 3. GET /api/v1/cameras/{id}/shadow returns authentic shadow document with MQTTControlChannel.
func TestIntegrations_CameraShadowEndpoint(t *testing.T) {
	_, streamMgr, _, _, mux, _ := setupFullTestEnvironment(t)

	// 1. Unknown camera returns 404
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cameras/nonexistent-cam/shadow", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown camera, got %d", rec.Code)
	}

	// Enroll a test camera
	camID := "cam-int-001"
	devs := []bridge.Device{
		{UUID: camID, Name: "Living Room", Type: "WS03", Online: 1},
	}
	if _, err := streamMgr.Enroll([]string{camID}, devs); err != nil {
		t.Fatalf("failed to enroll camera: %v", err)
	}

	mc, ok := streamMgr.GetCamera(camID)
	if !ok {
		t.Fatalf("expected camera %s to exist in StreamManager", camID)
	}
	mc.ObservedIR = "off"
	mc.ObservedLED = "on"

	// 2. Query with mock control channel
	mockCC := &mockControlChannel{irMode: 1, ledOn: true}
	SetGatewayControlChannel(mockCC)
	defer SetGatewayControlChannel(nil)

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/cameras/"+camID+"/shadow", nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for shadow endpoint, got %d. Body: %s", rec2.Code, rec2.Body.String())
	}
	var shadowResp map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &shadowResp); err != nil {
		t.Fatalf("failed to decode shadow response: %v", err)
	}
	stateMap, ok := shadowResp["state"].(map[string]any)
	if !ok {
		t.Fatalf("missing state in shadow response: %+v", shadowResp)
	}
	reported, ok := stateMap["reported"].(map[string]any)
	if !ok {
		t.Fatalf("missing reported in shadow response: %+v", stateMap)
	}
	if irVal, ok := reported["IrLedMode"].(float64); !ok || int(irVal) != 1 {
		t.Errorf("expected IrLedMode=1, got %v", reported["IrLedMode"])
	}
	if ledVal, ok := reported["LedOnOff"].(float64); !ok || int(ledVal) != 1 {
		t.Errorf("expected LedOnOff=1, got %v", reported["LedOnOff"])
	}

	// 3. Query with MQTTControlChannel and ShadowShimDaemon
	daemon := shadowshim.NewDaemon(shadowshim.Config{DefaultTimeout: 2 * time.Second})
	if err := daemon.Start(context.Background()); err != nil {
		t.Fatalf("failed to start shadow daemon: %v", err)
	}
	defer daemon.Close()

	// Ingest reported state into daemon
	_ = daemon.IngestReported(camID, map[string]any{
		"IrLedMode": 2, // on
		"LedOnOff":  0, // off
		"LightSW":   1, // floodlight on
	})

	mqttCC := bridge.NewMQTTControlChannel(daemon, 2*time.Second)
	SetGatewayControlChannel(mqttCC)

	req3 := httptest.NewRequest(http.MethodGet, "/api/v1/cameras/"+camID+"/shadow", nil)
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from MQTT shadow daemon, got %d. Body: %s", rec3.Code, rec3.Body.String())
	}
	var daemonDoc shadowshim.ShadowDocument
	if err := json.Unmarshal(rec3.Body.Bytes(), &daemonDoc); err != nil {
		t.Fatalf("failed to parse ShadowDocument: %v", err)
	}
	if daemonDoc.State.Reported == nil {
		t.Fatalf("reported state is nil in ShadowDocument: %+v", daemonDoc)
	}
	if irVal, ok := daemonDoc.State.Reported["IrLedMode"].(float64); !ok || int(irVal) != 2 {
		t.Errorf("expected IrLedMode=2 from shadow daemon, got %v", daemonDoc.State.Reported["IrLedMode"])
	}
	if lightVal, ok := daemonDoc.State.Reported["LightSW"].(float64); !ok || int(lightVal) != 1 {
		t.Errorf("expected LightSW=1 from shadow daemon, got %v", daemonDoc.State.Reported["LightSW"])
	}
}

// TestIntegrations_SecurityBoundary_ExpiredCookieAndCSRFBypass verifies:
// 1. A client with an expired/invalid session cookie can still access / and /api/v1/auth/csrf without 401.
// 2. /api/v1/auth/csrf sets a fresh session cookie and returns a valid CSRF token.
// 3. State-mutating POST with valid cookie and X-CSRF-Token succeeds.
// 4. State-mutating POST without X-CSRF-Token is rejected with 403 invalid_csrf_token.
func TestIntegrations_SecurityBoundary_ExpiredCookieAndCSRFBypass(t *testing.T) {
	tmpDir := t.TempDir()
	opMgr := auth.NewOperatorManager(auth.OperatorConfig{
		KeyFile: tmpDir + "/api_tokens.json",
	})
	SetGatewayOperatorManager(opMgr)
	defer SetGatewayOperatorManager(nil)

	_, _, _, _, mux, _ := setupFullTestEnvironment(t)
	handler := SecurityBoundaryHandler(mux)

	// Step 1: Request / with an invalid/expired session cookie
	reqStatic := httptest.NewRequest(http.MethodGet, "/", nil)
	reqStatic.RemoteAddr = "127.0.0.1:50000"
	reqStatic.Host = "127.0.0.1:8654"
	reqStatic.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: "expired_or_invalid_session_token_12345",
	})
	recStatic := httptest.NewRecorder()
	handler.ServeHTTP(recStatic, reqStatic)

	if recStatic.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on GET / even with expired cookie, got %d. Body: %s", recStatic.Code, recStatic.Body.String())
	}

	// Step 1b: Request /hls.min.js with an invalid/expired session cookie
	reqHLS := httptest.NewRequest(http.MethodGet, "/hls.min.js", nil)
	reqHLS.RemoteAddr = "127.0.0.1:50000"
	reqHLS.Host = "127.0.0.1:8654"
	reqHLS.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: "expired_or_invalid_session_token_12345",
	})
	recHLS := httptest.NewRecorder()
	handler.ServeHTTP(recHLS, reqHLS)

	if recHLS.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on GET /hls.min.js with expired cookie, got %d. Body: %s", recHLS.Code, recHLS.Body.String())
	}
	if !strings.Contains(recHLS.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("expected javascript Content-Type for /hls.min.js, got %s", recHLS.Header().Get("Content-Type"))
	}

	// Step 1c: HEAD request for /hls.min.js with an invalid/expired session cookie
	reqHLSHead := httptest.NewRequest(http.MethodHead, "/hls.min.js", nil)
	reqHLSHead.RemoteAddr = "127.0.0.1:50000"
	reqHLSHead.Host = "127.0.0.1:8654"
	reqHLSHead.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: "expired_or_invalid_session_token_12345",
	})
	recHLSHead := httptest.NewRecorder()
	handler.ServeHTTP(recHLSHead, reqHLSHead)

	if recHLSHead.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on HEAD /hls.min.js with expired cookie, got %d. Body: %s", recHLSHead.Code, recHLSHead.Body.String())
	}

	// Step 2: Request CSRF token with an invalid/expired session cookie
	reqCSRF := httptest.NewRequest(http.MethodGet, "/api/v1/auth/csrf", nil)
	reqCSRF.RemoteAddr = "127.0.0.1:50000"
	reqCSRF.Host = "127.0.0.1:8654"
	reqCSRF.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: "expired_or_invalid_session_token_12345",
	})
	recCSRF := httptest.NewRecorder()
	handler.ServeHTTP(recCSRF, reqCSRF)

	if recCSRF.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on GET /api/v1/auth/csrf with expired cookie, got %d. Body: %s", recCSRF.Code, recCSRF.Body.String())
	}
	var csrfData map[string]any
	if err := json.Unmarshal(recCSRF.Body.Bytes(), &csrfData); err != nil {
		t.Fatalf("failed to decode CSRF response: %v", err)
	}
	csrfToken, ok := csrfData["csrf_token"].(string)
	if !ok || csrfToken == "" {
		t.Fatalf("missing csrf_token in response: %+v", csrfData)
	}

	// Extract new session cookie from Set-Cookie header
	cookies := recCSRF.Result().Cookies()
	var newSessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName {
			newSessionCookie = c
			break
		}
	}
	if newSessionCookie == nil {
		t.Fatalf("expected Set-Cookie with bombecam_session, got none")
	}

	// Step 3: State-mutating request without CSRF token must fail (403)
	reqMutateNoCSRF := httptest.NewRequest(http.MethodPost, "/api/v1/onboarding/reset", strings.NewReader("{}"))
	reqMutateNoCSRF.RemoteAddr = "127.0.0.1:50000"
	reqMutateNoCSRF.Host = "127.0.0.1:8654"
	reqMutateNoCSRF.AddCookie(newSessionCookie)
	recMutateNoCSRF := httptest.NewRecorder()
	handler.ServeHTTP(recMutateNoCSRF, reqMutateNoCSRF)

	if recMutateNoCSRF.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden without CSRF token, got %d. Body: %s", recMutateNoCSRF.Code, recMutateNoCSRF.Body.String())
	}

	// Step 4: State-mutating request with valid cookie and X-CSRF-Token must succeed (200)
	reqMutateWithCSRF := httptest.NewRequest(http.MethodPost, "/api/v1/onboarding/reset", strings.NewReader("{}"))
	reqMutateWithCSRF.RemoteAddr = "127.0.0.1:50000"
	reqMutateWithCSRF.Host = "127.0.0.1:8654"
	reqMutateWithCSRF.AddCookie(newSessionCookie)
	reqMutateWithCSRF.Header.Set(auth.CSRFHeaderName, csrfToken)
	recMutateWithCSRF := httptest.NewRecorder()
	handler.ServeHTTP(recMutateWithCSRF, reqMutateWithCSRF)

	if recMutateWithCSRF.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on reset with valid CSRF token, got %d. Body: %s", recMutateWithCSRF.Code, recMutateWithCSRF.Body.String())
	}
}

// TestIntegrations_AuxiliaryControls_PTZAndSwitches verifies:
// 1. PTZ pulse command sends direction and duration.
// 2. IR mode and LED switch commands dispatch through control channel.
func TestIntegrations_AuxiliaryControls_PTZAndSwitches(t *testing.T) {
	_, streamMgr, _, _, mux, _ := setupFullTestEnvironment(t)

	camID := "cam-aux-001"
	devs := []bridge.Device{
		{UUID: camID, Name: "Driveway", Type: "WS03", Online: 1},
	}
	if _, err := streamMgr.Enroll([]string{camID}, devs); err != nil {
		t.Fatalf("failed to enroll camera: %v", err)
	}

	mockCC := &mockControlChannel{irMode: 0, ledOn: false}
	SetGatewayControlChannel(mockCC)
	defer SetGatewayControlChannel(nil)

	// 1. Test dedicated PTZ route: POST /api/v1/cameras/{id}/ptz
	ptzPayload, _ := json.Marshal(map[string]any{
		"direction":   1, // Left
		"duration_ms": 300,
	})
	reqPTZ := httptest.NewRequest(http.MethodPost, "/api/v1/cameras/"+camID+"/ptz", bytes.NewReader(ptzPayload))
	recPTZ := httptest.NewRecorder()
	mux.ServeHTTP(recPTZ, reqPTZ)

	if recPTZ.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on dedicated PTZ route, got %d. Body: %s", recPTZ.Code, recPTZ.Body.String())
	}
	if mockCC.ptzDir != 1 {
		t.Errorf("expected PTZ direction=1, got %d", mockCC.ptzDir)
	}
	if mockCC.ptzDur != 300 {
		t.Errorf("expected PTZ duration=300, got %d", mockCC.ptzDur)
	}

	// 2. Test IR control: POST /api/v1/cameras/{id}/control
	irPayload, _ := json.Marshal(map[string]any{
		"action": "ir",
		"mode":   "auto",
	})
	reqIR := httptest.NewRequest(http.MethodPost, "/api/v1/cameras/"+camID+"/control", bytes.NewReader(irPayload))
	recIR := httptest.NewRecorder()
	mux.ServeHTTP(recIR, reqIR)

	if recIR.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on IR control, got %d. Body: %s", recIR.Code, recIR.Body.String())
	}
	if mockCC.irMode != bridge.IRModeAuto {
		t.Errorf("expected irMode=%d (auto), got %d", bridge.IRModeAuto, mockCC.irMode)
	}

	// 3. Test LED switch control: POST /api/v1/cameras/{id}/control
	ledPayload, _ := json.Marshal(map[string]any{
		"action": "led",
		"mode":   "on",
	})
	reqLED := httptest.NewRequest(http.MethodPost, "/api/v1/cameras/"+camID+"/control", bytes.NewReader(ledPayload))
	recLED := httptest.NewRecorder()
	mux.ServeHTTP(recLED, reqLED)

	if recLED.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on LED control, got %d. Body: %s", recLED.Code, recLED.Body.String())
	}
	if !mockCC.ledOn {
		t.Errorf("expected ledOn=true, got %v", mockCC.ledOn)
	}
}

// TestIntegrations_StopLeavesRouterAlone verifies POST /api/v1/gateway/stop
// halts streams and reports that the router's rules were not touched, and
// that the retired firewall endpoints are gone.
func TestIntegrations_StopLeavesRouterAlone(t *testing.T) {
	_, _, _, _, mux, _ := setupFullTestEnvironment(t)

	reqStop := httptest.NewRequest(http.MethodPost, "/api/v1/gateway/stop", nil)
	recStop := httptest.NewRecorder()
	mux.ServeHTTP(recStop, reqStop)
	if recStop.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on gateway stop, got %d. Body: %s", recStop.Code, recStop.Body.String())
	}
	var stopResp map[string]any
	if err := json.Unmarshal(recStop.Body.Bytes(), &stopResp); err != nil {
		t.Fatalf("failed to decode stop response: %v", err)
	}
	if stopResp["router_unchanged"] != true {
		t.Errorf("expected router_unchanged=true on stop, got %v", stopResp["router_unchanged"])
	}

	for _, path := range []string{"/api/v1/firewall/restore-internet", "/api/v1/firewall/mode", "/api/v1/onboarding/mode"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"confirm": true, "privacy_mode": true}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("retired endpoint %s still answers 200", path)
		}
	}
}

// TestIntegrations_StreamingAndPrivacyStatus verifies streams and
// integrations work, and the Block cloud video status lists the camera.
func TestIntegrations_StreamingAndPrivacyStatus(t *testing.T) {
	_, streamMgr, _, _, mux, _ := setupFullTestEnvironment(t)

	camID := "cam-stream-001"
	devs := []bridge.Device{
		{UUID: camID, Name: "Patio", Type: "WS03", Online: 1},
	}
	if _, err := streamMgr.Enroll([]string{camID}, devs); err != nil {
		t.Fatalf("failed to enroll camera: %v", err)
	}

	// Stream descriptor
	reqStream := httptest.NewRequest(http.MethodGet, "/api/v1/cameras/"+camID+"/stream", nil)
	recStream := httptest.NewRecorder()
	mux.ServeHTTP(recStream, reqStream)
	if recStream.Code != http.StatusOK {
		t.Errorf("expected 200 for streaming descriptor, got %d", recStream.Code)
	}

	// Frigate integration
	reqFrigate := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/frigate", nil)
	recFrigate := httptest.NewRecorder()
	mux.ServeHTTP(recFrigate, reqFrigate)
	if recFrigate.Code != http.StatusOK {
		t.Errorf("expected 200 for Frigate integration, got %d", recFrigate.Code)
	}

	// Home Assistant integration
	reqHA := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/homeassistant", nil)
	recHA := httptest.NewRecorder()
	mux.ServeHTTP(recHA, reqHA)
	if recHA.Code != http.StatusOK {
		t.Errorf("expected 200 for Home Assistant integration, got %d", recHA.Code)
	}

	reqPriv := httptest.NewRequest(http.MethodGet, "/api/v1/privacy", nil)
	recPriv := httptest.NewRecorder()
	mux.ServeHTTP(recPriv, reqPriv)
	if recPriv.Code != http.StatusOK {
		t.Fatalf("expected 200 for privacy status, got %d", recPriv.Code)
	}
	var priv map[string]any
	_ = json.Unmarshal(recPriv.Body.Bytes(), &priv)
	if priv["cameras_connected"] != float64(1) || priv["block_cloud_video"] != nil {
		t.Errorf("unexpected privacy status: %+v", priv)
	}
}
