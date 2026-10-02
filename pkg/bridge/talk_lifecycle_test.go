package bridge

import (
	"github.com/gorilla/websocket"
	"net/http"
	"os/exec"
	"testing"
	"time"
)

func TestTalkSendFailureDoesNotActivate(t *testing.T) {
	v := &Viewer{sig: &Signaling{closed: true}, vc: &VideoCall{SessionID: "fixture"}}
	if ret, err := v.SetTalk(true); err == nil || ret == 0 || v.IsTalkActive() {
		t.Fatalf("failed send activated Talk: ret=%d err=%v active=%v", ret, err, v.IsTalkActive())
	}
	if v.talkWaitCh != nil {
		t.Fatal("failed send retained a response waiter")
	}
	v.vc = nil
	if _, err := v.SetTalk(true); err == nil {
		t.Fatal("missing media call accepted")
	}
}

func TestTalkStartStopCommandsAreSerialized(t *testing.T) {
	received := make(chan bool, 2)
	release := make(chan struct{})
	defer close(release)
	srv := newWSTestServer(t, func(c *websocket.Conn) {
		for {
			var m wsMsg
			if c.ReadJSON(&m) != nil {
				return
			}
			if m.Method != "service.Talk" {
				continue
			}
			enable := m.Data.(map[string]any)["EnableSpeaker"].(bool)
			received <- enable
			if enable {
				<-release
			}
			if c.WriteJSON(wsMsg{Method: "response.TalkResp", Data: map[string]any{"ret": 0}}) != nil {
				return
			}
		}
	})
	sig, err := connectHeader(srv.url(), http.Header{}, readDeadline)
	if err != nil {
		t.Fatal(err)
	}
	defer sig.Close()
	v := &Viewer{sig: sig, vc: &VideoCall{SessionID: "fixture"}, doneChan: make(chan struct{})}
	sig.OnMsg = v.OnMessage
	started, stopped := make(chan error, 1), make(chan error, 1)
	go func() { _, err := v.SetTalk(true); started <- err }()
	select {
	case enabled := <-received:
		if !enabled {
			t.Fatal("stop arrived first")
		}
	case <-time.After(time.Second):
		t.Fatal("start missing")
	}
	go func() { _, err := v.SetTalk(false); stopped <- err }()
	select {
	case <-stopped:
		t.Fatal("stop bypassed pending start")
	case <-time.After(50 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("start stuck")
	}
	select {
	case enabled := <-received:
		if enabled {
			t.Fatal("duplicate start")
		}
	case <-time.After(time.Second):
		t.Fatal("stop missing")
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop stuck")
	}
	if v.IsTalkActive() {
		t.Fatal("late start revived Talk after stop")
	}
}

func TestTalkEncoderExitClearsRunningState(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal("required FFmpeg unavailable")
	}
	v := &Viewer{doneChan: make(chan struct{}), talkActive: true}
	if err := v.StartTalkAudio(ffmpeg); err != nil {
		t.Fatal(err)
	}
	defer v.StopTalkAudio()
	v.talkMu.Lock()
	session := v.talkSess
	v.talkMu.Unlock()
	if err := session.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for v.TalkAudioRunning() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if v.TalkAudioRunning() || v.IsTalkActive() {
		t.Fatal("dead encoder still reported running")
	}
	if err := v.WriteTalkPCM([]byte{0, 0}); err == nil {
		t.Fatal("dead encoder accepted PCM")
	}
	if err := v.StartActiveTalkAudio(ffmpeg); err == nil || v.TalkAudioRunning() {
		t.Fatal("late upload recreated encoder after shutdown")
	}
}

func TestTalkStopCancelsPacketWaitingForPacing(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal("required FFmpeg unavailable")
	}
	v := &Viewer{doneChan: make(chan struct{})}
	// A deliberately long synthetic cadence makes the stop-during-wait case
	// observable without relying on a sub-millisecond scheduling window.
	v.camAudio = camAudioInfo{sampleRate: 8000, clock: 8000, tsStep: 8000}
	writes := make(chan struct{}, 3)
	v.talkWriteHook = func([]byte, uint32, bool) error { writes <- struct{}{}; return nil }
	if err := v.StartTalkAudio(ffmpeg); err != nil {
		t.Fatal(err)
	}
	defer v.StopTalkAudio()
	v.talkMu.Lock()
	session := v.talkSess
	v.talkMu.Unlock()
	session.frames <- talkPacket{payload: []byte{1}, samples: 1024}
	select {
	case <-writes:
	case <-time.After(time.Second):
		t.Fatal("first packet missing")
	}
	session.frames <- talkPacket{payload: []byte{2}, samples: 1024}
	deadline := time.Now().Add(200 * time.Millisecond)
	for len(session.frames) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(session.frames) != 0 {
		t.Fatal("pacer did not take pending packet")
	}
	v.StopTalkAudio()
	select {
	case <-writes:
		t.Fatal("packet sent after Stop while pacing")
	case <-time.After(1100 * time.Millisecond):
	}
}
