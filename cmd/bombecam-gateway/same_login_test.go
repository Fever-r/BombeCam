package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withHealthyMediaServer points the gateway at a stand-in MediaMTX API that
// lists no paths.
func withHealthyMediaServer(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	sup, base := currentMediaRuntime()
	setMediaRuntime(nil, srv.URL)
	t.Cleanup(func() {
		srv.Close()
		setMediaRuntime(sup, base)
	})
}

// The viewer explains a missing picture when the Osaio app holds the camera's
// live view with BombeCam's own login.
func TestDescribeStreamState_SameLoginViewer(t *testing.T) {
	const id = "cam-same-login"
	defer clearSameLoginViewer(id)
	withHealthyMediaServer(t)

	state, _, _, _ := describeStreamState(id, true, nil)
	if state != "waiting_for_video" {
		t.Fatalf("state = %q, want waiting_for_video", state)
	}
	noteSameLoginViewer(id)
	state, detail, ready, _ := describeStreamState(id, true, nil)
	if state != "busy_same_login" || ready || !strings.Contains(detail, "its own Osaio login") {
		t.Fatalf("state = %q, detail = %q", state, detail)
	}
	clearSameLoginViewer(id)
	if state, _, _, _ := describeStreamState(id, true, nil); state != "waiting_for_video" {
		t.Fatalf("after video arrives the note must go: %q", state)
	}
}
