package main

import (
	"bytes"
	"github.com/Fever-r/BombeCam/pkg/bridge"
	"net/http/httptest"
	"testing"
)

func TestTalkUploadsRequireActiveSessionAndCannotRearmAfterStop(t *testing.T) {
	_, sm, _, _ := setupTestEnvironment()
	_, _ = sm.Enroll([]string{"fixture"}, []bridge.Device{{UUID: "fixture", Name: "Fixture", Type: "WS03"}})
	mc, _ := sm.GetCamera("fixture")
	viewer, cleanup := setupRealMockViewerForTalkback(t, mc)
	defer cleanup()
	for _, stage := range []string{"before start", "after stop"} {
		if stage == "after stop" {
			if ret, err := viewer.SetTalk(true); ret != 0 || err != nil {
				t.Fatalf("start: %d %v", ret, err)
			}
			if _, err := viewer.SetTalk(false); err != nil {
				t.Fatal(err)
			}
		}
		for _, kind := range []string{"audio/l16;rate=16000;channels=1", "audio/aac"} {
			payload := []byte{0, 0, 0, 0}
			if kind == "audio/aac" {
				payload = buildValidADTSFrame()
			}
			req := httptest.NewRequest("POST", "/", bytes.NewReader(payload))
			req.Header.Set("Content-Type", kind)
			rr := httptest.NewRecorder()
			handleTalkStream(rr, req, "fixture", sm)
			if rr.Code != 409 || viewer.IsTalkActive() || viewer.TalkAudioRunning() {
				t.Fatalf("%s %s upload revived Talk: %d %s", stage, kind, rr.Code, rr.Body.String())
			}
		}
	}
}
