package bridge

import (
	"context"
	"encoding/json"
	shadowshim "github.com/Fever-r/BombeCam/pkg/bridge/shadow_shim"
	"testing"
	"time"
)

func TestLocalControlNeverReadsDesiredState(t *testing.T) {
	ch := NewMQTTControlChannel(nil, 40*time.Millisecond)
	defer ch.Close()
	if err := ch.SetIR(context.Background(), "fixture-camera", IRModeAuto); err == nil {
		t.Fatal("missing camera reply was accepted")
	}
	if _, err := ch.GetIR(context.Background(), "fixture-camera"); err == nil {
		t.Fatal("desired state became reported state")
	}
	if err := ch.Daemon().IngestReported("fixture-camera", map[string]any{"IrLedMode": IRModeAuto}); err != nil {
		t.Fatal(err)
	}
	if mode, err := ch.GetIR(context.Background(), "fixture-camera"); err != nil || mode != IRModeAuto {
		t.Fatalf("fresh report: %v %v", mode, err)
	}
}

func TestLocalControlRejectsStaleAndUnrelatedReports(t *testing.T) {
	ch := NewMQTTControlChannel(nil, 50*time.Millisecond)
	defer ch.Close()
	req := shadowshim.ShadowUpdateRequest{Timestamp: time.Now().Add(-time.Minute).Unix(), State: shadowshim.ShadowState{Reported: map[string]any{"LedOnOff": 1}}}
	payload, _ := json.Marshal(req)
	_ = ch.Daemon().Publish("$aws/things/fixture-camera/shadow/update", payload)
	if _, err := ch.GetLED(context.Background(), "fixture-camera"); err == nil {
		t.Fatal("stale report accepted")
	}
	if err := ch.SetLED(context.Background(), "fixture-camera", true); err == nil {
		t.Fatal("stale state confirmed a new command")
	}
}

func TestLocalPTZStopsBeforeReadbackTimeout(t *testing.T) {
	ch := NewMQTTControlChannel(nil, 300*time.Millisecond)
	defer ch.Close()
	msgs, unsub := ch.Daemon().Subscribe("$aws/things/fixture-camera/shadow/update/delta")
	defer unsub()
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- ch.MovePTZ(context.Background(), "fixture-camera", 1, 40) }()
	for {
		select {
		case msg := <-msgs:
			var delta shadowshim.ShadowDeltaMessage
			_ = json.Unmarshal(msg.Payload, &delta)
			if n, ok := delta.State["direction"].(float64); ok && n == 0 {
				if time.Since(start) > 200*time.Millisecond {
					t.Fatal("stop waited for readback")
				}
				if err := <-done; err == nil {
					t.Fatal("unconfirmed movement succeeded")
				}
				return
			}
		case <-time.After(time.Second):
			t.Fatal("no automatic stop")
		}
	}
}

func TestLocalTalkFailureDoesNotActivateAudio(t *testing.T) {
	ch := NewMQTTControlChannel(nil, 40*time.Millisecond)
	defer ch.Close()
	v := &Viewer{uuid: "fixture-camera", ctrlChan: ch, vc: &VideoCall{SessionID: "fixture-session"}}
	if _, err := v.SetTalk(true); err == nil {
		t.Fatal("unconfirmed talk succeeded")
	}
	if v.IsTalkActive() {
		t.Fatal("unconfirmed talk activated audio")
	}
	doc, err := ch.Daemon().GetLatestShadow("fixture-camera")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.State.Reported) != 0 {
		t.Fatal("manufactured talk report")
	}
}
