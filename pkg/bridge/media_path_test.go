package bridge

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// The viewer tells the user whether media is really local.
func TestMediaPathKind(t *testing.T) {
	v := &Viewer{}
	if err := v.SetAllowedSubnet("192.168.1.0/24"); err != nil {
		t.Fatal(err)
	}
	pair := func(local, remote webrtc.ICECandidateType, addr string) *webrtc.ICECandidatePair {
		return &webrtc.ICECandidatePair{
			Local:  &webrtc.ICECandidate{Typ: local, Address: "192.168.1.20"},
			Remote: &webrtc.ICECandidate{Typ: remote, Address: addr},
		}
	}
	cases := []struct {
		p    *webrtc.ICECandidatePair
		want string
	}{
		{pair(webrtc.ICECandidateTypeHost, webrtc.ICECandidateTypeHost, "192.168.1.50"), "lan"},
		{pair(webrtc.ICECandidateTypeRelay, webrtc.ICECandidateTypeHost, "192.168.1.50"), "relay"},
		{pair(webrtc.ICECandidateTypeHost, webrtc.ICECandidateTypeRelay, "47.1.2.3"), "relay"},
		{pair(webrtc.ICECandidateTypeSrflx, webrtc.ICECandidateTypeSrflx, "81.2.3.4"), "internet"},
		{pair(webrtc.ICECandidateTypeHost, webrtc.ICECandidateTypeHost, "10.9.9.9"), "internet"},
		{nil, ""},
	}
	for i, c := range cases {
		if got := v.MediaPathKind(c.p); got != c.want {
			t.Errorf("case %d: %q, want %q", i, got, c.want)
		}
	}
}

// The vendor socket also delivers the camera's replies to the Osaio app
// (another SessionId). BombeCam must not take the app's ICE candidates into
// its own connection, nor the app's talk replies as its own.
func TestOnMessage_IgnoresOtherViewersSessions(t *testing.T) {
	v := &Viewer{model: "t", vc: &VideoCall{SessionID: "usr0bombecam000a:e56:OWN"}}
	cand := func(sid string) map[string]any {
		return map[string]any{
			"SessionId":           sid,
			"WebrtcCandidate":     "candidate:1 1 udp 1694498815 203.0.113.61 51200 typ srflx raddr 0.0.0.0 rport 0 generation 0",
			"WebrtcSdpMid":        "0",
			"WebrtcSdpMLineIndex": float64(0),
		}
	}
	v.OnMessage("event.IceCandidate", cand("usr0phoneapp000b:e56:APP"))
	if len(v.pendingICE) != 0 {
		t.Fatalf("the app's candidate was taken: %d pending", len(v.pendingICE))
	}
	v.OnMessage("event.IceCandidate", cand("usr0bombecam000a:e56:OWN"))
	if len(v.pendingICE) != 1 {
		t.Fatalf("our own candidate was not kept: %d pending", len(v.pendingICE))
	}

	ch := make(chan int, 1)
	v.talkWaitCh = ch
	v.OnMessage("response.TalkResp", map[string]any{"SessionId": "usr0phoneapp000b:e56:APP", "enableSpeakerRet": float64(7)})
	select {
	case r := <-ch:
		t.Fatalf("the app's talk reply (%d) was taken as ours", r)
	default:
	}
	v.OnMessage("response.TalkResp", map[string]any{"SessionId": "usr0bombecam000a:e56:OWN", "enableSpeakerRet": float64(0)})
	if r := <-ch; r != 0 {
		t.Fatalf("our talk reply: %d", r)
	}
}

// The Osaio app watching with BombeCam's own login (same login part of the
// SessionId) is remembered; another login's viewer is not.
func TestOnMessage_NotesSameLoginViewer(t *testing.T) {
	v := &Viewer{model: "t", vc: &VideoCall{SessionID: "usr0bombecam000a:e56:OWN"}}
	v.OnMessage("response.SwitchResp", map[string]any{"SessionId": "usr0phoneapp000b:e56:APP", "Ret": float64(0)})
	if v.SameLoginViewerActive(time.Minute) {
		t.Fatal("a viewer on another login must not count")
	}
	v.OnMessage("response.SwitchResp", map[string]any{"SessionId": "usr0bombecam000a:e56:APP2", "Ret": float64(0)})
	if !v.SameLoginViewerActive(time.Minute) {
		t.Fatal("the app on BombeCam's own login was not noticed")
	}
	if v.SameLoginViewerActive(0) {
		t.Fatal("the window must bound how long it counts")
	}
}
