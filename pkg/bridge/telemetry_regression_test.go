package bridge

import (
	"context"
	"github.com/gorilla/websocket"
	"net/http"
	"testing"
	"time"
)

func TestCameraReadbackRejectsUncorrelatedReplies(t *testing.T) {
	for _, mismatch := range []string{"missing-id", "old-id", "other-camera", "set-echo", "missing-key", "invalid-value", "dropped"} {
		t.Run(mismatch, func(t *testing.T) {
			ts := newWSTestServer(t, func(c *websocket.Conn) {
				for {
					var m wsMsg
					if c.ReadJSON(&m) != nil {
						return
					}
					if m.Method != "atr.get" {
						continue
					}
					m.Data = map[string]any{"IrLedMode": 1}
					switch mismatch {
					case "missing-id":
						m.MsgID = ""
					case "old-id":
						m.MsgID = "old"
					case "other-camera":
						m.UUID = "other"
					case "set-echo":
						m.Method = "atr.set"
					case "missing-key":
						m.Data = map[string]any{"LedOnOff": 1}
					case "invalid-value":
						m.Data = map[string]any{"IrLedMode": 1.5}
					case "dropped":
						continue
					}
					if c.WriteJSON(m) != nil {
						return
					}
				}
			})
			sig, err := connectHeader(ts.url(), http.Header{}, readDeadline)
			if err != nil {
				t.Fatal(err)
			}
			defer sig.Close()
			sig.BindCamera("fixture", "WS03")
			cc := NewSignalingControlChannel(sig)
			if err := cc.SetIR(context.Background(), "fixture", 1); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if n, err := cc.GetIR(ctx, "fixture"); err == nil {
				t.Fatalf("unconfirmed reply returned %d", n)
			}
		})
	}
}

func TestCameraReadbackReturnsActualStateAndExpires(t *testing.T) {
	ts := newWSTestServer(t, func(c *websocket.Conn) {
		for {
			var m wsMsg
			if c.ReadJSON(&m) != nil {
				return
			}
			if m.Method != "atr.get" {
				continue
			}
			m.Data = map[string]any{"IrLedMode": 0, "LedOnOff": 0, "LightSW": 0}
			if c.WriteJSON(m) != nil {
				return
			}
		}
	})
	sig, err := connectHeader(ts.url(), http.Header{}, readDeadline)
	if err != nil {
		t.Fatal(err)
	}
	defer sig.Close()
	sig.BindCamera("fixture", "WS03")
	cc := NewSignalingControlChannel(sig)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := cc.SetIR(ctx, "fixture", 1); err != nil {
		t.Fatal(err)
	}
	if n, err := cc.GetIR(ctx, "fixture"); err != nil || n != 0 {
		t.Fatalf("actual IR: %d %v", n, err)
	}
	if err := cc.SetLED(ctx, "fixture", true); err != nil {
		t.Fatal(err)
	}
	if n, err := cc.GetLED(ctx, "fixture"); err != nil || n {
		t.Fatalf("actual LED: %v %v", n, err)
	}
	if err := cc.SetLight(ctx, "fixture", true); err != nil {
		t.Fatal(err)
	}
	if n, err := cc.GetLight(ctx, "fixture"); err != nil || n {
		t.Fatalf("actual light: %v %v", n, err)
	}
	if len(sig.FreshAttributes("other", time.Second)) != 0 {
		t.Fatal("other camera reused observation")
	}
	sig.mu.Lock()
	for k, o := range sig.observations {
		o.at = time.Now().Add(-time.Minute)
		sig.observations[k] = o
	}
	sig.mu.Unlock()
	if len(sig.FreshAttributes("fixture", 30*time.Second)) != 0 {
		t.Fatal("stale observation exported")
	}
}

func TestCameraReadbackBeforeCommandCannotConfirmNewCommand(t *testing.T) {
	queried := make(chan wsMsg, 1)
	release := make(chan struct{})
	ts := newWSTestServer(t, func(c *websocket.Conn) {
		var m wsMsg
		if c.ReadJSON(&m) != nil {
			return
		}
		queried <- m
		// Read the later write so the server knows it was sent before its old reply.
		var set wsMsg
		if c.ReadJSON(&set) != nil {
			return
		}
		<-release
		m.Data = map[string]any{"IrLedMode": 1}
		_ = c.WriteJSON(m)
		drain(c)
	})
	sig, err := connectHeader(ts.url(), http.Header{}, readDeadline)
	if err != nil {
		t.Fatal(err)
	}
	defer sig.Close()
	sig.BindCamera("fixture", "WS03")
	cc := NewSignalingControlChannel(sig)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := cc.GetIR(ctx, "fixture"); done <- err }()
	select {
	case <-queried:
	case <-time.After(time.Second):
		t.Fatal("query not sent")
	}
	if err := cc.SetIR(ctx, "fixture", 1); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("pre-command read confirmed later write")
	}
	if len(sig.FreshAttributes("fixture", time.Second)) != 0 {
		t.Fatal("invalidated observation cached")
	}
}

func TestCameraReadbackDisconnectAndCloseWakeWaiters(t *testing.T) {
	for _, closeClient := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnect", true: "close"}[closeClient], func(t *testing.T) {
			received := make(chan struct{}, 1)
			release := make(chan struct{})
			ts := newWSTestServer(t, func(c *websocket.Conn) {
				var m wsMsg
				if c.ReadJSON(&m) != nil {
					return
				}
				received <- struct{}{}
				<-release
				_ = c.Close()
			})
			sig, err := connectHeader(ts.url(), http.Header{}, readDeadline)
			if err != nil {
				t.Fatal(err)
			}
			defer sig.Close()
			sig.BindCamera("fixture", "WS03")
			done := make(chan error, 1)
			go func() { _, err := sig.QueryAttributes(context.Background(), "fixture", "IrLedMode"); done <- err }()
			select {
			case <-received:
			case <-time.After(time.Second):
				t.Fatal("query not sent")
			}
			if closeClient {
				_ = sig.Close()
			}
			close(release)
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("lost connection returned success")
				}
			case <-time.After(time.Second):
				t.Fatal("query did not terminate")
			}
		})
	}
}
