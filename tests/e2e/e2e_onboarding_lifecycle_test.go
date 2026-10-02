package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Fever-r/BombeCam/pkg/netstack"
	"github.com/Fever-r/BombeCam/pkg/profile"
)

// ----------------------------------------------------------------------------
// Group 1: Feature Coverage (AC3 Dual-Mode Onboarding & Lifecycle)
// ----------------------------------------------------------------------------

func TestE2E_Group1_FirstRun_Setup(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	canaryPass := "UserSecurePassword!#987"
	canaries := []string{canaryPass, "mock-auth-token-test-abc", string(h.MasterKey)}

	// 1. Initial State: Unconfigured / first-run mode
	code, status := h.GetOnboardingStatus()
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK from status, got %d", code)
	}
	if status["has_profile"] != false {
		t.Fatalf("expected has_profile=false, got %v", status["has_profile"])
	}
	if status["first_run"] != true {
		t.Fatalf("expected first_run=true, got %v", status["first_run"])
	}
	if v, ok := status["block_cloud_video"]; !ok || v != nil {
		t.Fatalf("expected block_cloud_video=null on first run, got %v", v)
	}

	// Also verify via alias /api/v1/setup
	resp, body, err := h.Get("/api/v1/setup")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/setup failed: resp=%v, err=%v", resp, err)
	}
	assertZeroSecrets(t, body, canaries, "GET /api/v1/setup")

	// 2. Submit Setup payload
	setupPayload := map[string]any{
		"account_email":   "test-user@example.com",
		"password":        canaryPass,
		"country":         "1",
		"allowed_subnets": []string{"192.168.1.0/24"},
		"timezone":        "America/New_York",
		"timezone_offset": -5.0,
	}

	code, setupResp := h.Setup(setupPayload)
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK from setup, got %d: %+v", code, setupResp)
	}
	if setupResp["status"] != "configured" {
		t.Fatalf("expected status=configured, got %v", setupResp["status"])
	}
	if _, ok := setupResp["privacy_mode"]; ok {
		t.Fatalf("retired privacy_mode field still reported: %+v", setupResp)
	}

	// 3. Signing in never contacts the router
	if len(h.MockRouter.Commands) != 0 || h.MockRouter.Installed() {
		t.Fatalf("setup must not touch the router")
	}

	// 4. Assert profile exists and is encrypted on disk
	if _, err := os.Stat(h.ProfilePath); err != nil {
		t.Fatalf("profile file not found on disk: %v", err)
	}
	h.AssertZeroSecrets(canaries)

	// 5. Subsequent status check confirms configured state
	code, statusAfter := h.GetOnboardingStatus()
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK from status after setup, got %d", code)
	}
	if statusAfter["has_profile"] != true || statusAfter["first_run"] != false {
		t.Fatalf("unexpected status after setup: %+v", statusAfter)
	}
}

func TestE2E_Group1_CloudDiscovery_OptionB(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	// Complete setup first
	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})

	// 1. Discovery call (initial)
	code, discResp := h.DiscoverCameras(false)
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK from discovery, got %d", code)
	}
	cams, ok := discResp["cameras"].([]any)
	if !ok || len(cams) != 4 {
		t.Fatalf("expected 4 discovered cameras, got %v", discResp)
	}
	freshness, ok := discResp["freshness_ts"].(string)
	if !ok || freshness == "" {
		t.Fatalf("missing or invalid freshness_ts: %v", discResp["freshness_ts"])
	}
	if _, err := time.Parse(time.RFC3339, freshness); err != nil {
		t.Fatalf("invalid RFC3339 freshness_ts: %v", err)
	}

	initialCalls := h.MockCloud.GetDeviceListCalls()

	// 2. Cached discovery call (without ?refresh=true)
	code, _ = h.DiscoverCameras(false)
	if code != http.StatusOK {
		t.Fatalf("cached discovery call failed: %d", code)
	}
	if h.MockCloud.GetDeviceListCalls() != initialCalls {
		t.Fatalf("expected cached discovery not to call cloud backend, calls=%d", h.MockCloud.GetDeviceListCalls())
	}

	// 3. Forced refresh discovery call (?refresh=true)
	code, _ = h.DiscoverCameras(true)
	if code != http.StatusOK {
		t.Fatalf("refresh discovery call failed: %d", code)
	}
	if h.MockCloud.GetDeviceListCalls() <= initialCalls {
		t.Fatalf("expected ?refresh=true to call cloud backend")
	}

	// 4. Test alias route /api/v1/devices/discover
	resp, body, err := h.Get("/api/v1/devices/discover")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("alias /api/v1/devices/discover failed: code=%d, err=%v", resp.StatusCode, err)
	}
	if !strings.Contains(body, "cam-001") {
		t.Fatalf("alias route did not return camera inventory: %s", body)
	}
}

func TestE2E_Group1_SelectiveEnrollment_And_ProfilePersistence(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})

	h.DiscoverCameras(true)

	// 1. Selectively enroll subset ["cam-001", "cam-002"]
	code, enrollResp := h.EnrollCameras([]string{"cam-001", "cam-002"})
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK from enroll, got %d: %+v", code, enrollResp)
	}
	enrolled, _ := enrollResp["enrolled"].([]any)
	if len(enrolled) != 2 {
		t.Fatalf("expected 2 enrolled cameras, got %v", enrolled)
	}

	// 2. Verify streams descriptors and dynamic even RTP ports assigned
	code, s1 := h.GetStream("cam-001")
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK for cam-001 stream, got %d", code)
	}
	// friendly stream names; the camera-ID URL keeps working
	if s1["rtsp_url"] != "rtsp://"+ExpectedLANHost()+":8554/front_porch" || !strings.HasSuffix(s1["rtsp_url_by_id"].(string), "/cam-001") {
		t.Fatalf("invalid RTSP URLs for cam-001: %v / %v", s1["rtsp_url"], s1["rtsp_url_by_id"])
	}

	code, s2 := h.GetStream("cam-002")
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK for cam-002 stream, got %d", code)
	}
	if s2["rtsp_url"] != "rtsp://"+ExpectedLANHost()+":8554/back_garden" || !strings.HasSuffix(s2["rtsp_url_by_id"].(string), "/cam-002") {
		t.Fatalf("invalid RTSP URLs for cam-002: %v / %v", s2["rtsp_url"], s2["rtsp_url_by_id"])
	}

	code, streamsResp := h.GetStreams(false)
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK from GetStreams, got %d", code)
	}
	streamsList, _ := streamsResp["streams"].([]any)
	if len(streamsList) != 2 {
		t.Fatalf("expected 2 streams in streams integration, got %d", len(streamsList))
	}
	st1 := streamsList[0].(map[string]any)
	st2 := streamsList[1].(map[string]any)
	port1 := int(st1["port_mappings"].(map[string]any)["rtp"].(float64))
	port2 := int(st2["port_mappings"].(map[string]any)["rtp"].(float64))
	if port1 < 5000 || port1%2 != 0 {
		t.Fatalf("expected even RTP port >= 5000 for st1, got %d", port1)
	}
	if port2 < 5000 || port2%2 != 0 || port2 == port1 {
		t.Fatalf("expected distinct even RTP port for st2, got %d (port1 was %d)", port2, port1)
	}

	// 3. Inspect encrypted profile on disk
	pm := profile.NewManager(h.ProfilePath, h.MasterKey)
	prof, err := pm.Load(context.Background())
	if err != nil {
		t.Fatalf("failed to load decrypted profile from disk: %v", err)
	}
	if len(prof.Cameras) != 2 {
		t.Fatalf("expected exactly 2 cameras in disk profile, got %d", len(prof.Cameras))
	}
	if _, ok := prof.Cameras["cam-001"]; !ok {
		t.Fatalf("cam-001 missing from profile")
	}
	if _, ok := prof.Cameras["cam-002"]; !ok {
		t.Fatalf("cam-002 missing from profile")
	}
	if _, ok := prof.Cameras["cam-003"]; ok {
		t.Fatalf("cam-003 unexpectedly enrolled in profile")
	}
}

func TestE2E_Group1_DynamicICE_LAN_IP_Resolution_And_Persistence(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email":   "test-user@example.com",
		"password":        "TestPassword123!",
		"country":         "1",
		"allowed_subnets": []string{"192.168.1.0/24"},
	})

	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001"})

	// 1. Candidate SDP validation using ICECandidateFilter
	filter, err := netstack.NewICECandidateFilter([]string{"192.168.1.0/24"}, nil)
	if err != nil {
		t.Fatalf("failed to create ICECandidateFilter: %v", err)
	}

	candSDP := "candidate:1 1 UDP 2130706431 192.168.1.150 50000 typ host"
	ip, ok := filter.ValidateCandidate(candSDP)
	if !ok || ip == nil || ip.String() != "192.168.1.150" {
		t.Fatalf("expected candidate to validate as 192.168.1.150, got ip=%v, ok=%v", ip, ok)
	}

	// 2. Update profile with the learned IP (as the stream manager does)
	discoveredIP := "192.168.1.150"
	pm := profile.NewManager(h.ProfilePath, h.MasterKey)
	_, err = pm.Update(context.Background(), func(p *profile.Profile) error {
		if cam, found := p.Cameras["cam-001"]; found {
			cam.IPAddress = discoveredIP
			p.Cameras["cam-001"] = cam
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to persist learned IP to profile: %v", err)
	}

	// Verify profile disk state contains learned IP
	reloaded, err := pm.Load(context.Background())
	if err != nil || reloaded.Cameras["cam-001"].IPAddress != discoveredIP {
		t.Fatalf("profile on disk does not reflect learned IP: %v, cam=%+v", err, reloaded.Cameras["cam-001"])
	}
}

func TestE2E_Group1_DedicatedPTZ_And_StreamLifecycle(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})

	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001"})

	// 1. Verify stream descriptor
	code, streamInfo := h.GetStream("cam-001")
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK from stream info, got %d", code)
	}
	if streamInfo["rtsp_url"] != "rtsp://"+ExpectedLANHost()+":8554/front_porch" || !strings.HasSuffix(streamInfo["rtsp_url_by_id"].(string), "/cam-001") {
		t.Fatalf("invalid RTSP URL: %v / %v", streamInfo["rtsp_url"], streamInfo["rtsp_url_by_id"])
	}
	// the viewer's HLS is relayed by the gateway; other devices get the LAN URL
	if streamInfo["hls_url"] != "/api/v1/cameras/cam-001/hls/index.m3u8" || streamInfo["hls_url_lan"] != "http://"+ExpectedLANHost()+":8888/front_porch/index.m3u8" {
		t.Fatalf("invalid HLS URLs: %v / %v", streamInfo["hls_url"], streamInfo["hls_url_lan"])
	}

	// 2. Numeric PTZ command: direction 1 (left), duration 500ms
	code, ptzResp := h.PTZ("cam-001", 1, 500)
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK from PTZ command, got %d: %+v", code, ptzResp)
	}
	if ptzResp["direction"] != "left" && ptzResp["direction"].(float64) != 1 {
		t.Logf("PTZ response direction: %v", ptzResp["direction"])
	}

	// 3. String PTZ command: direction "up"
	code, ptzUp := h.PTZ("cam-001", "up", 400)
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK from PTZ up command, got %d: %+v", code, ptzUp)
	}
}

// ----------------------------------------------------------------------------
// Group 2: Boundary & Corner Cases (AC3 Dual-Mode Onboarding & Lifecycle)
// ----------------------------------------------------------------------------

func TestE2E_Group2_FirstRun_AlreadyInitialized_409Conflict(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	payload := map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	}

	// First setup succeeds
	code, _ := h.Setup(payload)
	if code != http.StatusOK {
		t.Fatalf("initial setup failed: %d", code)
	}

	// Second setup with force: false returns 409 Conflict
	payload["force"] = false
	code, errResp := h.Setup(payload)
	if code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict on re-setup without force, got %d: %+v", code, errResp)
	}

	// Third setup with force: true succeeds
	payload["force"] = true
	code, reconfigured := h.Setup(payload)
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK with force: true, got %d: %+v", code, reconfigured)
	}
}

func TestE2E_Group2_Setup_InvalidPayload_400BadRequest(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	// 1. Empty email
	code, _ := h.Setup(map[string]any{
		"account_email": "",
		"password":      "ValidPassword123!",
		"country":       "1",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for empty email, got %d", code)
	}

	// 2. Empty password
	code, _ = h.Setup(map[string]any{
		"account_email": "user@example.com",
		"password":      "",
		"country":       "1",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for empty password, got %d", code)
	}

	// 3. Malformed JSON
	req, _ := http.NewRequest(http.MethodPost, h.BaseURL+"/api/v1/onboarding/setup", strings.NewReader("{bad-json"))
	req.Header.Set("Content-Type", "application/json")
	resp, _, _ := h.DoRequest(req)
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for malformed JSON, got %v", resp)
	}
}

func TestE2E_Group2_Setup_CloudAuthFailure_401And503(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	// Case A: Cloud rejects credentials (401)
	h.MockCloud.SetRejectLogin(true)
	code, _ := h.Setup(map[string]any{
		"account_email": "wrong@example.com",
		"password":      "WrongPass123!",
		"country":       "1",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for bad credentials, got %d", code)
	}

	// Profile must not be created
	code, status := h.GetOnboardingStatus()
	if status["has_profile"] == true {
		t.Fatalf("profile should NOT be created on auth failure")
	}

	// Case B: Cloud network timeout (503)
	h.MockCloud.SetRejectLogin(false)
	h.MockCloud.SetSimulateTimeout(true)
	code, _ = h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable on cloud timeout, got %d", code)
	}
}

func TestE2E_Group2_Enrollment_InvalidCameraIDs_400BadRequest(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})
	h.DiscoverCameras(true)

	// Case A: Empty array
	code, _ := h.EnrollCameras([]string{})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for empty camera_ids, got %d", code)
	}

	// Case B: Nonexistent ID
	code, _ = h.EnrollCameras([]string{"cam-ghost-999"})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for nonexistent camera ID, got %d", code)
	}

	// Case C: Mixed valid and invalid IDs
	code, _ = h.EnrollCameras([]string{"cam-001", "cam-ghost-888"})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for mixed invalid camera IDs, got %d", code)
	}
}

func TestE2E_Group2_PTZ_BoundaryAndInvalidDirection_Validation(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})
	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001"})

	// 1. Direction < 0
	code, _ := h.PTZ("cam-001", -1, 500)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for direction < 0, got %d", code)
	}

	// 2. Direction > 4
	code, _ = h.PTZ("cam-001", 5, 500)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for direction > 4, got %d", code)
	}

	// 3. Invalid string direction
	code, _ = h.PTZ("cam-001", "diagonal", 500)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for string direction 'diagonal', got %d", code)
	}

	// 4. Nonexistent camera ID
	code, _ = h.PTZ("cam-ghost-000", 1, 500)
	if code != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found for nonexistent camera, got %d", code)
	}
}

func TestE2E_Group2_UnauthenticatedDiscovery_401Unauthorized(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	// In first-run unauthenticated state:
	// Discovery must return 401 Unauthorized
	code, _ := h.DiscoverCameras(false)
	if code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for unauthenticated discovery, got %d", code)
	}

	// Profile check returns 404 Not Found
	resp, _, err := h.Get("/api/v1/onboarding/profile")
	if err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found for missing profile, got %v", resp)
	}
}

func TestE2E_Group2_GatewayReset_Lifecycle(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	// Initial configuration
	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})
	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001", "cam-002"})

	// Reset via POST /api/v1/onboarding/reset
	code, resetResp := h.Reset()
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK from reset, got %d: %+v", code, resetResp)
	}
	if resetResp["status"] != "reset" {
		t.Fatalf("expected status=reset, got %v", resetResp["status"])
	}

	// 1. Profile file removed from disk
	if _, err := os.Stat(h.ProfilePath); !os.IsNotExist(err) {
		t.Fatalf("expected profile file to be removed from disk after reset, err=%v", err)
	}

	// 2. No router was set up, so nothing on a router changed
	if resetResp["router_updated"] != true || !strings.Contains(fmt.Sprint(resetResp["router_message"]), "No router was set up") {
		t.Fatalf("unexpected router fields on reset: %+v", resetResp)
	}

	// 3. Status reverts to first-run mode
	code, status := h.GetOnboardingStatus()
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK from status after reset, got %d", code)
	}
	if status["has_profile"] != false || status["first_run"] != true {
		t.Fatalf("expected first_run=true and has_profile=false after reset, got: %+v", status)
	}
}

// TestE2E_Group2_BlockCloudVideo_NotReadyUntilMACsKnown: cameras enrolled
// through the cloud have no MAC yet, so "Yes" is refused with a reason
// instead of installing rules that would match nothing.
func TestE2E_Group2_BlockCloudVideo_NotReadyUntilMACsKnown(t *testing.T) {
	requireRouterShell(t)
	h := NewGatewayHarness(t)
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})
	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001"})

	// Choosing works at once; the camera waits for its MAC.
	code, resp := h.BlockAll(true)
	if code != http.StatusOK {
		t.Fatalf("block all: %d %+v", code, resp)
	}
	code, st := h.GetPrivacy()
	cam := st["cameras"].([]any)[0].(map[string]any)
	if code != http.StatusOK || st["ready"] != false || !strings.Contains(fmt.Sprint(st["not_ready_reason"]), "MAC") || cam["state"] != "waiting_for_mac" {
		t.Fatalf("expected waiting for the MAC: %d %+v", code, st)
	}
	if len(h.MockRouter.Commands) != 0 {
		t.Fatal("the router must not be contacted before it is connected")
	}
	// Connecting installs BombeCam's key but blocks nothing yet (no MAC).
	code, resp = h.ConnectRouter()
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("connect: %d %+v\n%s", code, resp, h.MockRouter.LastOutput())
	}
	if !h.MockRouter.Connected() || h.MockRouter.Installed() {
		t.Fatalf("expected connected with no rules (connected=%v installed=%v)", h.MockRouter.Connected(), h.MockRouter.Installed())
	}
	req, _ := http.NewRequest(http.MethodPost, h.BaseURL+"/api/v1/privacy/camera", strings.NewReader("bad-json"))
	req.Header.Set("Content-Type", "application/json")
	r, _, _ := h.DoRequest(req)
	if r == nil || r.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON")
	}
}
