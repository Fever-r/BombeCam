package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// splitAnnexB splits an H.264 Annex-B byte stream into NAL units.
func splitAnnexB(b []byte) [][]byte {
	var out [][]byte
	start := -1
	for i := 0; i+3 <= len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && (b[i+2] == 1 || (i+3 < len(b) && b[i+2] == 0 && b[i+3] == 1)) {
			sc := 3
			if b[i+2] == 0 {
				sc = 4
			}
			if start >= 0 {
				out = append(out, bytes.TrimRight(b[start:i], "\x00"))
			}
			start = i + sc
			i += sc - 1
		}
	}
	if start >= 0 && start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

// vendorPackets packetizes a NAL the way the camera does (see nalAssembler):
// large slices as type-49 fragmentation units, everything else as-is.
func vendorPackets(nal []byte) [][]byte {
	t := h264NALType(nal)
	if (t != 5 && t != 1) || len(nal) < 900 {
		return [][]byte{nal}
	}
	fuType := byte(1)
	if t == 5 {
		fuType = 19
	}
	body := nal[2:]
	var out [][]byte
	for off := 0; off < len(body); off += 1000 {
		end := off + 1000
		if end > len(body) {
			end = len(body)
		}
		fu := fuType
		if off == 0 {
			fu |= 0x80
		}
		if end == len(body) {
			fu |= 0x40
		}
		out = append(out, append([]byte{49 << 1, nal[1], fu}, body[off:end]...))
	}
	return out
}

func e2ePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// End to end: camera-style RTP -> nativeSink -> MediaMTX -> RTSP/HLS readers.
// Needs a MediaMTX binary (BOMBECAM_TEST_MEDIAMTX) plus ffmpeg/ffprobe on PATH.
func TestNativeSink_PublishesPlayableStreamToMediaMTX(t *testing.T) {
	mtx := os.Getenv("BOMBECAM_TEST_MEDIAMTX")
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if mtx == "" || err1 != nil || err2 != nil {
		t.Skip("set BOMBECAM_TEST_MEDIAMTX and install ffmpeg/ffprobe to run")
	}
	t.Run("aac-only", func(t *testing.T) { runNativeSinkE2E(t, mtx, ffmpeg, ffprobe, "") })
	// with FFmpeg available, a G.711 copy of the sound is added for WebRTC viewers
	t.Run("with-browser-audio", func(t *testing.T) { runNativeSinkE2E(t, mtx, ffmpeg, ffprobe, ffmpeg) })
}

func runNativeSinkE2E(t *testing.T, mtx, ffmpeg, ffprobe, transcoder string) {
	dir := t.TempDir()
	h264 := filepath.Join(dir, "v.h264")
	aac := filepath.Join(dir, "a.aac")
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=15", "-t", "8",
		"-c:v", "libx264", "-bf", "0", "-g", "15", "-pix_fmt", "yuv420p", "-f", "h264", h264).CombinedOutput(); err != nil {
		t.Skipf("cannot generate test video: %v %s", err, out)
	}
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=8000", "-t", "8",
		"-c:a", "aac", "-ac", "1", "-ar", "8000", "-f", "adts", aac).CombinedOutput(); err != nil {
		t.Skipf("cannot generate test audio: %v %s", err, out)
	}

	rtsp, hls, api := e2ePort(t), e2ePort(t), e2ePort(t)
	cfgPath := filepath.Join(dir, "mtx.yml")
	cfg := fmt.Sprintf("logLevel: warn\napi: yes\napiAddress: 127.0.0.1:%d\nrtspAddress: :%d\nprotocols: [tcp]\nrtmp: no\nsrt: no\nwebrtc: no\nhls: yes\nhlsAddress: :%d\nhlsAlwaysRemux: yes\nhlsVariant: mpegts\npaths:\n  all_others:\n", api, rtsp, hls)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := exec.Command(mtx, cfgPath)
	srv.Stdout, srv.Stderr = os.Stdout, os.Stderr
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Process.Kill(); _, _ = srv.Process.Wait() }()
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", rtsp)); err == nil {
			c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	vdata, _ := os.ReadFile(h264)
	adata, _ := os.ReadFile(aac)
	nals := splitAnnexB(vdata)
	var frames [][][]byte // access units
	for _, n := range nals {
		if len(n) == 0 {
			continue
		}
		t := h264NALType(n)
		if len(frames) == 0 || t == 7 || ((t == 1 || t == 5) && lastHasSlice(frames[len(frames)-1])) {
			frames = append(frames, nil)
		}
		frames[len(frames)-1] = append(frames[len(frames)-1], n)
	}
	var audioFrames [][]byte
	for b := adata; len(b) >= 7; {
		fl := (int(b[3]&0x03) << 11) | (int(b[4]) << 3) | (int(b[5]&0xE0) >> 5)
		if fl < 7 || fl > len(b) {
			break
		}
		audioFrames = append(audioFrames, b[:fl])
		b = b[fl:]
	}

	path := "e2e_cam"
	sink := newNativeSink(fmt.Sprintf("rtsp://127.0.0.1:%d/%s", rtsp, path), "e2e", true, transcoder)
	defer sink.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		start := time.Now()
		ai := 0
		for fi, au := range frames {
			ts := uint32(123456 + fi*6000) // 15 fps at 90 kHz
			var pkts [][]byte
			for _, n := range au {
				pkts = append(pkts, vendorPackets(n)...)
			}
			for pi, p := range pkts {
				sink.OnVideoPacket(p, ts, pi == len(pkts)-1)
			}
			// audio: 8000 Hz / 1024 samples = 7.8 frames per second, sent in pairs
			for float64(ai)*1024/8000 < float64(fi+1)/15 && ai < len(audioFrames) {
				end := ai + 2
				if end > len(audioFrames) {
					end = len(audioFrames)
				}
				sink.OnAudioADTS(bytes.Join(audioFrames[ai:end], nil))
				ai = end
			}
			time.Sleep(time.Until(start.Add(time.Duration(fi+1) * time.Second / 15)))
		}
	}()

	time.Sleep(4 * time.Second)
	// while publishing, the path must be ready with both tracks
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v3/paths/get/%s", api, path))
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		Ready  bool     `json:"ready"`
		Tracks []string `json:"tracks"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	wantTracks := 2
	if transcoder != "" {
		wantTracks = 3
	}
	if !info.Ready || len(info.Tracks) != wantTracks {
		t.Fatalf("path not ready with %d tracks: %+v", wantTracks, info)
	}
	// an RTSP consumer can decode it (dimensions come from decoded SPS/frames)
	out, err := exec.Command(ffprobe, "-v", "error", "-rtsp_transport", "tcp", "-show_entries", "stream=codec_name,width,height,sample_rate",
		"-of", "compact", fmt.Sprintf("rtsp://127.0.0.1:%d/%s", rtsp, path)).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "codec_name=h264") || !strings.Contains(string(out), "width=640") || !strings.Contains(string(out), "codec_name=aac") {
		t.Fatalf("ffprobe rtsp: %v\n%s", err, out)
	}
	if transcoder != "" && !strings.Contains(string(out), "codec_name=pcm_mulaw") {
		t.Fatalf("ffprobe rtsp: no G.711 track\n%s", out)
	}
	// the video-only viewer copy (muted live view): ready, video alone
	resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/v3/paths/get/%s/viewer", api, path))
	if err != nil {
		t.Fatal(err)
	}
	info.Ready, info.Tracks = false, nil
	_ = json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	if !info.Ready || len(info.Tracks) != 1 || !sink.ViewerCopyReady() {
		t.Fatalf("viewer copy not ready with one track: %+v (sink says %v)", info, sink.ViewerCopyReady())
	}
	out, err = exec.Command(ffprobe, "-v", "error", "-rtsp_transport", "tcp", "-show_entries", "stream=codec_name,width",
		"-of", "compact", fmt.Sprintf("rtsp://127.0.0.1:%d/%s/viewer", rtsp, path)).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "codec_name=h264") || strings.Contains(string(out), "aac") {
		t.Fatalf("ffprobe viewer copy: %v\n%s", err, out)
	}
	// the HLS muxer produces a playlist
	resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/%s/index.m3u8", hls, path))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "#EXTM3U") {
		t.Fatalf("hls playlist: %d %s", resp.StatusCode, body)
	}
	<-done
	time.Sleep(time.Second) // let the A/V alignment queue drain
	sink.mu.Lock()
	published, delay := sink.published, sink.delay
	sink.mu.Unlock()
	if published < int64(len(frames)-5) {
		t.Fatalf("published %d of %d access units", published, len(frames))
	}
	if delay <= 0 || delay > 600*time.Millisecond {
		t.Fatalf("unexpected A/V alignment delay %v", delay)
	}
}

func lastHasSlice(au [][]byte) bool {
	for _, n := range au {
		if t := h264NALType(n); t == 1 || t == 5 {
			return true
		}
	}
	return false
}
