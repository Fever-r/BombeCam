package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Fever-r/BombeCam/pkg/bridge"
	"github.com/gorilla/websocket"
)

// fakeVendorWS records what the gateway sends to the vendor WebSocket.
type fakeVendorWS struct {
	mu       sync.Mutex
	msgs     []map[string]any
	reported map[string]any
}

func (f *fakeVendorWS) handler(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{}
	c, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	for {
		var m map[string]any
		if err := c.ReadJSON(&m); err != nil {
			return
		}
		f.mu.Lock()
		f.msgs = append(f.msgs, m)
		if m["method"] == "atr.get" && f.reported != nil {
			m["data"] = f.reported
			if err := c.WriteJSON(m); err != nil {
				f.mu.Unlock()
				return
			}
		}
		f.mu.Unlock()
	}
}

func (f *fakeVendorWS) find(method, key string) (any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.msgs {
		if m["method"] != method {
			continue
		}
		if d, ok := m["data"].(map[string]any); ok {
			if v, ok := d[key]; ok {
				return v, true
			}
		}
		if key == "" {
			return m["data"], true
		}
	}
	return nil, false
}

func shadowReported(t *testing.T, mux *http.ServeMux, cam string) map[string]any {
	t.Helper()
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/cameras/"+cam+"/shadow", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("shadow: %d %s", rr.Code, rr.Body.String())
	}
	var doc struct {
		State struct {
			Reported map[string]any `json:"reported"`
		} `json:"state"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &doc)
	return doc.State.Reported
}

// Motion and sound detection are the camera's MotionDetectSW / SoundDetectSW
// attributes, set with atr.set over the camera's control connection and read
// back from what the camera reports (else the Osaio device list).
func TestDetectionSwitches(t *testing.T) {
	sm, streamMgr, _, mux := setupTestEnvironment()
	devs := []bridge.Device{{UUID: "uuid-det", Name: "Hall", Type: "WS03", Online: 1,
		Config: map[string]any{"MotionDetectSW": float64(0), "SoundDetectSW": float64(1)}}}
	sm.SetStateForTest(SessionStatusAuthenticated, "user@example.com", "uid-1", "", devs)
	_, _ = streamMgr.Enroll([]string{"uuid-det"}, devs)

	fake := &fakeVendorWS{}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer srv.Close()
	sig, err := bridge.Connect("ws"+strings.TrimPrefix(srv.URL, "http"), "tok", "uid-1", "39")
	if err != nil {
		t.Fatal(err)
	}
	defer sig.Close()
	registerSig(bridge.Device{UUID: "uuid-det", Name: "Hall", Type: "WS03"}, sig, nil)
	defer unregisterSig("uuid-det")
	SetGatewayControlChannel(bridge.NewSignalingControlChannel(sig))
	defer SetGatewayControlChannel(nil)

	// The old device list cannot certify current camera state.
	rep := shadowReported(t, mux, "uuid-det")
	if len(rep) != 0 {
		t.Fatalf("unverified device-list values leaked: %v", rep)
	}

	for _, tc := range []struct{ action, key string }{{"motion", "MotionDetectSW"}, {"sound", "SoundDetectSW"}} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/cameras/uuid-det/control", strings.NewReader(`{"action":"`+tc.action+`","mode":"on","value":1}`)))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s on: %d %s", tc.action, rr.Code, rr.Body.String())
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			if v, ok := fake.find("atr.set", tc.key); ok {
				if v != float64(1) {
					t.Fatalf("%s: sent %v", tc.key, v)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: no atr.set reached the vendor socket", tc.key)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// Successful sends do not become reported values.
	rep = shadowReported(t, mux, "uuid-det")
	if len(rep) != 0 {
		t.Fatalf("requested values leaked: %v", rep)
	}
	// the shadow read asks the camera for its current values
	time.Sleep(100 * time.Millisecond)
	if v, ok := fake.find("atr.get", ""); !ok || !strings.Contains(strings.Join(toStrings(v), ","), "MotionDetectSW") {
		t.Fatalf("no atr.get for the detection switches: %v", v)
	}

	// what the camera reports wins
	fake.mu.Lock()
	fake.reported = map[string]any{"MotionDetectSW": 0, "SoundDetectSW": 0}
	fake.mu.Unlock()
	rep = shadowReported(t, mux, "uuid-det")
	if rep["MotionDetectSW"] != float64(0) || rep["SoundDetectSW"] != float64(0) {
		t.Fatalf("camera report: %v", rep)
	}

	// bad value
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/cameras/uuid-det/control", strings.NewReader(`{"action":"motion","mode":"sideways"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad value: %d", rr.Code)
	}
}

func toStrings(v any) []string {
	var out []string
	if a, ok := v.([]any); ok {
		for _, x := range a {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
