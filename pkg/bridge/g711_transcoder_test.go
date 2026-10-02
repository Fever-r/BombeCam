package bridge

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type g711Chunk struct {
	samples []byte
	ts      int64
}

// muLawToLinear decodes one G.711 µ-law sample.
func muLawToLinear(u byte) int {
	u = ^u
	sign := u & 0x80
	exp := (u >> 4) & 0x07
	mant := int(u & 0x0f)
	v := ((mant << 3) + 0x84) << exp
	v -= 0x84
	if sign != 0 {
		return -v
	}
	return v
}

func adtsFrames(t *testing.T, ffmpeg string, rate string, seconds string) [][]byte {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.aac")
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate="+rate, "-t", seconds,
		"-c:a", "aac", "-ac", "1", "-ar", rate, "-f", "adts", p).CombinedOutput(); err != nil {
		t.Skipf("cannot generate test audio: %v %s", err, out)
	}
	data, _ := os.ReadFile(p)
	var frames [][]byte
	for b := data; len(b) >= 7; {
		fl := (int(b[3]&0x03) << 11) | (int(b[4]) << 3) | (int(b[5]&0xE0) >> 5)
		if fl < 7 || fl > len(b) {
			break
		}
		frames = append(frames, b[:fl])
		b = b[fl:]
	}
	return frames
}

// The G.711 copy must carry the same timeline as the AAC it was decoded from:
// contiguous 8 kHz timestamps that follow the input's timestamps (including a
// gap), with the actual sound intact.
func TestG711Transcoder_TimestampsFollowInput(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	for _, rate := range []int{8000, 16000} {
		frames := adtsFrames(t, ffmpeg, map[int]string{8000: "8000", 16000: "16000"}[rate], "4")
		var mu sync.Mutex
		var got []g711Chunk
		tr, err := startG711Transcoder(ffmpeg, rate, 1024, func(s []byte, ts int64) {
			mu.Lock()
			got = append(got, g711Chunk{s, ts})
			mu.Unlock()
		})
		if err != nil {
			t.Fatal(err)
		}
		const base = int64(1) << 40 // far beyond 32 bits: nothing may truncate
		gapAt := len(frames) / 2
		gap := int64(rate) // one second of silence in the timeline
		fed := 0
		for i := 0; i < len(frames); i += 2 {
			end := i + 2
			if end > len(frames) {
				end = len(frames)
			}
			ts := base + int64(i*1024)
			if i >= gapAt {
				ts += gap
			}
			var chunk []byte
			for _, f := range frames[i:end] {
				chunk = append(chunk, f...)
			}
			tr.Feed(chunk, end-i, ts, rate)
			fed += (end - i) * 1024 * 8000 / rate
			time.Sleep(time.Duration(end-i) * 1024 * time.Second / time.Duration(rate) / 4)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := 0
			for _, c := range got {
				n += len(c.samples)
			}
			mu.Unlock()
			if n >= fed-2*1024 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		tr.Close()
		mu.Lock()
		chunks := append([]g711Chunk(nil), got...)
		mu.Unlock()

		total, jumps := 0, 0
		var sumSq float64
		first8 := base * 8000 / int64(rate)
		if len(chunks) == 0 {
			t.Fatalf("rate %d: no output", rate)
		}
		// output starts at the first frame (or, if FFmpeg needed a moment
		// to start, later, skipping the backlog: on a busy machine FFmpeg
		// can take a second or two to start)
		if d := chunks[0].ts - first8; d < 0 || d > 16*1024 || d%160 != 0 {
			t.Fatalf("rate %d: first G.711 timestamp %d, want %d (+ whole packets)", rate, chunks[0].ts, first8)
		}
		fed -= int(chunks[0].ts - first8)
		for i, c := range chunks {
			total += len(c.samples)
			for _, u := range c.samples {
				v := float64(muLawToLinear(u))
				sumSq += v * v
			}
			if i == 0 {
				continue
			}
			prev := chunks[i-1]
			want := prev.ts + int64(len(prev.samples))
			if c.ts != want {
				jump := c.ts - want
				if jump < 7900 || jump > 8100 || jumps > 0 {
					t.Fatalf("rate %d: chunk %d ts %d, want %d (+gap 8000 at most once), jump %d", rate, i, c.ts, want, jump)
				}
				jumps++
			}
		}
		if jumps != 1 {
			t.Fatalf("rate %d: expected the one-second gap to show up once, saw %d", rate, jumps)
		}
		if d := total - fed; d > 1024 || d < -2*1024 {
			t.Fatalf("rate %d: produced %d samples for %d fed", rate, total, fed)
		}
		if rms := math.Sqrt(sumSq / float64(total)); rms < 1000 {
			t.Fatalf("rate %d: decoded audio is silent (rms %.0f)", rate, rms)
		}
	}
}

// Feed must never block the caller, even if FFmpeg stops reading.
func TestG711Transcoder_FeedNeverBlocks(t *testing.T) {
	tr := &g711Transcoder{in: make(chan []byte, 1)}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			tr.Feed([]byte{1, 2, 3}, 1, int64(i*1024), 8000)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Feed blocked")
	}
	if len(tr.marks) != 1 {
		t.Fatalf("dropped chunks must not be timestamped: %d marks", len(tr.marks))
	}
}

// The WS03 sends one AAC frame every 120 ms (RTP step 960) although decoders
// turn each into 1024 samples. The browser copy is resampled to 960 samples
// per frame: timestamps stay contiguous (nothing dropped for overlap), the
// amount of audio matches real time, and it does not fall behind.
func TestG711Transcoder_960SampleFrames(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if args, out := g711Args(8000, 960); out != 960 || !strings.Contains(strings.Join(args, " "), "aresample=7500,asetrate=8000") {
		t.Fatalf("args %v out %d", args, out)
	}
	frames := adtsFrames(t, ffmpeg, "8000", "6")
	var mu sync.Mutex
	var got []g711Chunk
	var arrived []time.Time
	tr, err := startG711Transcoder(ffmpeg, 8000, 960, func(s []byte, ts int64) {
		mu.Lock()
		got = append(got, g711Chunk{s, ts})
		arrived = append(arrived, time.Now())
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	const base = int64(5_000_000)
	var fedAt []time.Time
	for i, f := range frames {
		fedAt = append(fedAt, time.Now())
		tr.Feed(f, 1, base+int64(i*960), 8000)
		time.Sleep(120 * time.Millisecond) // the camera's real cadence
	}
	time.Sleep(time.Second)
	tr.Close()
	mu.Lock()
	chunks := append([]g711Chunk(nil), got...)
	when := append([]time.Time(nil), arrived...)
	mu.Unlock()
	if len(chunks) < 10 {
		t.Fatalf("only %d chunks", len(chunks))
	}
	total := 0
	for i, c := range chunks {
		total += len(c.samples)
		if i > 0 && c.ts != chunks[i-1].ts+int64(len(chunks[i-1].samples)) {
			t.Fatalf("chunk %d: ts %d, want %d (contiguous)", i, c.ts, chunks[i-1].ts+int64(len(chunks[i-1].samples)))
		}
	}
	want := (len(frames) - 2) * 960 // allow the decoder's one-frame delay and the start-up skip
	if total < want || total > len(frames)*960 {
		t.Fatalf("%d samples for %d frames, want about %d (960 per frame)", total, len(frames), len(frames)*960)
	}
	var maxLag time.Duration
	for i, c := range chunks {
		if k := int((c.ts - base) / 960); k >= 0 && k < len(fedAt) {
			if lag := when[i].Sub(fedAt[k]); lag > maxLag {
				maxLag = lag
			}
		}
	}
	t.Logf("browser audio leaves FFmpeg at most %v after its frame went in", maxLag)
	// FFmpeg keeps up: the last chunk leaves well under half a second after
	// its frame was fed
	last := chunks[len(chunks)-1]
	frame := int((last.ts - base) / 960)
	if frame >= 0 && frame < len(fedAt) {
		if lag := when[len(when)-1].Sub(fedAt[frame]); lag > 500*time.Millisecond {
			t.Errorf("browser audio %v behind its input", lag)
		}
	}
}
