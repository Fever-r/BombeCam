package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

// TestE2E_GatewaySubprocess_SmokeCheck performs an end-to-end black-box
// headless subprocess smoke check confirming onboarding and operator authentication in the compiled gateway binary.
func TestE2E_GatewaySubprocess_SmokeCheck(t *testing.T) {
	// Start headless gateway subprocess
	h := NewGatewayHarness(t)
	defer h.Teardown()

	// =========================================================================
	// 1. Onboarding needs no router; applying with an unreachable router fails cleanly
	// =========================================================================
	t.Run("Subprocess_Onboarding_WithoutRouter", func(t *testing.T) {
		code, setupResp := h.Setup(map[string]any{
			"account_email": "smokeuser@example.com",
			"password":      "SecretPassword123!",
			"country":       "1",
		})
		if code != http.StatusOK {
			t.Fatalf("expected HTTP 200 for onboarding, got %d: %+v", code, setupResp)
		}
		if setupResp["status"] != "configured" {
			t.Errorf("expected status=configured, got %v", setupResp["status"])
		}
		if _, ok := setupResp["privacy_mode"]; ok {
			t.Errorf("retired privacy_mode field still reported")
		}

		// Nothing listens here: the apply must report router_unreachable,
		// not pretend anything was blocked.
		h.MockRouter.Close()
		codeNo, respNo := h.ApplyPrivacyWith(map[string]any{
			"block_cloud_video": false, "router_address": h.MockRouter.Addr, "router_password": "x",
		})
		if codeNo != http.StatusBadGateway || respNo["error"] != "router_unreachable" {
			t.Fatalf("expected 502 router_unreachable, got %d: %+v", codeNo, respNo)
		}
		_, st := h.GetPrivacy()
		if st["block_cloud_video"] != nil {
			t.Fatalf("a failed apply must not record a setting: %+v", st)
		}
	})

	// =========================================================================
	// 2. Operator Token Endpoint Caller Authentication
	// =========================================================================
	t.Run("Subprocess_OperatorToken_StrictCallerAuth", func(t *testing.T) {
		tokenURL := h.BaseURL + "/api/v1/operator/token"

		// Use a clean HTTP client without any pre-configured cookies or tokens
		cleanJar, _ := cookiejar.New(nil)
		cleanClient := &http.Client{Jar: cleanJar}

		// 2A. Unauthenticated request without session or credentials -> HTTP 401
		reqUnauth, err := http.NewRequest(http.MethodPost, tokenURL, nil)
		if err != nil {
			t.Fatalf("failed to create unauth request: %v", err)
		}
		respUnauth, err := cleanClient.Do(reqUnauth)
		if err != nil {
			t.Fatalf("unauth request failed: %v", err)
		}
		defer respUnauth.Body.Close()

		if respUnauth.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected HTTP 401 for unauthenticated request on /api/v1/operator/token, got %d", respUnauth.StatusCode)
		}

		// 2B. Stranger / visitor session caller without elevation -> HTTP 401
		csrfReq, err := http.NewRequest(http.MethodGet, h.BaseURL+"/api/v1/auth/csrf", nil)
		if err != nil {
			t.Fatalf("failed to create csrf request: %v", err)
		}
		csrfResp, err := cleanClient.Do(csrfReq)
		if err != nil {
			t.Fatalf("csrf request failed: %v", err)
		}
		defer csrfResp.Body.Close()

		var csrfData map[string]any
		_ = json.NewDecoder(csrfResp.Body).Decode(&csrfData)
		csrfToken, _ := csrfData["csrf_token"].(string)

		reqVisitor, err := http.NewRequest(http.MethodPost, tokenURL, nil)
		if err != nil {
			t.Fatalf("failed to create visitor request: %v", err)
		}
		reqVisitor.Header.Set("X-CSRF-Token", csrfToken)

		respVisitor, err := cleanClient.Do(reqVisitor)
		if err != nil {
			t.Fatalf("visitor request failed: %v", err)
		}
		defer respVisitor.Body.Close()

		if respVisitor.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected HTTP 401 for visitor session on /api/v1/operator/token, got %d", respVisitor.StatusCode)
		}

		// 2C. An Osaio login's password never grants access to BombeCam.
		osaioPayload, _ := json.Marshal(map[string]any{"account_email": "smokeuser@example.com", "password": "SecretPassword123!"})
		reqOsaio, _ := http.NewRequest(http.MethodPost, tokenURL, bytes.NewReader(osaioPayload))
		reqOsaio.Header.Set("Content-Type", "application/json")
		reqOsaio.Header.Set("X-CSRF-Token", csrfToken)
		respOsaio, err := cleanClient.Do(reqOsaio)
		if err != nil {
			t.Fatalf("osaio-password request failed: %v", err)
		}
		respOsaio.Body.Close()
		if respOsaio.StatusCode != http.StatusUnauthorized {
			t.Fatalf("an Osaio password must not grant a token, got %d", respOsaio.StatusCode)
		}

		// 2D. The administrator, created on this PC, gets a token -> HTTP 200 OK
		setupBody, _ := json.Marshal(map[string]any{"name": "admin", "password": "AdminPassword123!"})
		setupResp, err := http.Post(h.BaseURL+"/api/v1/admin/setup", "application/json", bytes.NewReader(setupBody))
		if err != nil {
			t.Fatalf("admin setup failed: %v", err)
		}
		setupResp.Body.Close()
		if setupResp.StatusCode != http.StatusOK {
			t.Fatalf("admin setup: HTTP %d", setupResp.StatusCode)
		}
		credsPayload, _ := json.Marshal(map[string]any{
			"name":     "admin",
			"password": "AdminPassword123!",
		})
		reqElevate, err := http.NewRequest(http.MethodPost, tokenURL, bytes.NewReader(credsPayload))
		if err != nil {
			t.Fatalf("failed to create elevate request: %v", err)
		}
		reqElevate.Header.Set("Content-Type", "application/json")
		reqElevate.Header.Set("X-CSRF-Token", csrfToken)

		respElevate, err := cleanClient.Do(reqElevate)
		if err != nil {
			t.Fatalf("elevate request failed: %v", err)
		}
		defer respElevate.Body.Close()

		if respElevate.StatusCode != http.StatusOK {
			t.Fatalf("expected HTTP 200 OK for valid bootstrap credentials, got %d", respElevate.StatusCode)
		}

		var tokData map[string]any
		_ = json.NewDecoder(respElevate.Body).Decode(&tokData)
		if tokData["ok"] != true {
			t.Errorf("expected ok=true, got %v", tokData["ok"])
		}
		tokStr, _ := tokData["token"].(string)
		if !strings.HasPrefix(tokStr, "bc_tok_") {
			t.Errorf("expected valid token prefix 'bc_tok_', got %v", tokStr)
		}

		// 2D. Subsequent call using elevated session cookie + CSRF token -> HTTP 200 OK
		reqSubsequent, err := http.NewRequest(http.MethodPost, tokenURL, nil)
		if err != nil {
			t.Fatalf("failed to create subsequent request: %v", err)
		}
		reqSubsequent.Header.Set("X-CSRF-Token", csrfToken)

		respSubsequent, err := cleanClient.Do(reqSubsequent)
		if err != nil {
			t.Fatalf("subsequent request failed: %v", err)
		}
		defer respSubsequent.Body.Close()

		if respSubsequent.StatusCode != http.StatusOK {
			t.Fatalf("expected HTTP 200 OK for subsequent request with elevated session, got %d", respSubsequent.StatusCode)
		}
	})
}
