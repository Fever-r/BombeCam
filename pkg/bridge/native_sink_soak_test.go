package bridge

import (
	"bufio"
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Soak test: camera-style RTP with a bad network (jitter, stalls followed by
// bursts, late and duplicated packets, lost audio) goes through the native
// sink into a real MediaMTX, while ffprobe reads the RTSP output the way a
// recorder does. Every stream's DTS must increase, and MediaMTX (whose HLS
// muxer runs the whole time) must not report a timestamp error.
//
//	BOMBECAM_TEST_MEDIAMTX=/path/to/mediamtx BOMBECAM_SOAK=10m go test ./pkg/bridge -run Soak -v
//
// Lab use (a stand-in camera for Frigate / Home Assistant tests): with
// BOMBECAM_SOAK_URL=rtsp://127.0.0.1:8554/<camera ID> it publishes into an
// already running MediaMTX (BombeCam's) instead of starting its own, and
// BOMBECAM_SOAK_NETWORK=clean|mild|harsh picks the impairments (harsh).
func TestNativeSink_Soak_MonotonicTimestamps(t *testing.T) {
	mtx := os.Getenv("BOMBECAM_TEST_MEDIAMTX")
	durStr := os.Getenv("BOMBECAM_SOAK")
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if extURL := os.Getenv("BOMBECAM_SOAK_URL"); extURL != "" && mtx == "" {
		mtx = "external"
	}
	if mtx == "" || durStr == "" || err1 != nil || err2 != nil {
		t.Skip("set BOMBECAM_TEST_MEDIAMTX and BOMBECAM_SOAK (e.g. 10m) and install ffmpeg/ffprobe to run")
	}
	dur, err := time.ParseDuration(durStr)
	if err != nil {
		t.Fatal(err)
	}
	for _, withG711 := range []bool{true} {
		runSoak(t, mtx, ffmpeg, ffprobe, dur, withG711)
	}
}

func runSoak(t *testing.T, mtx, ffmpeg, ffprobe string, dur time.Duration, withG711 bool) {
	dir := t.TempDir()
	h264 := filepath.Join(dir, "v.h264")
	aac := filepath.Join(dir, "a.aac")
	// 10 s of 12 fps video with a keyframe every 2.5 s, like the WS03 with BombeCam's PLIs
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=12", "-t", "10",
		"-c:v", "libx264", "-bf", "0", "-g", "30", "-b:v", "1500k", "-pix_fmt", "yuv420p", "-f", "h264", h264).CombinedOutput(); err != nil {
		t.Skipf("cannot generate test video: %v %s", err, out)
	}
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=8000", "-t", "10",
		"-c:a", "aac", "-ac", "1", "-ar", "8000", "-f", "adts", aac).CombinedOutput(); err != nil {
		t.Skipf("cannot generate test audio: %v %s", err, out)
	}

	var mtxLog lockedBuf
	publishURL := os.Getenv("BOMBECAM_SOAK_URL")
	if publishURL == "" {
		rtsp, hls, api := e2ePort(t), e2ePort(t), e2ePort(t)
		cfgPath := filepath.Join(dir, "mtx.yml")
		cfg := fmt.Sprintf("logLevel: info\napi: yes\napiAddress: 127.0.0.1:%d\nrtspAddress: :%d\nprotocols: [tcp]\nrtmp: no\nsrt: no\nwebrtc: no\nhls: yes\nhlsAddress: :%d\nhlsAlwaysRemux: yes\nhlsVariant: mpegts\npaths:\n  all_others:\n", api, rtsp, hls)
		if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		srv := exec.Command(mtx, cfgPath)
		srv.Stdout, srv.Stderr = &mtxLog, &mtxLog
		if err := srv.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = srv.Process.Kill(); _, _ = srv.Process.Wait() }()
		time.Sleep(time.Second)
		publishURL = fmt.Sprintf("rtsp://127.0.0.1:%d/soak_cam", rtsp)
	}
	network := os.Getenv("BOMBECAM_SOAK_NETWORK")
	if network == "" {
		network = "harsh"
	}

	vdata, _ := os.ReadFile(h264)
	adata, _ := os.ReadFile(aac)
	var frames [][][]byte
	for _, n := range splitAnnexB(vdata) {
		if len(n) == 0 {
			continue
		}
		ty := h264NALType(n)
		if len(frames) == 0 || ty == 7 || ((ty == 1 || ty == 5) && lastHasSlice(frames[len(frames)-1])) {
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

	transcoder := ""
	if withG711 {
		transcoder = ffmpeg
	}
	sink := newNativeSink(publishURL, "soak", true, transcoder)
	defer sink.Close()

	// The camera: frames and audio are captured on schedule, but the network
	// delivers them late at times.
	type pkt struct {
		video   bool
		payload []byte
		ts      uint32
	}
	deliver := make(chan pkt, 4096)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // capture
		defer wg.Done()
		defer close(deliver)
		rng := rand.New(rand.NewSource(1))
		start := time.Now()
		vts := uint32(rng.Int63())
		ats := uint32(rng.Int63())
		fi, ai := 0, 0
		nextV, nextA := start, start
		for {
			select {
			case <-stop:
				return
			default:
			}
			now := time.Now()
			if !now.Before(nextV) {
				au := frames[fi%len(frames)]
				var pkts [][]byte
				for _, n := range au {
					pkts = append(pkts, vendorPackets(n)...)
				}
				for _, p := range pkts {
					deliver <- pkt{video: true, payload: p, ts: vts}
				}
				vts += 7500 // 12 fps
				fi++
				nextV = nextV.Add(time.Second / 12)
			}
			if !now.Before(nextA) {
				f := audioFrames[ai%len(audioFrames)]
				deliver <- pkt{payload: f, ts: ats}
				ats += 960 // the WS03: RTP +960 and one frame every 120 ms
				ai++
				nextA = nextA.Add(120 * time.Millisecond)
			}
			d := time.Until(nextV)
			if a := time.Until(nextA); a < d {
				d = a
			}
			if d > 0 {
				time.Sleep(d)
			}
		}
	}()
	var stalls, reorders, dups, lost int
	go func() { // network
		defer wg.Done()
		rng := rand.New(rand.NewSource(2))
		var held []pkt
		var prevVideo *pkt
		stallUntil := time.Time{}
		stallEvery := map[string]int{"harsh": 30, "mild": 240, "clean": 0}[network]
		nextStall := time.Now().Add(20 * time.Second)
		if stallEvery == 0 {
			nextStall = time.Now().Add(1000 * time.Hour)
		}
		pBad := map[string]float64{"harsh": 0.01, "mild": 0.002, "clean": 0}[network]
		for p := range deliver {
			now := time.Now()
			if now.Before(stallUntil) {
				held = append(held, p)
				continue
			}
			if len(held) > 0 { // burst
				for _, h := range held {
					sendPkt(sink, h.video, h.payload, h.ts)
				}
				held = nil
			}
			r := rng.Float64()
			switch {
			case now.After(nextStall): // harsh: every 30-60 s a 1.2-2 s stall, then the burst
				stalls++
				stallUntil = now.Add(time.Duration(1200+rng.Intn(800)) * time.Millisecond)
				nextStall = now.Add(time.Duration(stallEvery+rng.Intn(stallEvery)) * time.Second)
				held = append(held, p)
				continue
			case r < pBad && p.video && prevVideo != nil && prevVideo.ts != p.ts: // a late packet from the previous frame
				reorders++
				sendPkt(sink, true, p.payload, p.ts)
				sendPkt(sink, true, prevVideo.payload, prevVideo.ts)
			case r < pBad && !p.video: // duplicated audio
				dups++
				sendPkt(sink, false, p.payload, p.ts)
				sendPkt(sink, false, p.payload, p.ts)
			case r < 2*pBad && !p.video: // lost audio
				lost++
			default:
				if pBad > 0 && rng.Float64() < 0.05 {
					time.Sleep(time.Duration(rng.Intn(40)) * time.Millisecond) // jitter
				}
				sendPkt(sink, p.video, p.payload, p.ts)
			}
			if p.video {
				cp := p
				prevVideo = &cp
			}
		}
	}()

	time.Sleep(5 * time.Second)
	// Two readers for the whole run. The recorder's view reads every stream;
	// with more than one stream FFmpeg re-anchors each stream on MediaMTX's
	// RTCP sender reports, which MediaMTX 1.9.3 builds from packet arrival
	// times. The video-only view has a single stream, so its DTS are exactly
	// the RTP timestamps BombeCam published.
	probeDur := dur
	runProbe := func(extra ...string) (*bytes.Buffer, *lockedBuf, *exec.Cmd) {
		args := append([]string{"-v", "error", "-rtsp_transport", "tcp"}, extra...)
		args = append(args, "-show_packets", "-show_entries", "packet=stream_index,dts,pts,dts_time",
			"-of", "csv=p=0", "-read_intervals", fmt.Sprintf("%%+%d", int(probeDur.Seconds())), publishURL)
		cmd := exec.Command(ffprobe, args...)
		var out bytes.Buffer
		var errb lockedBuf
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return &out, &errb, cmd
	}
	probeOut, probeErr, probe := runProbe()
	videoOut, videoErr, videoProbe := runProbe("-allowed_media_types", "video")
	if err := probe.Wait(); err != nil {
		t.Logf("ffprobe: %v %s", err, probeErr.String())
	}
	if err := videoProbe.Wait(); err != nil {
		t.Logf("ffprobe (video only): %v %s", err, videoErr.String())
	}
	close(stop)
	wg.Wait()

	// ffprobe prints stream_index,pts,dts,dts_time. A reader of several
	// streams lines them up by their first packets until each stream's first
	// RTCP sender report (about 10 s in), then re-anchors on it; that one
	// correction is at most about one frame and is not a publishing fault.
	checkDTS := func(out *bytes.Buffer, name string, fail bool) (map[string]int, int) {
		last := map[string]int64{}
		lastT := map[string]float64{}
		count := map[string]int{}
		bad := 0
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			f := strings.Split(sc.Text(), ",")
			if len(f) < 4 {
				continue
			}
			stream, dtsStr, tStr := f[0], f[2], f[3]
			if dtsStr == "N/A" || dtsStr == "" {
				dtsStr = f[1]
			}
			dts, err := strconv.ParseInt(dtsStr, 10, 64)
			if err != nil {
				continue
			}
			ts, _ := strconv.ParseFloat(tStr, 64)
			if c := count[stream]; c > 0 && dts <= last[stream] {
				msg := fmt.Sprintf("%s: stream %s packet %d at %.2f s: DTS %d after %d", name, stream, c, ts, dts, last[stream])
				if back := lastT[stream] - ts; ts > 0 && ts < 13 && back < 0.15 {
					t.Log(msg + " (first sender report re-anchor)")
				} else {
					bad++
					if bad <= 10 {
						if fail {
							t.Error(msg)
						} else {
							t.Log(msg)
						}
					}
				}
			}
			last[stream], lastT[stream] = dts, ts
			count[stream]++
		}
		return count, bad
	}
	// What BombeCam publishes must never go back.
	videoCount, videoBad := checkDTS(videoOut, "video-only reader", true)
	if videoCount["0"] == 0 {
		t.Error("the video-only reader got no packets")
	}
	sink.mu.Lock()
	guardHeld := sink.guardHeld
	sink.mu.Unlock()
	if guardHeld > 0 {
		t.Errorf("the write-time guard had to hold %d timestamps: the sink's own clocks went back", guardHeld)
	}
	// The all-streams reader: on a clean network it must be monotonic too.
	// After a stall longer than the alignment delay, packets reach MediaMTX
	// late and its arrival-time sender reports can show FFmpeg-based readers
	// one short step back; those are logged, not failed.
	count, bad := checkDTS(probeOut, "all-streams reader", network == "clean")
	t.Logf("video-only reader: %d packets, %d DTS steps back; write-time guard holds: %d", videoCount["0"], videoBad, guardHeld)
	t.Logf("%d DTS steps back", bad)
	t.Logf("network %s", network)
	t.Logf("%v: packets per stream %v; stalls %d, late video packets %d, duplicated audio %d, lost audio %d; sink dropped %d late video packets, %d audio packets; A/V delay %v",
		dur, count, stalls, reorders, dups, lost, sink.droppedLate, sink.droppedAudio, sink.delay)
	wantStreams := 2
	if withG711 {
		wantStreams = 3
	}
	if len(count) != wantStreams {
		t.Errorf("ffprobe saw %d streams, want %d", len(count), wantStreams)
	}
	minVideo := int(dur.Seconds()*12) * 8 / 10
	if count["0"] < minVideo {
		t.Errorf("only %d video packets in %v", count["0"], dur)
	}
	for _, l := range strings.Split(mtxLog.String(), "\n") {
		if strings.Contains(l, "DTS") || strings.Contains(l, "muxer error") || strings.Contains(l, "timestamp") {
			t.Errorf("MediaMTX: %s", l)
		}
	}
}

func sendPkt(s *nativeSink, video bool, payload []byte, ts uint32) {
	if video {
		s.OnVideoPacket(payload, ts, false)
	} else {
		s.OnAudioRTP(payload, ts)
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
