package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Fever-r/BombeCam/pkg/bridge"
)

// The app ID works exactly like the server key (e2e_server_key_test.go):
// built in, replaceable on the page without a restart, saved in the data
// folder, pinned by the environment, and asked for when missing.

const e2eNewAppID = "synthetic-e2e-new-app-id"

func appIDField(t *testing.T, status map[string]any) map[string]any {
	t.Helper()
	v, ok := status["app_id"].(map[string]any)
	if !ok {
		t.Fatalf("onboarding status has no app_id object: %v", status)
	}
	return v
}

// getAppID reads the app ID status the page sees. The page never gets the
// app ID itself, so a reply that carries one fails the test.
func getAppID(t *testing.T, h *GatewayHarness) map[string]any {
	t.Helper()
	_, raw, err := h.Get("/api/v1/app-id")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("GET /api/v1/app-id: %v (%s)", err, raw)
	}
	for _, id := range []string{E2EAppID, e2eNewAppID} {
		if strings.Contains(raw, id) {
			t.Fatal("GET /api/v1/app-id returned an app ID value")
		}
	}
	return v
}

func waitSessionStatus(t *testing.T, h *GatewayHarness, want string) {
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

// A fresh install signs in with the built-in app ID. When the vendor changes
// it, pasting the new one works without a restart; it survives a restart;
// going back to the built-in app ID removes it.
func TestE2E_AppID_BuiltInThenHotSwap(t *testing.T) {
	h := NewGatewayHarness(t)
	creds := map[string]any{"account_email": "keyuser@example.com", "password": "TestPassword123!", "country": "1", "force": true}

	if v := getAppID(t, h); v["source"] != "built_in" || v["configured"] != true || v["has_built_in"] != true || v["editable"] != true {
		t.Fatalf("fresh install app ID = %v; want the built-in one", v)
	}
	if code, body := h.Setup(creds); code != http.StatusOK {
		t.Fatalf("sign-in with the built-in app ID = %d %v", code, body)
	}

	h.MockCloud.SetAppID(e2eNewAppID)
	if code, body := h.Setup(creds); code != http.StatusUnauthorized || body["error"] != "invalid_credentials" {
		t.Fatalf("sign-in with the outdated built-in app ID = %d %v; want 401 invalid_credentials", code, body)
	}
	resp, raw, err := h.PostJSON("/api/v1/app-id", map[string]any{"app_id": e2eNewAppID})
	if err != nil || resp.StatusCode != http.StatusOK || strings.Contains(raw, e2eNewAppID) {
		t.Fatalf("saving the new app ID failed (or echoed it): %v %s", err, raw)
	}
	if code, body := h.Setup(creds); code != http.StatusOK {
		t.Fatalf("sign-in right after the swap = %d %v", code, body)
	}
	if v := getAppID(t, h); v["source"] != "key_file" {
		t.Fatalf("after the swap app ID = %v", v)
	}

	h.RestartGateway()
	if v := getAppID(t, h); v["source"] != "key_file" {
		t.Fatalf("after restart app ID = %v; want the saved one", v)
	}

	req, _ := http.NewRequest(http.MethodDelete, h.BaseURL+"/api/v1/app-id", nil)
	if resp, raw, err := h.DoRequest(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("going back to the built-in app ID failed: %v %s", err, raw)
	}
	if v := getAppID(t, h); v["source"] != "built_in" {
		t.Fatalf("after reset app ID = %v", v)
	}
	if _, err := os.Stat(filepath.Join(h.TempDir, bridge.AppIDFileName)); !os.IsNotExist(err) {
		t.Fatalf("%s still exists after going back to the built-in app ID: %v", bridge.AppIDFileName, err)
	}
	h.AssertZeroSecrets([]string{E2EServerKey, E2EAppID, e2eNewAppID})
}

// A build from source without osaio-setup.txt has no app ID: the page asks
// for it, sign-in says why, nothing reaches the cloud, and it works as soon
// as an app ID is pasted on the page.
func TestE2E_AppID_SourceBuildWithoutAppID(t *testing.T) {
	h := NewGatewayHarness(t, WithoutBuiltInValues())
	_, st := h.GetOnboardingStatus()
	if v := appIDField(t, st); v["configured"] != false || v["has_built_in"] != false || v["editable"] != true {
		t.Fatalf("app_id without a built-in one = %v", v)
	}
	if st["session_status"] != "credentials_missing" {
		t.Fatalf("first-run session_status = %v; want credentials_missing (the page asks for the app ID)", st["session_status"])
	}
	creds := map[string]any{"account_email": "keyuser@example.com", "password": "TestPassword123!", "country": "1", "force": true}
	code, body := h.Setup(creds)
	if code != http.StatusPreconditionRequired || body["error"] != "app_id_missing" || !strings.Contains(fmt.Sprint(body["message"]), "app ID") {
		t.Fatalf("sign-in without an app ID = %d %v; want 428 app_id_missing", code, body)
	}
	if n, _ := h.MockCloud.Counts(); n != 0 {
		t.Fatalf("%d request(s) reached the cloud without an app ID", n)
	}
	if !strings.Contains(h.StdoutBuf.String(), "no app ID configured") {
		t.Fatal("the log does not say that there is no app ID")
	}

	resp, raw, err := h.PostJSON("/api/v1/app-id", map[string]any{"app_id": E2EAppID})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("saving the app ID failed: %v %s", err, raw)
	}
	if code, body := h.Setup(creds); code != http.StatusOK {
		t.Fatalf("sign-in after pasting the app ID = %d %v", code, body)
	}
	if v := getAppID(t, h); v["source"] != "key_file" || v["has_built_in"] != false {
		t.Fatalf("after saving app ID = %v", v)
	}
	h.AssertZeroSecrets([]string{E2EAppID})
}

// Saved logins without an app ID: start-up waits instead of retrying, sends
// nothing, and reconnects by itself once the app ID is entered.
func TestE2E_AppID_SavedLoginsWaitThenResume(t *testing.T) {
	h := NewGatewayHarness(t, WithoutBuiltInValues(), WithSeededCameras())
	waitSessionStatus(t, h, "app_id_missing")
	time.Sleep(6 * time.Second) // past the first retry a sign-in loop would make
	if n, _ := h.MockCloud.Counts(); n != 0 {
		t.Fatalf("%d request(s) reached the cloud without an app ID", n)
	}
	if out := h.StdoutBuf.String(); strings.Contains(out, "retrying in") {
		t.Fatalf("start-up retried a sign-in that can't work:\n%s", out)
	}
	resp, raw, err := h.PostJSON("/api/v1/app-id", map[string]any{"app_id": E2EAppID})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("saving the app ID failed: %v %s", err, raw)
	}
	waitSessionStatus(t, h, "authenticated")
	if _, bad := h.MockCloud.Counts(); bad != 0 {
		t.Fatalf("%d badly signed request(s)", bad)
	}
}

// A login given at start-up (Docker's OSAIO_EMAIL/OSAIO_PASSWORD) without an
// app ID reports the app ID, not a wrong password, and signs in once it is
// entered.
func TestE2E_AppID_StartupLoginWaitsForAppID(t *testing.T) {
	h := NewGatewayHarness(t, WithoutBuiltInValues(), WithExtraArgs("-email", "keyuser@example.com", "-password", "TestPassword123!", "-country", "1"))
	waitSessionStatus(t, h, "app_id_missing")
	if n, _ := h.MockCloud.Counts(); n != 0 {
		t.Fatalf("%d request(s) reached the cloud without an app ID", n)
	}
	resp, raw, err := h.PostJSON("/api/v1/app-id", map[string]any{"app_id": E2EAppID})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("saving the app ID failed: %v %s", err, raw)
	}
	waitSessionStatus(t, h, "authenticated")
}

// A broken saved app ID is reported, not skipped for the built-in one, and
// stops sign-in until a good one is entered.
func TestE2E_AppID_BrokenSavedFileWaitsThenResumes(t *testing.T) {
	h := NewGatewayHarness(t, WithSeededCameras(), func(h *GatewayHarness) {
		if err := os.WriteFile(filepath.Join(h.TempDir, bridge.AppIDFileName), []byte("one\ntwo\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	waitSessionStatus(t, h, "app_id_missing")
	_, st := h.GetOnboardingStatus()
	if v := appIDField(t, st); v["configured"] != false || v["editable"] != true || !strings.Contains(fmt.Sprint(v["problem"]), "more than one app ID line") {
		t.Fatalf("app_id = %v", v)
	}
	if n, _ := h.MockCloud.Counts(); n != 0 {
		t.Fatalf("%d request(s) reached the cloud with a broken app ID", n)
	}
	resp, raw, err := h.PostJSON("/api/v1/app-id", map[string]any{"app_id": E2EAppID})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("saving the app ID failed: %v %s", err, raw)
	}
	waitSessionStatus(t, h, "authenticated")
}

// An app ID from the environment wins over the saved and built-in ones and
// cannot be changed from the page.
func TestE2E_AppID_EnvironmentIsNotEditable(t *testing.T) {
	h := NewGatewayHarness(t, WithAppIDInEnvironment(E2EAppID))
	_, st := h.GetOnboardingStatus()
	if v := appIDField(t, st); v["configured"] != true || v["source"] != "environment" || v["editable"] != false {
		t.Fatalf("app_id = %v; want configured from the environment, not editable", v)
	}
	resp, raw, err := h.PostJSON("/api/v1/app-id", map[string]any{"app_id": e2eNewAppID})
	if err != nil || resp.StatusCode != http.StatusConflict || !strings.Contains(raw, "app_id_not_editable") {
		t.Fatalf("page save over an environment app ID = %v %s; want 409 app_id_not_editable", err, raw)
	}
	creds := map[string]any{"account_email": "keyuser@example.com", "password": "TestPassword123!", "country": "1", "force": true}
	if code, body := h.Setup(creds); code != http.StatusOK {
		t.Fatalf("sign-in with the app ID from the environment = %d %v", code, body)
	}
}

// A login given at start-up that Osaio refuses because the built-in app ID
// is outdated signs in once the new app ID is saved on the page, without a
// restart.
func TestE2E_AppID_RefusedStartupLoginRetriesAfterSave(t *testing.T) {
	h := NewGatewayHarness(t, WithExtraArgs("-email", "keyuser@example.com", "-password", "TestPassword123!", "-country", "1"),
		func(h *GatewayHarness) { h.MockCloud.SetAppID(e2eNewAppID) })
	waitSessionStatus(t, h, "invalid_credentials")
	resp, raw, err := h.PostJSON("/api/v1/app-id", map[string]any{"app_id": e2eNewAppID})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("saving the app ID failed: %v %s", err, raw)
	}
	waitSessionStatus(t, h, "authenticated")
	h.AssertZeroSecrets([]string{E2EServerKey, E2EAppID, e2eNewAppID})
}
