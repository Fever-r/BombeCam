package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"
)

// After BombeCam restarts, the browser only has a fresh (visitor) session.
// On this PC that session, with its CSRF token, may change the Firewall and
// the Frigate / Home Assistant settings without signing in again. A foreign
// web page (other Origin) is still refused.
func TestE2E_BrowserSessionAfterRestart_CanChangeSettings(t *testing.T) {
	h := NewGatewayHarness(t, WithSeededCameras())
	defer h.Teardown()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Timeout: 5 * time.Second, Jar: jar}
	resp, err := c.Get(h.BaseURL + "/api/v1/auth/csrf")
	if err != nil {
		t.Fatal(err)
	}
	var tok struct {
		CSRF string `json:"csrf_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	post := func(path, body, origin, csrf string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, h.BaseURL+path, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		req.Header.Set("Origin", origin)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	for _, tc := range []struct{ path, body string }{
		{"/api/v1/privacy/camera", `{"camera_id":"cam-001","blocked":false}`},
		{"/api/v1/integrations/settings", `{"nvr_address":""}`},
	} {
		if code, body := post(tc.path, tc.body, h.BaseURL, tok.CSRF); code != http.StatusOK {
			t.Errorf("%s from the UI's own session: %d %s", tc.path, code, body)
		}
		if code, _ := post(tc.path, tc.body, h.BaseURL, "wrong-token"); code == http.StatusOK {
			t.Errorf("%s accepted a wrong CSRF token", tc.path)
		}
		if code, _ := post(tc.path, tc.body, "http://evil.example.com", tok.CSRF); code != http.StatusForbidden {
			t.Errorf("%s accepted a foreign Origin: %d", tc.path, code)
		}
	}
}
