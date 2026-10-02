package bridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func TestG711_KnownValues(t *testing.T) {
	cases := []struct {
		in   int16
		ulaw byte
		alaw byte
	}{
		{0, 0xFF, 0xD5},
		{-1, 0x7F, 0x55},
		{32767, 0x80, 0xAA},
		{-32768, 0x00, 0x2A},
		{1000, 0xCE, 0xFA},
		{-1000, 0x4E, 0x7A},
	}
	for _, c := range cases {
		if got := linearToULaw(c.in); got != c.ulaw {
			t.Errorf("µ-law(%d) = %#02x, want %#02x", c.in, got, c.ulaw)
		}
		if got := linearToALaw(c.in); got != c.alaw {
			t.Errorf("A-law(%d) = %#02x, want %#02x", c.in, got, c.alaw)
		}
	}
}

// ffmpeg's own G.711 encoders agree with ours on a sweep of values, except
// that FFmpeg rounds to the nearest level where the G.711 reference rounds
// down: a boundary value may land one level apart.
func TestG711_MatchesFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	pcm := make([]byte, 0, 2*65536)
	for v := -32768; v <= 32767; v++ {
		pcm = binary.LittleEndian.AppendUint16(pcm, uint16(int16(v)))
	}
	for _, law := range []string{"mulaw", "alaw"} {
		cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "s16le", "-ar", "8000", "-ac", "1", "-i", "pipe:0",
			"-c:a", "pcm_"+law, "-f", law, "pipe:1")
		cmd.Stdin = bytes.NewReader(pcm)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v", law, err)
		}
		if len(out) != 65536 {
			t.Fatalf("%s: %d bytes", law, len(out))
		}
		// sign and magnitude level (0..127) of a code
		level := func(c byte) (byte, int) {
			if law == "alaw" {
				c ^= 0x55
			} else {
				c = ^c
			}
			return c & 0x80, int(c & 0x7F)
		}
		off := 0
		for i := 0; i < 65536; i++ {
			v := int16(i - 32768)
			want := out[i]
			got := linearToULaw(v)
			if law == "alaw" {
				got = linearToALaw(v)
			}
			if got == want {
				continue
			}
			off++
			gs, gl := level(got)
			ws, wl := level(want)
			if gs != ws || gl-wl > 1 || wl-gl > 1 {
				t.Fatalf("%s(%d) = %#02x, ffmpeg %#02x", law, v, got, want)
			}
		}
		if off > 65536/50 {
			t.Fatalf("%s: %d values differ from FFmpeg", law, off)
		}
	}
}

func tonePCM16k(ms int) []byte {
	n := 16 * ms
	pcm := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(8000*math.Sin(2*math.Pi*440*float64(i)/16000))))
	}
	return pcm
}

func TestG711Encoder_Packets(t *testing.T) {
	out := make(chan talkPacket, 100)
	e := &g711Encoder{out: out}
	pcm := tonePCM16k(1000)
	// odd-sized writes, like the browser's
	for off := 0; off < len(pcm); off += 999 {
		if _, err := e.Write(pcm[off:min(off+999, len(pcm))]); err != nil {
			t.Fatal(err)
		}
	}
	if len(out) != 50 {
		t.Fatalf("%d packets for 1 s, want 50", len(out))
	}
	p := <-out
	if len(p.payload) != 160 || p.samples != 160 {
		t.Fatalf("packet %d bytes / %d samples", len(p.payload), p.samples)
	}
}

func TestPackAAC(t *testing.T) {
	frame := []byte{0xFF, 0xF1, 0x6C, 0x40, 0x01, 0x7F, 0xFC, 0xAA, 0xBB, 0xCC}
	if got := packAAC(frame, "adts"); !bytes.Equal(got, frame) {
		t.Fatalf("adts = %x", got)
	}
	if got := packAAC(frame, "raw"); !bytes.Equal(got, []byte{0xAA, 0xBB, 0xCC}) {
		t.Fatalf("raw = %x", got)
	}
	// RFC 3640 AAC-hbr: 16 bits of AU headers, size 3 << 3
	if got := packAAC(frame, "rfc3640"); !bytes.Equal(got, []byte{0x00, 0x10, 0x00, 0x18, 0xAA, 0xBB, 0xCC}) {
		t.Fatalf("rfc3640 = %x", got)
	}
}

func TestTalkFormats_UniqueAndDefault(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range TalkFormats() {
		if seen[f.ID] {
			t.Fatalf("duplicate %s", f.ID)
		}
		seen[f.ID] = true
		if f.samples == 0 || f.Label == "" {
			t.Fatalf("%s incomplete", f.ID)
		}
	}
	if !seen[DefaultTalkFormatID] {
		t.Fatal("default format missing")
	}
}

type captured struct {
	pt      uint8
	ssrc    uint32
	ts      uint32
	marker  bool
	payload []byte
	at      time.Time
}

type recordingWriter struct {
	mu   sync.Mutex
	pkts []captured
}

func (r *recordingWriter) WriteRTP(h *rtp.Header, payload []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pkts = append(r.pkts, captured{h.PayloadType, h.SSRC, h.Timestamp, h.Marker, append([]byte(nil), payload...), time.Now()})
	return len(payload), nil
}
func (r *recordingWriter) Write(b []byte) (int, error) { return len(b), nil }
func (r *recordingWriter) packets() []captured {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]captured(nil), r.pkts...)
}

type fakeTrackCtx struct {
	ws *recordingWriter
}

func (f fakeTrackCtx) CodecParameters() []webrtc.RTPCodecParameters {
	return []webrtc.RTPCodecParameters{{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: "audio/AAC", ClockRate: 8000},
		PayloadType:        96,
	}}
}
func (f fakeTrackCtx) HeaderExtensions() []webrtc.RTPHeaderExtensionParameter { return nil }
func (f fakeTrackCtx) SSRC() webrtc.SSRC                                      { return 4242 }
func (f fakeTrackCtx) SSRCRetransmission() webrtc.SSRC                        { return 0 }
func (f fakeTrackCtx) SSRCForwardErrorCorrection() webrtc.SSRC                { return 0 }
func (f fakeTrackCtx) WriteStream() webrtc.TrackLocalWriter                   { return f.ws }
func (f fakeTrackCtx) ID() string                                             { return "t" }
func (f fakeTrackCtx) RTCPReader() interceptor.RTCPReader                     { return nil }

func boundViewer(t *testing.T) (*Viewer, *recordingWriter) {
	t.Helper()
	tr, err := newTalkTrack()
	if err != nil {
		t.Fatal(err)
	}
	w := &recordingWriter{}
	if _, err := tr.Bind(fakeTrackCtx{ws: w}); err != nil {
		t.Fatal(err)
	}
	v := &Viewer{model: "t", doneChan: make(chan struct{}), audioTrack: tr}
	v.camAudio = camAudioInfo{sampleRate: 8000, channels: 1, clock: 8000, tsStep: 960}
	return v, w
}

// The payload type is the format's own, or the negotiated one; SSRC is the
// binding's either way.
func TestTalkTrack_PayloadTypePerFormat(t *testing.T) {
	v, w := boundViewer(t)
	for _, id := range []string{"pcmu", "pcma", "pcma-96", "aac-16k", "opus", "aac-adts"} {
		f, _ := TalkFormatByID(id)
		if err := v.writeTalkPacket(f, []byte{1, 2, 3}, 160, true); err != nil {
			t.Fatal(err)
		}
	}
	want := []uint8{0, 8, 96, 97, 111, 96}
	got := w.packets()
	for i, p := range got {
		if p.pt != want[i] || p.ssrc != 4242 {
			t.Fatalf("packet %d: pt %d ssrc %d, want pt %d ssrc 4242", i, p.pt, p.ssrc, want[i])
		}
	}
	if v.talkDelivered.Load() != int64(len(want)) {
		t.Fatalf("delivered %d", v.talkDelivered.Load())
	}
}

// G.711 talk needs no FFmpeg: 20 ms packets, paced, timestamps +160.
func TestTalkAudio_G711WithoutFFmpeg(t *testing.T) {
	v, w := boundViewer(t)
	if err := v.SetTalkFormat("pcma"); err != nil {
		t.Fatal(err)
	}
	if err := v.StartTalkAudio(""); err != nil {
		t.Fatal(err)
	}
	pcm := tonePCM16k(1000)
	for off := 0; off < len(pcm); off += 640 {
		_ = v.WriteTalkPCM(pcm[off:min(off+640, len(pcm))])
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	v.StopTalkAudio()
	pk := w.packets()
	if len(pk) < 45 {
		t.Fatalf("%d packets for 1 s", len(pk))
	}
	for i := 1; i < len(pk); i++ {
		if pk[i].ts-pk[i-1].ts != 160 || pk[i].pt != 8 || len(pk[i].payload) != 160 {
			t.Fatalf("packet %d: ts step %d pt %d len %d", i, pk[i].ts-pk[i-1].ts, pk[i].pt, len(pk[i].payload))
		}
	}
	if !pk[0].marker || pk[1].marker {
		t.Fatal("G.711 marker should be set only on the first packet of a talkspurt")
	}
	span := pk[len(pk)-1].at.Sub(pk[0].at)
	if avg := span / time.Duration(len(pk)-1); avg < 15*time.Millisecond || avg > 30*time.Millisecond {
		t.Fatalf("average spacing %v, want ~20ms", avg)
	}
}

// Opus via FFmpeg: Ogg pages parsed into 20 ms packets on payload type 111.
func TestTalkAudio_OpusViaFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	v, w := boundViewer(t)
	_ = v.SetTalkFormat("opus")
	if err := v.StartTalkAudio(ffmpeg); err != nil {
		t.Fatal(err)
	}
	pcm := tonePCM16k(1000)
	for off := 0; off < len(pcm); off += 640 {
		_ = v.WriteTalkPCM(pcm[off:min(off+640, len(pcm))])
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(800 * time.Millisecond)
	v.StopTalkAudio()
	pk := w.packets()
	if len(pk) < 35 {
		t.Fatalf("%d Opus packets for 1 s", len(pk))
	}
	for i, p := range pk {
		if p.pt != 111 || isOpusHeader(p.payload) || len(p.payload) == 0 {
			t.Fatalf("packet %d: pt %d len %d", i, p.pt, len(p.payload))
		}
		if i > 0 && p.ts-pk[i-1].ts != 960 {
			t.Fatalf("packet %d: ts step %d", i, p.ts-pk[i-1].ts)
		}
	}
}

// AAC mirrors the camera: its RTP step (960 here), marker on every packet;
// the raw and RFC 3640 packings carry no ADTS header.
func TestTalkAudio_AACPackings(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	for _, id := range []string{"aac-adts", "aac-raw", "aac-rfc3640", "aac-16k"} {
		v, w := boundViewer(t)
		_ = v.SetTalkFormat(id)
		if err := v.StartTalkAudio(ffmpeg); err != nil {
			t.Fatal(err)
		}
		pcm := tonePCM16k(1000)
		for off := 0; off < len(pcm); off += 2048 {
			_ = v.WriteTalkPCM(pcm[off:min(off+2048, len(pcm))])
			time.Sleep(64 * time.Millisecond)
		}
		time.Sleep(700 * time.Millisecond)
		v.StopTalkAudio()
		pk := w.packets()
		if len(pk) < 5 {
			t.Fatalf("%s: %d packets", id, len(pk))
		}
		wantStep := uint32(960)
		if id == "aac-16k" {
			wantStep = 1024
		}
		for i, p := range pk {
			adts := len(p.payload) > 1 && p.payload[0] == 0xFF && p.payload[1]&0xF0 == 0xF0
			switch id {
			case "aac-adts", "aac-16k":
				if !adts {
					t.Fatalf("%s packet %d: no ADTS header", id, i)
				}
			case "aac-raw":
				if adts {
					t.Fatalf("%s packet %d: ADTS header left in", id, i)
				}
			case "aac-rfc3640":
				if p.payload[0] != 0 || p.payload[1] != 0x10 {
					t.Fatalf("%s packet %d: AU header %x", id, i, p.payload[:2])
				}
				size := int(p.payload[2])<<5 | int(p.payload[3])>>3
				if size != len(p.payload)-4 {
					t.Fatalf("%s packet %d: AU size %d, payload %d", id, i, size, len(p.payload)-4)
				}
			}
			if !p.marker {
				t.Fatalf("%s packet %d: marker off", id, i)
			}
			if i > 0 && p.ts-pk[i-1].ts != wantStep {
				t.Fatalf("%s packet %d: ts step %d, want %d", id, i, p.ts-pk[i-1].ts, wantStep)
			}
		}
	}
}

type fakeTalkCC struct {
	ControlChannel
	mu   sync.Mutex
	arms []bool
}

func (f *fakeTalkCC) ArmTalk(ctx context.Context, uuid string, enable bool, sid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.arms = append(f.arms, enable)
	return nil
}

// Other viewers' answers and talk replies are described without session IDs.
func TestForeignDetail(t *testing.T) {
	d := foreignDetail("response.SdpAnswer", map[string]any{"SessionId": "usfX:e56:abc", "WebrtcSdp": `00` + "\r\n" + `{"audio":{"pt":97,"pts":16000}}`})
	if d != `camera audio section for that viewer: {"pt":97,"pts":16000}` {
		t.Fatalf("answer detail %q", d)
	}
	d = foreignDetail("response.TalkResp", map[string]any{"SessionId": "usfX:e56:abc", "call_id": "1", "enableSpeakerRet": 0.0, "enableMicRet": 0.0})
	if d != "talk reply for that viewer: enableMicRet=0 enableSpeakerRet=0" {
		t.Fatalf("talk detail %q", d)
	}
}

// Talk reaches the camera promptly: FFmpeg must not read seconds of the
// pipe before its first output (it did until -probesize/-analyzeduration).
func TestTalkAudio_FirstPacketQuickly(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	v, w := boundViewer(t)
	if err := v.StartTalkAudio(ffmpeg); err != nil {
		t.Fatal(err)
	}
	defer v.StopTalkAudio()
	start := time.Now()
	pcm := tonePCM16k(3000)
	for off := 0; off < len(pcm); off += 640 {
		_ = v.WriteTalkPCM(pcm[off:min(off+640, len(pcm))])
		time.Sleep(20 * time.Millisecond)
		if pk := w.packets(); len(pk) > 0 {
			if d := pk[0].at.Sub(start); d > 900*time.Millisecond {
				t.Fatalf("first talk packet after %v", d)
			}
			return
		}
	}
	t.Fatal("no talk packet within 3 s")
}

// The TURN client's permission-refresh error is logged once, not every 2 min.
func TestTurnLogFilter(t *testing.T) {
	f := newPionLoggerFactory("t")
	l := f.NewLogger("turnc").(*turnLogFilter)
	if !l.quiet("Fail to refresh permissions: error 400") || !l.quiet("Failed to refresh permissions: x") {
		t.Fatal("refresh-permission messages should be quieted")
	}
	if l.quiet("some other TURN error") {
		t.Fatal("other messages must still be logged")
	}
	if _, ok := f.NewLogger("ice").(*turnLogFilter); ok {
		t.Fatal("only the TURN client is filtered")
	}
}

// A pause in the browser's upload, followed by the backlog all at once, no
// longer leaves Talk that far behind for the rest of the session: the stale
// voice is skipped. (Before, 1.5 s of pause kept Talk about 1.5 s late.)
func TestTalkAudio_PauseDoesNotLeaveTalkBehind(t *testing.T) {
	ffmpeg, _ := exec.LookPath("ffmpeg")
	for _, id := range []string{"pcmu", "aac-adts"} {
		f, _ := TalkFormatByID(id)
		if f.NeedsFFmpeg() && ffmpeg == "" {
			t.Logf("%s: ffmpeg not installed, skipped", id)
			continue
		}
		t.Run(id, func(t *testing.T) {
			v, w := boundViewer(t)
			if err := v.SetTalkFormat(id); err != nil {
				t.Fatal(err)
			}
			if err := v.StartTalkAudio(ffmpeg); err != nil {
				t.Fatal(err)
			}
			defer v.StopTalkAudio()
			pcm := tonePCM16k(4000)
			off := 0
			realTime := func(ms int) {
				start := time.Now()
				for i := 1; i <= ms/20; i++ {
					_ = v.WriteTalkPCM(pcm[off : off+640])
					off += 640
					time.Sleep(time.Until(start.Add(time.Duration(i) * 20 * time.Millisecond)))
				}
			}
			realTime(800)
			time.Sleep(1500 * time.Millisecond) // the upload stalls...
			_ = v.WriteTalkPCM(pcm[off : off+75*640])
			off += 75 * 640 // ...then delivers its 1.5 s backlog at once
			realTime(1200)
			lastVoice := time.Now()
			time.Sleep(1500 * time.Millisecond)
			pk := w.packets()
			if len(pk) == 0 {
				t.Fatal("no packets")
			}
			if late := pk[len(pk)-1].at.Sub(lastVoice); late > 450*time.Millisecond {
				t.Fatalf("the last voice went out %v after it arrived; the pause stayed as delay", late)
			}
			for i := 1; i < len(pk); i++ {
				if d := int32(pk[i].ts - pk[i-1].ts); d <= 0 {
					t.Fatalf("packet %d: RTP time stepped %d", i, d)
				}
			}
		})
	}
}

// Talk keeps pace with the WS03's audio clock: it plays one frame per 120 ms
// (RTP step 960 at 8 kHz), so frames must carry 120 ms of voice and go out
// every 120 ms; otherwise the camera falls further behind with every frame
// and the voice cuts out.
func TestTalkAudio_KeepsPaceWithCameraClock(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	v, w := boundViewer(t) // WS03: 8 kHz, clock 8000, step 960
	if err := v.StartTalkAudio(ffmpeg); err != nil {
		t.Fatal(err)
	}
	pcm := tonePCM16k(5000)
	start := time.Now()
	for off, i := 0, 1; off < len(pcm); off, i = off+640, i+1 {
		_ = v.WriteTalkPCM(pcm[off:min(off+640, len(pcm))])
		time.Sleep(time.Until(start.Add(time.Duration(i) * 20 * time.Millisecond)))
	}
	time.Sleep(300 * time.Millisecond)
	v.StopTalkAudio()
	pk := w.packets()
	if len(pk) < 30 {
		t.Fatalf("%d packets", len(pk))
	}
	// skip the start (encoder warm-up), compare RTP time with wall time
	a, b := pk[5], pk[len(pk)-1]
	rtpSecs := float64(b.ts-a.ts) / 8000
	wallSecs := b.at.Sub(a.at).Seconds()
	if r := rtpSecs / wallSecs; r < 0.97 || r > 1.03 {
		t.Fatalf("RTP clock runs at %.3f x real time over %.1f s (%d packets)", r, wallSecs, len(pk))
	}
	// and the encoder keeps up: about one frame per 120 ms of input
	if n := len(pk); n < 36 || n > 44 {
		t.Fatalf("%d frames for 5 s of voice, want about 41", n)
	}
}
