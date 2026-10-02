package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Fever-r/BombeCam/pkg/bridge"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type missingReadback struct {
	mockControlChannel
	reads int
}

func (cc *missingReadback) GetIR(context.Context, string) (int, error) {
	cc.reads++
	return 0, fmt.Errorf("no camera answer")
}
func (cc *missingReadback) GetLED(context.Context, string) (bool, error) {
	cc.reads++
	return false, fmt.Errorf("no camera answer")
}
func (cc *missingReadback) GetLight(context.Context, string) (bool, error) {
	cc.reads++
	return false, fmt.Errorf("no camera answer")
}

func TestAcceptedCommandsDoNotFabricateHardwareConfirmation(t *testing.T) {
	_, sm, _, _ := setupTestEnvironment()
	_, err := sm.Enroll([]string{"fixture"}, []bridge.Device{{UUID: "fixture", Name: "Fixture", Type: "WS03"}})
	if err != nil {
		t.Fatal(err)
	}
	cc := &missingReadback{}
	SetGatewayControlChannel(cc)
	defer SetGatewayControlChannel(nil)
	mc, _ := sm.GetCamera("fixture")
	for _, action := range []string{"ir", "led", "light", "ptz"} {
		t.Run(action, func(t *testing.T) {
			before := mc.Freshness
			rr := httptest.NewRecorder()
			handleCameraControl(rr, httptest.NewRequest("POST", "/", strings.NewReader(`{"action":"`+action+`","mode":"on"}`)), "fixture", sm)
			if rr.Code != 200 {
				t.Fatalf("dispatch: %d %s", rr.Code, rr.Body.String())
			}
			var body struct {
				Status        string         `json:"status"`
				ReadbackError string         `json:"readback_error"`
				Confirmation  string         `json:"confirmation"`
				Confirmed     map[string]any `json:"confirmed_state"`
				Requested     map[string]any `json:"requested_state"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Status != "sent" || body.Confirmation != "not_requested" || body.ReadbackError != "" || len(body.Confirmed) != 0 || len(body.Requested) == 0 {
				t.Fatalf("fabricated confirmation: %s", rr.Body.String())
			}
			if cc.reads != 0 {
				t.Fatal("successful dispatch queried camera confirmation")
			}
			if !mc.Freshness.Equal(before) {
				t.Fatal("sent command refreshed observed telemetry timestamp")
			}
		})
	}
	mc.UpdateObservedIR("auto")
	mc.UpdateObservedLED("on")
	mc.UpdateObservedLight("on")
	rr := httptest.NewRecorder()
	handleCameraShadow(rr, httptest.NewRequest("GET", "/", nil), "fixture", sm)
	var doc struct {
		State struct {
			Reported map[string]any `json:"reported"`
		} `json:"state"`
		Timestamp int64 `json:"timestamp"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.State.Reported) != 0 || doc.Timestamp != 0 {
		t.Fatalf("failed read exposed defaults/cache: %s", rr.Body.String())
	}
}

type differentReadback struct{ mockControlChannel }

func (*differentReadback) GetIR(context.Context, string) (int, error) { return bridge.IRModeAuto, nil }

func TestOptionalTelemetryKeepsActualStateSeparateFromDispatch(t *testing.T) {
	_, sm, _, _ := setupTestEnvironment()
	_, _ = sm.Enroll([]string{"fixture"}, []bridge.Device{{UUID: "fixture", Name: "Fixture", Type: "WS03"}})
	SetGatewayControlChannel(&differentReadback{})
	defer SetGatewayControlChannel(nil)
	rr := httptest.NewRecorder()
	handleCameraControl(rr, httptest.NewRequest("POST", "/", strings.NewReader(`{"action":"ir","mode":"off"}`)), "fixture", sm)
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "sent" || body["requested_state"].(map[string]any)["IrLedMode"] != float64(bridge.IRModeOff) {
		t.Fatalf("dispatch not reported: %s", rr.Body.String())
	}
	mc, _ := sm.GetCamera("fixture")
	if !mc.ObservedIRAt.IsZero() {
		t.Fatal("dispatch invented a camera observation")
	}
	rr = httptest.NewRecorder()
	handleCameraShadow(rr, httptest.NewRequest("GET", "/", nil), "fixture", sm)
	if mc.ObservedIR != "auto" || mc.ObservedIRAt.IsZero() {
		t.Fatal("actual readback not recorded")
	}
	mc.ObservedIRAt = time.Now().Add(-time.Minute)
	if status := mc.GetStatus(nil); status.ObservedIR != "unknown" {
		t.Fatalf("expired IR observation: %+v", status)
	}
}
