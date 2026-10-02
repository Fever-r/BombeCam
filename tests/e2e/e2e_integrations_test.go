package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Fever-r/BombeCam/pkg/policy"
)

// ----------------------------------------------------------------------------
// Group 3: Cross-Feature Combinatorial Tests (>= 12 tests)
// ----------------------------------------------------------------------------

func TestE2E_Group3_FrigateYAMLExport(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	// 1. Setup
	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})

	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001", "cam-002"})

	// 2. Fetch Frigate YAML
	code, rawYAML, _ := h.GetFrigate("format=yaml")
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK for Frigate YAML, got %d", code)
	}

	// 3. Current Frigate: a go2rtc stream per camera, named like the camera,
	// with URLs other devices can reach, and no mqtt: block (pasting it into
	// an existing config would switch off Home Assistant's Frigate MQTT).
	lan := ExpectedLANHost()
	if strings.Contains(rawYAML, "mqtt:") {
		t.Fatalf("the add-to-existing Frigate YAML must not contain mqtt:\n%s", rawYAML)
	}
	for _, want := range []string{
		"go2rtc:\n  streams:\n",
		"    front_porch:\n      - rtsp://" + lan + ":8554/front_porch\n",
		"    back_garden:\n      - rtsp://" + lan + ":8554/back_garden\n",
		"\ncameras:\n",
		"        - path: rtsp://" + lan + ":8554/front_porch\n",
		"        record: preset-record-generic-audio-copy\n",
	} {
		if !strings.Contains(rawYAML, want) {
			t.Fatalf("Frigate YAML lacks %q:\n%s", want, rawYAML)
		}
	}
	if lan != "127.0.0.1" && strings.Contains(rawYAML, "127.0.0.1") {
		t.Fatalf("Frigate YAML advertises 127.0.0.1 although this PC has %s:\n%s", lan, rawYAML)
	}
	// the full-config variant starts with mqtt disabled
	code, fresh, _ := h.GetFrigate("format=yaml", "variant=new")
	if code != http.StatusOK || !strings.Contains(fresh, "mqtt:\n  enabled: false") || !strings.Contains(fresh, "front_porch:") {
		t.Fatalf("new-config Frigate YAML: %d\n%s", code, fresh)
	}
}

func TestE2E_Group3_HomeAssistantServices(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	// Setup
	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})

	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001"})

	// 1. Fetch Home Assistant config in JSON
	code, _, haJSON := h.GetHomeAssistant()
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK for Home Assistant JSON, got %d", code)
	}

	cams, ok := haJSON["cameras"].([]any)
	if !ok || len(cams) != 1 {
		t.Fatalf("expected 1 camera in HA JSON, got %+v", haJSON)
	}
	c0 := cams[0].(map[string]any)

	// Generic Camera values (UI setup), no REST endpoints other devices can't reach
	lan := ExpectedLANHost()
	g, _ := c0["generic_camera"].(map[string]any)
	if g["stream_source"] != "rtsp://"+lan+":8554/front_porch" || g["rtsp_transport"] != "tcp" {
		t.Fatalf("generic_camera: %+v", c0)
	}
	if _, bad := c0["ptz_services"]; bad {
		t.Fatalf("ptz_services must be gone: %+v", c0)
	}
	if _, bad := c0["control_services"]; bad {
		t.Fatalf("control_services must be gone: %+v", c0)
	}
	if steps, _ := c0["setup_steps"].(string); !strings.Contains(steps, "Generic Camera") || !strings.Contains(steps, "rtsp://"+lan+":8554/front_porch") {
		t.Fatalf("setup_steps: %v", c0["setup_steps"])
	}

	// Controls still work on this PC (the viewer, and Home Assistant through MQTT)
	// 2. Control action: set_ir_on
	code, ctrlResp := h.Control("cam-001", "set_ir_on", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK for set_ir_on, got %d: %+v", code, ctrlResp)
	}

	// 3. PTZ action: move_left
	code, ptzResp := h.PTZ("cam-001", "left", 500)
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK for move_left, got %d: %+v", code, ptzResp)
	}
}

func TestE2E_Group3_CameraChurn_DynamicRTPPortPreservation(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})

	h.DiscoverCameras(true)

	// 1. Enroll Cam 1, Cam 2, Cam 3
	h.EnrollCameras([]string{"cam-001", "cam-002", "cam-003"})

	code, streamsResp := h.GetStreams(false)
	if code != http.StatusOK {
		t.Fatalf("GetStreams failed: %d", code)
	}
	sList := streamsResp["streams"].([]any)
	if len(sList) != 3 {
		t.Fatalf("expected 3 streams, got %d", len(sList))
	}

	portMap := make(map[string]int)
	for _, entry := range sList {
		e := entry.(map[string]any)
		uuid := e["uuid"].(string)
		rtp := int(e["port_mappings"].(map[string]any)["rtp"].(float64))
		portMap[uuid] = rtp
	}

	// Verify distinct even ports allocated (5000, 5002, 5004)
	if portMap["cam-001"] != 5000 || portMap["cam-002"] != 5002 || portMap["cam-003"] != 5004 {
		t.Fatalf("unexpected port allocation: %+v", portMap)
	}

	// 2. Unenroll cam-002: keep only cam-001 and cam-003
	h.EnrollCameras([]string{"cam-001", "cam-003"})
	code, streamsAfterUnenroll := h.GetStreams(false)
	if code != http.StatusOK {
		t.Fatalf("GetStreams after unenroll failed: %d", code)
	}
	sListAfter := streamsAfterUnenroll["streams"].([]any)
	if len(sListAfter) != 2 {
		t.Fatalf("expected 2 streams after unenroll, got %d", len(sListAfter))
	}

	// 3. Enroll cam-004: should reclaim freed port 5002 (lowest available even port >= 5000)
	h.EnrollCameras([]string{"cam-001", "cam-003", "cam-004"})
	code, streamsReclaimed := h.GetStreams(false)
	if code != http.StatusOK {
		t.Fatalf("GetStreams after reclaim failed: %d", code)
	}
	sListRec := streamsReclaimed["streams"].([]any)
	if len(sListRec) != 3 {
		t.Fatalf("expected 3 streams after re-enroll, got %d", len(sListRec))
	}

	reclaimedMap := make(map[string]int)
	for _, entry := range sListRec {
		e := entry.(map[string]any)
		uuid := e["uuid"].(string)
		rtp := int(e["port_mappings"].(map[string]any)["rtp"].(float64))
		reclaimedMap[uuid] = rtp
	}

	if reclaimedMap["cam-004"] != 5002 {
		t.Fatalf("expected cam-004 to reclaim port 5002, got %d. Map: %+v", reclaimedMap["cam-004"], reclaimedMap)
	}
}

func TestE2E_Group3_EncryptedProfileColdRestart_StreamURLs(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})

	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001", "cam-002"})

	// Record initial stream descriptors
	code, sInit := h.GetStreams(false)
	if code != http.StatusOK {
		t.Fatalf("initial GetStreams failed: %d", code)
	}
	initStreams := sInit["streams"].([]any)

	// Simulate sudden crash and cold restart
	h.RestartGateway()

	// Verify stream URLs preserved exactly
	var postStreams []any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		code, sPost := h.GetStreams(false)
		if code == http.StatusOK {
			if list, ok := sPost["streams"].([]any); ok && len(list) == len(initStreams) {
				postStreams = list
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(postStreams) != len(initStreams) {
		t.Fatalf("stream count mismatch after restart: init=%d, post=%d", len(initStreams), len(postStreams))
	}

	for i := range initStreams {
		e1 := initStreams[i].(map[string]any)
		e2 := postStreams[i].(map[string]any)
		if e1["uuid"] != e2["uuid"] || e1["rtsp_url"] != e2["rtsp_url"] || e1["hls_url"] != e2["hls_url"] {
			t.Fatalf("stream descriptor mismatch: %+v vs %+v", e1, e2)
		}
	}
}

func TestE2E_Group3_FrigateCustomRoles_SingleCameraFilter(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})

	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001", "cam-002"})

	// 1. Query with ?camera=cam-001&roles=record,audio
	code, rawYAML, _ := h.GetFrigate("camera=cam-001", "roles=record,audio", "format=yaml")
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", code)
	}
	if !strings.Contains(rawYAML, "  front_porch:\n") {
		t.Fatalf("missing cam-001 (front_porch) in filtered Frigate YAML: %s", rawYAML)
	}
	if strings.Contains(rawYAML, "back_garden") || strings.Contains(rawYAML, "cam-002") {
		t.Fatalf("cam-002 unexpectedly included in filtered Frigate YAML: %s", rawYAML)
	}
	// the stream name works as a filter too
	if code, byName, _ := h.GetFrigate("camera=front_porch", "format=yaml"); code != http.StatusOK || byName == "" || !strings.Contains(byName, "  front_porch:\n") {
		t.Fatalf("filter by stream name: %d %s", code, byName)
	}
	if !strings.Contains(rawYAML, "- record") || !strings.Contains(rawYAML, "- audio") {
		t.Fatalf("custom roles record,audio missing in Frigate YAML: %s", rawYAML)
	}

	// 2. Query nonexistent camera returns 404
	code, _, _ = h.GetFrigate("camera=cam-ghost-999")
	if code != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found for nonexistent camera, got %d", code)
	}
}

func TestE2E_Group3_ActiveOnlyFilter_MixedStreamingState(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})

	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001", "cam-002"})

	// Query with active_only=true
	code, streamsActiveOnly := h.GetStreams(true)
	if code != http.StatusOK {
		t.Fatalf("GetStreams active_only failed: %d", code)
	}
	if streamsActiveOnly["total_cameras"].(float64) != 2 {
		t.Fatalf("expected total_cameras=2, got %v", streamsActiveOnly["total_cameras"])
	}

	// Without active streaming runners in mock mode, active_streams is 0
	if streamsActiveOnly["active_streams"].(float64) != 0 {
		t.Fatalf("expected active_streams=0 in idle test state, got %v", streamsActiveOnly["active_streams"])
	}
	streamsList := streamsActiveOnly["streams"].([]any)
	if len(streamsList) != 0 {
		t.Fatalf("expected 0 entries with active_only=true when none streaming, got %d", len(streamsList))
	}
}

func TestE2E_Group3_HomeAssistantGenericYAML_SpecialCharacters(t *testing.T) {
	h := NewGatewayHarness(t, WithMockDevices([]map[string]any{
		{
			"uuid":       "cam-spec-01",
			"name":       "Front Porch (2nd / HD)",
			"type":       "WS03",
			"model_type": 1,
			"online":     1,
		},
		{
			"uuid":       "cam-spec-02",
			"name":       "Back Yard & BBQ #1",
			"type":       "WS04",
			"model_type": 1,
			"online":     1,
		},
	}))
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})

	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-spec-01", "cam-spec-02"})

	// Home Assistant as text: the Generic Camera steps name each camera as is
	code, rawText, _ := h.GetHomeAssistant("format=text")
	if code != http.StatusOK {
		t.Fatalf("expected 200 OK for HA text, got %d", code)
	}
	if strings.Contains(rawText, "platform: generic") {
		t.Fatalf("YAML setup of Generic Camera is deprecated; got:\n%s", rawText)
	}
	for _, want := range []string{`add "Front Porch (2nd / HD)" (Generic Camera)`, `add "Back Yard & BBQ #1" (Generic Camera)`,
		"rtsp://" + ExpectedLANHost() + ":8554/front_porch_2nd_hd", "rtsp://" + ExpectedLANHost() + ":8554/back_yard_bbq_1"} {
		if !strings.Contains(rawText, want) {
			t.Fatalf("HA text lacks %q:\n%s", want, rawText)
		}
	}
	// and Frigate keys are valid for the same names
	code, rawYAML, _ := h.GetFrigate("format=yaml")
	if code != http.StatusOK || !strings.Contains(rawYAML, "  front_porch_2nd_hd: # Front Porch (2nd / HD)\n") || !strings.Contains(rawYAML, "  back_yard_bbq_1: # Back Yard & BBQ #1\n") {
		t.Fatalf("Frigate YAML:\n%s", rawYAML)
	}
}

func TestE2E_Group3_ZeroSecretLeakage_AllIntegrationEndpoints(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	canaryPass := "SuperSecretPassword123!@#"
	canaryToken := "CanaryToken999XYZABC"
	canaries := []string{canaryPass, canaryToken, string(h.MasterKey)}

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      canaryPass,
		"country":       "1",
	})

	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001"})

	// 1. Frigate JSON and YAML
	_, body, _ := h.GetFrigate()
	assertZeroSecrets(t, body, canaries, "Frigate JSON")
	_, bodyYAML, _ := h.GetFrigate("format=yaml")
	assertZeroSecrets(t, bodyYAML, canaries, "Frigate YAML")

	// 2. Home Assistant JSON and YAML
	_, bodyHA, _ := h.GetHomeAssistant()
	assertZeroSecrets(t, bodyHA, canaries, "Home Assistant JSON")
	_, bodyHAYAML, _ := h.GetHomeAssistant("format=yaml")
	assertZeroSecrets(t, bodyHAYAML, canaries, "Home Assistant YAML")

	// 3. Streams Integration
	resp, bodyStreams, _ := h.Get("/api/v1/integrations/streams")
	if resp != nil {
		assertZeroSecrets(t, bodyStreams, canaries, "/api/v1/integrations/streams")
	}

	// 4. Assert zero secrets in logs
	h.AssertZeroSecrets(canaries)
}

func TestE2E_Group3_Concurrency_IntegrationsAndEnrollment(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})

	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001", "cam-002"})

	const workers = 15
	const iterations = 10
	var wg sync.WaitGroup
	errCh := make(chan error, workers*iterations*3)

	for w := 0; w < workers; w++ {
		wg.Add(3)

		// Group A: Frigate polling
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				code, _, _ := h.GetFrigate()
				if code != http.StatusOK {
					errCh <- fmt.Errorf("concurrent Frigate query failed: %d", code)
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()

		// Group B: Home Assistant polling
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				code, _, _ := h.GetHomeAssistant()
				if code != http.StatusOK {
					errCh <- fmt.Errorf("concurrent HA query failed: %d", code)
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()

		// Group C: Streams polling
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				code, _ := h.GetStreams(false)
				if code != http.StatusOK {
					errCh <- fmt.Errorf("concurrent Streams query failed: %d", code)
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrency error: %v", err)
	}
}

func TestE2E_Group3_MethodNotAllowed_AllIntegrationRoutes(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	routes := []string{
		"/api/v1/integrations/frigate",
		"/api/v1/integrations/homeassistant",
		"/api/v1/integrations/streams",
	}

	for _, route := range routes {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			req, err := http.NewRequest(method, h.BaseURL+route, nil)
			if err != nil {
				t.Fatalf("NewRequest failed: %v", err)
			}
			resp, _, err := h.DoRequest(req)
			if err != nil {
				t.Fatalf("request %s %s failed: %v", method, route, err)
			}
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("expected 405 Method Not Allowed for %s %s, got %d", method, route, resp.StatusCode)
			}
		}
	}
}

func TestE2E_Group3_PTZAndControl_InvalidActionsValidation(t *testing.T) {
	h := NewGatewayHarness(t)
	defer h.Teardown()

	h.Setup(map[string]any{
		"account_email": "test-user@example.com",
		"password":      "TestPassword123!",
		"country":       "1",
	})

	h.DiscoverCameras(true)
	h.EnrollCameras([]string{"cam-001"})

	// 1. Invalid PTZ direction: 99
	code, _ := h.PTZ("cam-001", 99, 400)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for PTZ direction 99, got %d", code)
	}

	// 2. Invalid control action: "fly"
	code, _ = h.Control("cam-001", "fly", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for control action 'fly', got %d", code)
	}

	// 3. Invalid IR mode: "ultra-infrared"
	code, _ = h.Control("cam-001", "ir", "ultra-infrared")
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for invalid IR mode, got %d", code)
	}
}

// TestE2E_Combo_BlockCloudVideo_AppliedOnRouterOverSSH drives the real gateway
// binary: one Apply puts the router script on a (mock) router over SSH with
// the cameras' MACs, the choice survives a gateway restart, and No removes it.
func TestE2E_Combo_BlockCloudVideo_AppliedOnRouterOverSSH(t *testing.T) {
	requireRouterShell(t)
	h := NewGatewayHarness(t, WithSeededCameras())
	defer h.Teardown()

	code, st := h.GetPrivacy()
	if code != http.StatusOK || st["ready"] != true || st["block_cloud_video"] != nil {
		t.Fatalf("before apply: %d %+v", code, st)
	}

	code, resp := h.ApplyPrivacy(true)
	if code != http.StatusOK || resp["ok"] != true || resp["firewall"] != "fw4" {
		t.Fatalf("apply yes: %d %+v\nrouter output:\n%s", code, resp, h.MockRouter.LastOutput())
	}
	if got := h.MockRouter.CameraList(); got != "aa:bb:cc:dd:ee:01\tTest Camera\naa:bb:cc:dd:ee:02\tPorch\n" {
		t.Fatalf("router camera list = %q", got)
	}
	out := h.MockRouter.LastOutput()
	for _, want := range []string{"ip daddr @control_v4 tcp dport 8883", "counter drop comment \"everything else, IPv4 and IPv6\"",
		policy.Headline} {
		if !strings.Contains(out, want) {
			t.Errorf("router output missing %q", want)
		}
	}
	if strings.Contains(h.GetCapturedLogs(), MockRouterPassword) {
		t.Fatal("SECURITY: router password appeared in gateway logs")
	}

	h.RestartGateway()
	_, st = h.GetPrivacy()
	if st["block_cloud_video"] != true || st["needs_reapply"] != false {
		t.Fatalf("after restart: %+v", st)
	}
	router := st["router"].(map[string]any)
	if router["host_key"] != h.MockRouter.HostKey {
		t.Fatalf("host key not pinned: %v vs %s", router["host_key"], h.MockRouter.HostKey)
	}

	code, resp = h.ApplyPrivacy(false)
	if code != http.StatusOK || resp["block_cloud_video"] != false {
		t.Fatalf("apply no: %d %+v", code, resp)
	}
	if h.MockRouter.Installed() {
		t.Fatal("router still has BombeCam's rules after No")
	}
	code, resp = h.ApplyPrivacyWith(map[string]any{
		"block_cloud_video": true, "router_address": h.MockRouter.Addr, "router_password": "wrong",
	})
	if code != http.StatusUnauthorized || resp["error"] != "router_auth" {
		t.Fatalf("wrong router password: %d %+v", code, resp)
	}
}

// TestE2E_Combo_BlockCloudVideoToggle_IntegrationConsistency checks that
// switching Block cloud video on and off never changes the local stream URLs.
func TestE2E_Combo_BlockCloudVideoToggle_IntegrationConsistency(t *testing.T) {
	requireRouterShell(t)
	h := NewGatewayHarness(t, WithSeededCameras())
	defer h.Teardown()

	urls := func() string {
		code, s := h.GetStreams(false)
		if code != http.StatusOK {
			t.Fatalf("GetStreams failed: %d", code)
		}
		var parts []string
		for _, e := range s["streams"].([]any) {
			parts = append(parts, fmt.Sprint(e.(map[string]any)["rtsp_url"]))
		}
		return strings.Join(parts, ",")
	}
	before := urls()
	if before == "" {
		t.Fatal("no streams listed")
	}
	for _, block := range []bool{true, false, true} {
		if code, resp := h.ApplyPrivacy(block); code != http.StatusOK {
			t.Fatalf("apply %v: %d %+v", block, code, resp)
		}
		if after := urls(); after != before {
			t.Fatalf("RTSP URLs changed when Block cloud video = %v: %s vs %s", block, before, after)
		}
	}
}

// TestE2E_Combo_PerCameraBlocking_OverRouterKey drives the real gateway
// binary through the per-camera flow: Block all, connect the router once with
// its password, then per-camera changes, camera removal and "delete all"
// reach the router over BombeCam's restricted key, including after a restart.
func TestE2E_Combo_PerCameraBlocking_OverRouterKey(t *testing.T) {
	requireRouterShell(t)
	h := NewGatewayHarness(t, WithSeededCameras())
	defer h.Teardown()

	code, resp := h.BlockAll(true)
	router, _ := resp["router"].(map[string]any)
	if code != http.StatusOK || router["error"] != "router_not_connected" || h.MockRouter.CommandCount() != 0 {
		t.Fatalf("block all before connecting: %d %+v", code, resp)
	}
	code, resp = h.ConnectRouter()
	if code != http.StatusOK || resp["ok"] != true {
		t.Fatalf("connect: %d %+v\nrouter output:\n%s", code, resp, h.MockRouter.LastOutput())
	}
	if got := h.MockRouter.CameraList(); got != "aa:bb:cc:dd:ee:01\tTest Camera\naa:bb:cc:dd:ee:02\tPorch\n" {
		t.Fatalf("router camera list = %q", got)
	}
	if !h.MockRouter.Connected() || h.MockRouter.KeyLoginCount() != 1 {
		t.Fatalf("expected BombeCam's key installed and used once (key logins %d)", h.MockRouter.KeyLoginCount())
	}

	// The key survives a restart; later changes need no password.
	h.RestartGateway()
	code, resp = h.SetCameraBlocked("cam-002", false)
	router, _ = resp["router"].(map[string]any)
	if code != http.StatusOK || router["ok"] != true {
		t.Fatalf("toggle after restart: %d %+v\n%s", code, resp, h.MockRouter.LastOutput())
	}
	if got := h.MockRouter.CameraList(); got != "aa:bb:cc:dd:ee:01\tTest Camera\n" {
		t.Fatalf("router camera list after toggle = %q", got)
	}
	_, st := h.GetPrivacy()
	if st["master"] != "some" || st["in_sync"] != true {
		t.Fatalf("status after toggle: %+v", st)
	}

	// Removing a blocked camera takes it off the router.
	r, body, err := h.Delete("/api/v1/cameras/cam-001")
	if err != nil || r.StatusCode != http.StatusOK || !strings.Contains(body, `"still_on_router":false`) {
		t.Fatalf("remove cam-001: %v %s", err, body)
	}
	if h.MockRouter.Installed() {
		t.Fatalf("router still lists cameras after removal: %q", h.MockRouter.CameraList())
	}
	if !h.MockRouter.Connected() {
		t.Fatal("BombeCam should stay connected with no cameras blocked")
	}

	// Deleting all stored information removes BombeCam's key from the router.
	code, resp = h.Reset()
	if code != http.StatusOK || resp["router_updated"] != true || h.MockRouter.Connected() {
		t.Fatalf("delete all: %d %+v (router still connected: %v)", code, resp, h.MockRouter.Connected())
	}

	logs := h.GetCapturedLogs()
	for _, secret := range []string{MockRouterPassword, "PRIVATE KEY"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("SECURITY: %q appeared in gateway logs", secret)
		}
	}
}
