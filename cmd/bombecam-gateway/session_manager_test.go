package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSessionManager_MissingCredentials(t *testing.T) {
	sm := NewSessionManager("39", "test-phone", testServerKey)
	err := sm.Login("", "")
	if err == nil {
		t.Fatal("expected error for empty credentials, got nil")
	}

	status := sm.GetStatus()
	if status.Status != SessionStatusCredentialsMissing {
		t.Fatalf("expected status %s, got %s", SessionStatusCredentialsMissing, status.Status)
	}
	if status.Error == "" {
		t.Fatal("expected non-empty error message")
	}
	if status.VendorUID != "" {
		t.Fatalf("expected empty vendor UID, got %s", status.VendorUID)
	}
}

func TestSessionManager_MockLoginSuccess(t *testing.T) {
	var serverURL string
	// Mock OSAIO cloud server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/account/get-baseurl":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 1000,
				"data": map[string]any{
					"region": "EU",
					"web":    serverURL,
					"ws":     "wss://mock.osaio.net/ws",
				},
			})
		case "/v2/login/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 1000,
				"data": map[string]any{
					"uid":       "mock-uid-12345",
					"api_token": "mock-token-xyz",
				},
			})
		case "/v2/device/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 1000,
				"data": map[string]any{
					"data": []map[string]any{
						{
							"uuid":       "mock-uuid-001",
							"name":       "Test Cam 1",
							"type":       "WS03",
							"model_type": 1,
							"online":     1,
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	sm := NewSessionManager("39", "test-phone", testServerKey)
	sm.Cloud().Web = server.URL
	sm.Cloud().GlobalBase = server.URL

	err := sm.Login("user@example.com", "valid_password")
	if err != nil {
		t.Fatalf("unexpected login error: %v", err)
	}

	if !sm.IsAuthenticated() {
		t.Fatal("expected IsAuthenticated() to be true")
	}

	status := sm.GetStatus()
	if status.Status != SessionStatusAuthenticated {
		t.Fatalf("expected status %s, got %s", SessionStatusAuthenticated, status.Status)
	}
	if status.VendorUID != "mock-uid-12345" {
		t.Fatalf("expected UID mock-uid-12345, got %s", status.VendorUID)
	}
	if status.LastLoginTS == "" {
		t.Fatal("expected last_login_ts to be populated")
	}

	// Test DeviceList caching
	devs, ts, err := sm.DeviceList(false)
	if err != nil {
		t.Fatalf("DeviceList failed: %v", err)
	}
	if len(devs) != 1 || devs[0].UUID != "mock-uuid-001" {
		t.Fatalf("expected 1 camera mock-uuid-001, got %+v", devs)
	}
	if ts.IsZero() {
		t.Fatal("expected discovery timestamp")
	}

	cached := sm.GetCachedDevices()
	if len(cached) != 1 {
		t.Fatalf("expected 1 cached device, got %d", len(cached))
	}
}

func TestSessionManager_MockLoginFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v2/login/login" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 1006,
				"msg":  "account or password incorrect",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 1000, "data": map[string]any{}})
	}))
	defer server.Close()

	sm := NewSessionManager("39", "test-phone", testServerKey)
	sm.Cloud().Web = server.URL
	sm.Cloud().GlobalBase = server.URL

	err := sm.Login("user@example.com", "wrong_password")
	if err == nil {
		t.Fatal("expected login error for wrong password, got nil")
	}

	status := sm.GetStatus()
	if status.Status != SessionStatusInvalidCredentials {
		t.Fatalf("expected status %s, got %s", SessionStatusInvalidCredentials, status.Status)
	}
	if status.Error == "" {
		t.Fatal("expected error description in status")
	}
}

func TestSessionManager_DeviceListUnauthenticated(t *testing.T) {
	sm := NewSessionManager("39", "test-phone", testServerKey)
	_, _, err := sm.DeviceList(false)
	if err == nil {
		t.Fatal("expected unauthenticated error from DeviceList, got nil")
	}
}
