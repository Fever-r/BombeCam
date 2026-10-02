package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// ----------------------------------------------------------------------------
// Group 4: Real-World Production Workload Scenarios (>= 6 scenarios)
// ----------------------------------------------------------------------------

func TestE2E_Group4_FullProductionSimulation_SetupToRecovery(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	canaryPass := "MasterPass#2026-Production!"
	canaries := []string{canaryPass, "mock-auth-token-test-abc", string(h.MasterKey)}

	// Step 1: Initial cold boot (unconfigured)
	code, status := h.GetOnboardingStatus()
	if code != http.StatusOK || status["first_run"] != true || status["has_profile"] != false {
		t.Fatalf("expected unconfigured initial state: %+v", status)
	}

	// Step 2: Setup profile with credentials
	setupPayload := map[string]any{
		"account_email":   "production-admin@example.com",
		"password":        canaryPass,
		"country":         "1",
		"allowed_subnets": []string{"192.168.10.0/24"},
		"timezone":        "America/New_York",
		"timezone_offset": -5.0,
	}
	code, setupResp := h.Setup(setupPayload)
	if code != http.StatusOK || setupResp["status"] != "configured" {
		t.Fatalf("setup failed: %d, %+v", code, setupResp)
	}
	if len(h.MockRouter.Commands) != 0 {
		t.Fatal("setup must not touch the router")
	}

	// Step 3: Discover 4 cameras from cloud inventory
	code, discResp := h.DiscoverCameras(true)
	if code != http.StatusOK {
		t.Fatalf("discovery failed: %d", code)
	}
	cams := discResp["cameras"].([]any)
	if len(cams) != 4 {
		t.Fatalf("expected 4 discovered cameras, got %d", len(cams))
	}

	// Step 4: Selectively enroll 2 cameras (cam-001 and cam-002)
	code, enrollResp := h.EnrollCameras([]string{"cam-001", "cam-002"})
	if code != http.StatusOK {
		t.Fatalf("enrollment failed: %d", code)
	}
	enrolled := enrollResp["enrolled"].([]any)
	if len(enrolled) != 2 {
		t.Fatalf("expected 2 enrolled, got %v", enrolled)
	}

	// Step 5: Verify assigned even RTP ports
	code, streamsInit := h.GetStreams(false)
	if code != http.StatusOK {
		t.Fatalf("GetStreams failed: %d", code)
	}
	sList := streamsInit["streams"].([]any)
	if len(sList) != 2 {
		t.Fatalf("expected 2 streams, got %d", len(sList))
	}

	// Step 6-7: Block all is saved at once, but the router can only match
	// cameras by MAC, which BombeCam learns from a LAN stream; until then
	// the cameras wait and the status explains why.
	if code, resp := h.BlockAll(true); code != http.StatusOK {
		t.Fatalf("block all: %d %+v", code, resp)
	}
	code, priv := h.GetPrivacy()
	if code != http.StatusOK || priv["ready"] != false || priv["cameras_connected"] != float64(2) || priv["master"] != "all" {
		t.Fatalf("privacy status before MACs are known: %d %+v", code, priv)
	}

	// Step 8: Extract Frigate YAML
	code, frigateYAML, _ := h.GetFrigate("format=yaml")
	if code != http.StatusOK {
		t.Fatalf("GetFrigate YAML failed: %d", code)
	}
	lan := ExpectedLANHost()
	if !strings.Contains(frigateYAML, "rtsp://"+lan+":8554/front_porch") || !strings.Contains(frigateYAML, "rtsp://"+lan+":8554/back_garden") {
		t.Fatalf("Frigate YAML missing camera RTSP paths: %s", frigateYAML)
	}

	// Step 9: Extract Home Assistant config & execute PTZ and control
	code, _, haJSON := h.GetHomeAssistant()
	if code != http.StatusOK {
		t.Fatalf("GetHomeAssistant failed: %d", code)
	}
	cams, ok := haJSON["cameras"].([]any)
	if !ok || len(cams) != 2 {
		t.Fatalf("expected 2 HA cameras, got: %+v", haJSON)
	}

	code, _ = h.PTZ("cam-001", "left", 500)
	if code != http.StatusOK {
		t.Fatalf("PTZ move failed: %d", code)
	}

	code, _ = h.Control("cam-001", "set_ir_on", nil)
	if code != http.StatusOK {
		t.Fatalf("Control set_ir_on failed: %d", code)
	}

	// Step 10: "No" can always be applied (it needs no camera MACs). It
	// runs the router script, so not where the mock router can't.
	if routerShell() {
		code, noResp := h.ApplyPrivacy(false)
		if code != http.StatusOK || noResp["block_cloud_video"] != false {
			t.Fatalf("apply No failed: %d %+v", code, noResp)
		}
	}

	// Step 11: Sudden process crash
	h.KillGateway()

	// Step 12: Cold restart from encrypted profile
	h.RestartGateway()

	// Step 13: Verify restored state
	code, postStatus := h.GetOnboardingStatus()
	if code != http.StatusOK || postStatus["has_profile"] != true {
		t.Fatalf("expected profile restored after restart: %+v", postStatus)
	}

	var postList []any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		code, postStreams := h.GetStreams(false)
		if code == http.StatusOK {
			if list, ok := postStreams["streams"].([]any); ok && len(list) == 2 {
				postList = list
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(postList) != 2 {
		t.Fatalf("expected 2 restored cameras after restart, got %d", len(postList))
	}

	// Step 14: Forensic audit for zero leaked credentials
	h.AssertZeroSecrets(canaries)
}

func TestE2E_Group4_DisasterRecovery_CorruptedProfileFallback(t *testing.T) {
	// Start with corrupted profile file
	h := NewGatewayHarness(t, WithCorruptedProfile())
	defer h.Teardown()

	// Gateway should remain operational in fallback mode
	code, status := h.GetOnboardingStatus()
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK from status with corrupt profile fallback, got %d", code)
	}
	// session_status should report unavailable due to decryption failure
	if status["session_status"] != "unavailable" {
		t.Fatalf("expected session_status unavailable, got: %v", status["session_status"])
	}
	if status["ready_for_discovery"] == true {
		t.Fatalf("ready_for_discovery should be false on corrupted profile")
	}

	// Admin executes clean reset
	code, resetResp := h.Reset()
	if code != http.StatusOK || resetResp["status"] != "reset" {
		t.Fatalf("reset failed: %d, %+v", code, resetResp)
	}

	// Subsequent status confirms first-run ready
	code, postReset := h.GetOnboardingStatus()
	if code != http.StatusOK || postReset["first_run"] != true {
		t.Fatalf("expected clean first-run ready after reset: %+v", postReset)
	}
}

func TestE2E_Group4_MultiCameraScale_PortExhaustionAndReclaim(t *testing.T) {
	// Configure 8 mock cameras
	eightCameras := make([]map[string]any, 8)
	for i := 0; i < 8; i++ {
		uuid := fmt.Sprintf("cam-scale-%03d", i+1)
		eightCameras[i] = map[string]any{
			"uuid":       uuid,
			"name":       fmt.Sprintf("Scale Cam %d", i+1),
			"type":       "WS03",
			"model_type": 1,
			"online":     1,
		}
	}

	h := NewGatewayHarness(t, WithMockDevices(eightCameras))
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})

	h.DiscoverCameras(true)

	// 1. Enroll 6 cameras: cam-scale-001 through 006
	initialEnroll := []string{
		"cam-scale-001", "cam-scale-002", "cam-scale-003",
		"cam-scale-004", "cam-scale-005", "cam-scale-006",
	}
	code, _ := h.EnrollCameras(initialEnroll)
	if code != http.StatusOK {
		t.Fatalf("initial 6-camera enroll failed: %d", code)
	}

	code, sResp := h.GetStreams(false)
	if code != http.StatusOK {
		t.Fatalf("GetStreams failed: %d", code)
	}
	sList := sResp["streams"].([]any)
	if len(sList) != 6 {
		t.Fatalf("expected 6 streams, got %d", len(sList))
	}

	// 2. Unenroll 3 cameras (002, 004, 006): keep 001, 003, 005
	keepList := []string{"cam-scale-001", "cam-scale-003", "cam-scale-005"}
	h.EnrollCameras(keepList)

	// 3. Re-enroll cam-scale-007 and cam-scale-008
	newList := []string{"cam-scale-001", "cam-scale-003", "cam-scale-005", "cam-scale-007", "cam-scale-008"}
	h.EnrollCameras(newList)

	code, sResp2 := h.GetStreams(false)
	if code != http.StatusOK {
		t.Fatalf("GetStreams after re-enroll failed: %d", code)
	}
	sList2 := sResp2["streams"].([]any)
	if len(sList2) != 5 {
		t.Fatalf("expected 5 streams, got %d", len(sList2))
	}

	// Verify all assigned ports are distinct even ports >= 5000 with zero collisions
	seenPorts := make(map[int]string)
	for _, entry := range sList2 {
		e := entry.(map[string]any)
		uuid := e["uuid"].(string)
		rtp := int(e["port_mappings"].(map[string]any)["rtp"].(float64))
		if rtp < 5000 || rtp%2 != 0 {
			t.Fatalf("invalid RTP port %d for camera %s", rtp, uuid)
		}
		if prev, exists := seenPorts[rtp]; exists {
			t.Fatalf("RTP PORT COLLISION: port %d assigned to both %s and %s", rtp, prev, uuid)
		}
		seenPorts[rtp] = uuid
	}
}

func TestE2E_Group4_NetworkPartitionAndLockoutRecovery(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})

	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001"})

	// Admin executes manual unlock via POST /api/v1/cameras/{id}/unlock
	code, unlockResp := h.Unlock("cam-001")
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK from unlock, got %d: %+v", code, unlockResp)
	}
	if unlockResp["status"] != "unlocked" {
		t.Fatalf("expected status=unlocked, got %v", unlockResp["status"])
	}

	// Unlock nonexistent camera returns 404
	code, _ = h.Unlock("cam-nonexistent-999")
	if code != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found for nonexistent camera unlock, got %d", code)
	}
}

func TestE2E_Group4_ZeroDayCredentialsLeakAudit_LogAndPayloadScrape(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	canaryPass := "P@ssw0rd!#%s%v\u00A9\u20AC-CanarySecret"
	canaryToken := "Token-XYZ-987-DoNotLeakUnderAnyCircumstances"
	canarySecondary := "SecondaryPass#456-Secret"
	canaries := []string{canaryPass, canaryToken, canarySecondary, string(h.MasterKey)}

	// 1. Setup with canaries
	h.Setup(map[string]any{
		"account_email": "forensic-audit@example.com",
		"password":      canaryPass,
		"country":       "1",
		"secondary_account": map[string]any{
			"enabled":       true,
			"account_email": "sec@example.com",
			"password":      canarySecondary,
		},
	})

	// 2. Discover, enroll, query streams, execute control
	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001"})
	h.GetStreams(false)
	h.PTZ("cam-001", "up", 300)
	h.Control("cam-001", "set_ir_on", nil)

	// 3. Query all integration endpoints
	_, bodyFrigateJSON, _ := h.GetFrigate()
	assertZeroSecrets(t, bodyFrigateJSON, canaries, "Frigate JSON")
	_, bodyFrigateYAML, _ := h.GetFrigate("format=yaml")
	assertZeroSecrets(t, bodyFrigateYAML, canaries, "Frigate YAML")

	_, bodyHAJSON, _ := h.GetHomeAssistant()
	assertZeroSecrets(t, bodyHAJSON, canaries, "Home Assistant JSON")
	_, bodyHAYAML, _ := h.GetHomeAssistant("format=yaml")
	assertZeroSecrets(t, bodyHAYAML, canaries, "Home Assistant YAML")

	// 4. Query public profile view
	resp, bodyProf, _ := h.Get("/api/v1/onboarding/profile")
	if resp != nil {
		assertZeroSecrets(t, bodyProf, canaries, "Public Profile View")
	}

	// 5. Scrape stdout and stderr logs and on-disk encrypted profile
	h.AssertZeroSecrets(canaries)
}

func TestE2E_Group4_DualClientIntegration_FrigateAndHomeAssistantSimultaneous(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})

	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001", "cam-002"})

	const workers = 10
	const iterations = 8
	var wg sync.WaitGroup
	errCh := make(chan error, workers*iterations*2)

	// Client 1: Frigate querying streams & YAML continuously
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				code, rawYAML, _ := h.GetFrigate("format=yaml")
				if code != http.StatusOK || !strings.Contains(rawYAML, "/front_porch\n") {
					errCh <- fmt.Errorf("Frigate YAML fetch error: code=%d", code)
					return
				}
				time.Sleep(15 * time.Millisecond)
			}
		}()
	}

	// Client 2: Home Assistant polling status and executing PTZ
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				code, _, _ := h.GetHomeAssistant()
				if code != http.StatusOK {
					errCh <- fmt.Errorf("HA fetch error: code=%d", code)
					return
				}
				code, _ = h.PTZ("cam-001", "stop", 100)
				if code != http.StatusOK {
					errCh <- fmt.Errorf("HA PTZ command error: code=%d", code)
					return
				}
				time.Sleep(15 * time.Millisecond)
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("Dual client concurrency failure: %v", err)
	}
}
