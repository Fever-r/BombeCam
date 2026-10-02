package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Fever-r/BombeCam/pkg/serverkey"
)

func serverKeyField(t *testing.T, status map[string]any) map[string]any {
	t.Helper()
	sk, ok := status["server_key"].(map[string]any)
	if !ok {
		t.Fatalf("onboarding status has no server_key object: %v", status)
	}
	return sk
}

// getServerKey reads the key status the page sees. The page never gets the
// key itself, so a reply that carries one fails the test.
func getServerKey(t *testing.T, h *GatewayHarness) map[string]any {
	t.Helper()
	_, raw, err := h.Get("/api/v1/server-key")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("GET /api/v1/server-key: %v (%s)", err, raw)
	}
	if _, has := v["key"]; has {
		t.Fatalf("GET /api/v1/server-key returned the key: %v", v)
	}
	for _, k := range []string{E2EServerKey, E2EBuiltInKey} {
		if strings.Contains(raw, k) {
			t.Fatal("GET /api/v1/server-key returned a key value")
		}
	}
	return v
}

// A fresh install signs in with the built-in key and shows it in settings.
// When the vendor rotates its key, pasting the new one works without a
// restart; it survives a restart; going back to the built-in key removes it.
func TestE2E_ServerKey_BuiltInThenHotSwap(t *testing.T) {
	h := NewGatewayHarness(t, WithoutServerKey())
	h.MockCloud.SetSignKey(E2EBuiltInKey)
	creds := map[string]any{"account_email": "keyuser@example.com", "password": "TestPassword123!", "country": "1", "force": true}

	if sk := getServerKey(t, h); sk["source"] != "built_in" || sk["configured"] != true || sk["has_built_in"] != true {
		t.Fatalf("fresh install server key = %v; want the built-in key", sk)
	}
	if code, body := h.Setup(creds); code != http.StatusOK {
		t.Fatalf("sign-in with the built-in key = %d %v", code, body)
	}

	// The vendor changes its key: sign-in now fails until the new key is set.
	h.MockCloud.SetSignKey(E2EServerKey)
	if code, body := h.Setup(creds); code != http.StatusUnauthorized || body["error"] != "invalid_credentials" {
		t.Fatalf("sign-in with the outdated built-in key = %d %v; want 401 invalid_credentials", code, body)
	}
	resp, raw, err := h.PostJSON("/api/v1/server-key", map[string]any{"key": E2EServerKey})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("saving the new key failed: %v %s", err, raw)
	}
	if code, body := h.Setup(creds); code != http.StatusOK {
		t.Fatalf("sign-in right after the hot swap = %d %v", code, body)
	}
	if sk := getServerKey(t, h); sk["source"] != "key_file" {
		t.Fatalf("after the swap server key = %v", sk)
	}

	h.RestartGateway()
	if sk := getServerKey(t, h); sk["source"] != "key_file" {
		t.Fatalf("after restart server key = %v; want the saved key", sk)
	}

	req, _ := http.NewRequest(http.MethodDelete, h.BaseURL+"/api/v1/server-key", nil)
	if resp, raw, err := h.DoRequest(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("going back to the built-in key failed: %v %s", err, raw)
	}
	if sk := getServerKey(t, h); sk["source"] != "built_in" {
		t.Fatalf("after reset server key = %v", sk)
	}
	if _, err := os.Stat(filepath.Join(h.TempDir, serverkey.FileName)); !os.IsNotExist(err) {
		t.Fatalf("server.key still exists after going back to the built-in key: %v", err)
	}
	h.AssertZeroSecrets([]string{E2EServerKey, E2EBuiltInKey, E2EAppID})
}

// A build from source without the server key has none built in: it says so,
// contacts nobody, offers no built-in key to go back to, and works as soon as
// a key is pasted on the page.
func TestE2E_ServerKey_SourceBuildWithoutKey(t *testing.T) {
	h := NewGatewayHarness(t, WithoutServerKey(), WithoutBuiltInKey())
	creds := map[string]any{"account_email": "keyuser@example.com", "password": "TestPassword123!", "country": "1", "force": true}

	if sk := getServerKey(t, h); sk["configured"] != false || sk["has_built_in"] != false || sk["editable"] != true {
		t.Fatalf("server key without a built-in one = %v", sk)
	}
	if code, body := h.Setup(creds); code == http.StatusOK || body["error"] != "server_key_missing" {
		t.Fatalf("sign-in without any key = %d %v; want server_key_missing", code, body)
	}
	if n, _ := h.MockCloud.Counts(); n != 0 {
		t.Fatalf("%d request(s) reached the cloud without a key", n)
	}

	resp, raw, err := h.PostJSON("/api/v1/server-key", map[string]any{"key": E2EServerKey})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("saving the key failed: %v %s", err, raw)
	}
	if code, body := h.Setup(creds); code != http.StatusOK {
		t.Fatalf("sign-in after pasting the key = %d %v", code, body)
	}
	if sk := getServerKey(t, h); sk["source"] != "key_file" || sk["has_built_in"] != false {
		t.Fatalf("after saving server key = %v", sk)
	}
	h.AssertZeroSecrets([]string{E2EServerKey})
}

// A broken saved key stops sign-in without contacting the cloud, and the
// gateway reconnects by itself once a good key is entered.
func TestE2E_ServerKey_BrokenSavedKeyWaitsThenResumes(t *testing.T) {
	h := NewGatewayHarness(t, WithoutServerKey(), WithSeededCameras(), func(h *GatewayHarness) {
		if err := os.WriteFile(filepath.Join(h.TempDir, serverkey.FileName), []byte("one\ntwo\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	})

	waitStatus := func(want string) {
		t.Helper()
		var st map[string]any
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			_, st = h.GetOnboardingStatus()
			if st["session_status"] == want {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("session_status = %v; want %s", st["session_status"], want)
	}

	waitStatus("server_key_missing")
	_, st := h.GetOnboardingStatus()
	if sk := serverKeyField(t, st); sk["configured"] != false || sk["editable"] != true || !strings.Contains(sk["problem"].(string), "more than one key line") {
		t.Fatalf("server_key = %v", sk)
	}
	if n, _ := h.MockCloud.Counts(); n != 0 {
		t.Fatalf("%d request(s) reached the cloud with a broken key", n)
	}

	resp, raw, err := h.PostJSON("/api/v1/server-key", map[string]any{"key": E2EServerKey})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("saving the key failed: %v %s", err, raw)
	}
	waitStatus("authenticated")
	if _, bad := h.MockCloud.Counts(); bad != 0 {
		t.Fatalf("%d badly signed request(s)", bad)
	}
}

// A key from the environment wins over the saved and built-in keys and
// cannot be changed from the page.
func TestE2E_ServerKey_EnvironmentKeyIsNotEditable(t *testing.T) {
	h := NewGatewayHarness(t)
	_, st := h.GetOnboardingStatus()
	if sk := serverKeyField(t, st); sk["configured"] != true || sk["source"] != "environment" || sk["editable"] != false {
		t.Fatalf("server_key = %v; want configured from the environment, not editable", sk)
	}
	if _, has := serverKeyField(t, st)["key"]; has {
		t.Fatal("onboarding status must not carry the key")
	}
	resp, raw, err := h.PostJSON("/api/v1/server-key", map[string]any{"key": "another-synthetic-key"})
	if err != nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("page save over an environment key = %v %s; want 409", err, raw)
	}
}

// A login given at start-up that Osaio refuses because the built-in key is
// outdated signs in once the new key is saved on the page, without a
// restart.
func TestE2E_ServerKey_RefusedStartupLoginRetriesAfterSave(t *testing.T) {
	h := NewGatewayHarness(t, WithoutServerKey(), WithExtraArgs("-email", "keyuser@example.com", "-password", "TestPassword123!", "-country", "1"))
	waitSessionStatus(t, h, "invalid_credentials")
	resp, raw, err := h.PostJSON("/api/v1/server-key", map[string]any{"key": E2EServerKey})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("saving the key failed: %v %s", err, raw)
	}
	waitSessionStatus(t, h, "authenticated")
	h.AssertZeroSecrets([]string{E2EServerKey, E2EBuiltInKey, E2EAppID})
}
