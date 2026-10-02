package bridge

import (
	"fmt"
	"github.com/Fever-r/BombeCam/internal/winproc"
	"io"
	"os/exec"
	"sync"
	"time"
)

// g711Transcoder turns the camera's AAC (ADTS) into 8 kHz G.711 µ-law with a
// long-running FFmpeg, so WebRTC viewers get sound. Output samples are
// timestamped from the AAC frames they were decoded from, keeping the G.711
// track on exactly the same timeline as the AAC track and the video.
type g711Transcoder struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	in    chan []byte // decouples callers from the FFmpeg pipe

	// frameSamples is the real time one input frame covers (in input
	// samples); outPerFrame is what FFmpeg produces per frame, in input
	// samples: 1024 from the AAC decoder, or frameSamples when FFmpeg
	// resamples each frame to real time (see startG711Transcoder).
	frameSamples int
	outPerFrame  int

	mu     sync.Mutex
	marks  []g711Mark // input frame start (in 8 kHz samples) -> its timestamp
	fedIn  int64      // output samples to expect so far, in the input sample rate
	inRate int        // input sample rate (a rate change restarts the count)
	outAt8 int64      // samples produced so far
	live   bool       // first fresh sample emitted (see readLoop)
	closed bool
	emit   func(samples []byte, ts int64)
}

type g711Mark struct {
	start int64 // position in the output (8 kHz samples)
	ts    int64 // timeline position of that sample (8 kHz ticks)
}

// g711Args builds the FFmpeg command line. AAC decoders always produce 1024
// samples per frame. When the camera's frames cover less real time (the WS03:
// 960 samples, one frame every 120 ms), each decoded frame is resampled to
// its real length (1024 -> 960 at 7500 Hz) and relabelled 8000 Hz, which also
// restores the pitch. Without that, the browser copy would run 6.7% long.
func g711Args(rate, frameSamples int) ([]string, int) {
	args := []string{"-hide_banner", "-loglevel", "error",
		// minimal probing so decoding starts with the first frame; do NOT use
		// -fflags nobuffer, which discards the frames read while probing
		// (the G.711 copy would then run short and its timestamps drift).
		"-probesize", "32", "-analyzeduration", "0",
		"-f", "aac", "-i", "pipe:0"}
	out := 1024
	if rate > 0 && frameSamples > 0 && frameSamples != 1024 && rate*frameSamples%1024 == 0 {
		mid := rate * frameSamples / 1024
		args = append(args, "-af", fmt.Sprintf("aresample=%d,asetrate=%d", mid, rate))
		out = frameSamples
	}
	args = append(args, "-ar", "8000", "-ac", "1", "-f", "mulaw", "-flush_packets", "1", "pipe:1")
	return args, out
}

func startG711Transcoder(ffmpegPath string, rate, frameSamples int, emit func(samples []byte, ts int64)) (*g711Transcoder, error) {
	if frameSamples <= 0 {
		frameSamples = 1024
	}
	args, outPerFrame := g711Args(rate, frameSamples)
	cmd := exec.Command(ffmpegPath, args...)
	winproc.Hide(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg audio transcoder: %w", err)
	}
	// in holds about 2 s of camera audio (one 120 ms frame per chunk): if
	// FFmpeg falls further behind, newer audio is skipped rather than queued
	// (queued audio would come out too late to be in step anyway).
	t := &g711Transcoder{cmd: cmd, stdin: stdin, emit: emit, in: make(chan []byte, 16),
		frameSamples: frameSamples, outPerFrame: outPerFrame}
	go t.writeLoop(t.in)
	go t.readLoop(stdout)
	go func() { _ = cmd.Wait() }()
	return t, nil
}

// Feed queues ADTS frames whose first sample is at timeline position ts (in
// ticks of the AAC sample rate). It never blocks: if FFmpeg falls behind, the chunk is
// dropped (and not timestamped), so the video path is never held up.
func (t *g711Transcoder) Feed(adts []byte, frames int, ts int64, rate int) {
	if rate <= 0 || frames <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	ts8 := ts * 8000 / int64(rate)
	if t.inRate != rate {
		// rate change: continue from the current 8 kHz position
		t.fedIn = t.fedAt8() * int64(rate) / 8000
		t.inRate = rate
	}
	buf := append([]byte(nil), adts...)
	select {
	case t.in <- buf:
	default:
		return // FFmpeg is not keeping up; skip this chunk
	}
	for i := 0; i < frames; i++ {
		t.marks = append(t.marks, g711Mark{start: t.fedAt8(), ts: ts8 + int64(i*t.frameSamples)*8000/int64(rate)})
		t.fedIn += int64(t.outPerFrame)
	}
	if len(t.marks) > 4096 {
		t.marks = t.marks[len(t.marks)-4096:]
	}
}

func (t *g711Transcoder) writeLoop(in <-chan []byte) {
	for b := range in {
		if _, err := t.stdin.Write(b); err != nil {
			t.mu.Lock()
			t.closed = true
			t.mu.Unlock()
			// drain so Feed never blocks
			for range in {
			}
			return
		}
	}
	_ = t.stdin.Close()
}

func (t *g711Transcoder) readLoop(r io.Reader) {
	buf := make([]byte, 160) // 20 ms packets
	for {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			t.mu.Lock()
			start := t.outAt8
			t.outAt8 += int64(n)
			// drop marks for frames fully produced already
			for len(t.marks) > 1 && t.marks[1].start <= start {
				t.marks = t.marks[1:]
			}
			var ts int64
			ok := len(t.marks) > 0 && t.marks[0].start <= start
			if ok {
				ts = t.marks[0].ts + (start - t.marks[0].start)
			}
			if ok && !t.live {
				// MediaMTX places a track's first packet on the timeline by its
				// arrival time, so start the G.711 track with fresh audio: skip
				// any start-up backlog older than 100 ms.
				if t.fedAt8()-t.outAt8 > 800 {
					ok = false
				} else {
					t.live = true
				}
			}
			emit := t.emit
			t.mu.Unlock()
			if ok && emit != nil {
				out := make([]byte, n)
				copy(out, buf[:n])
				emit(out, ts)
			}
		}
		if err != nil {
			return
		}
	}
}

// fedAt8 is the fed position in 8 kHz samples (computed exactly, so rates
// that do not divide 8000 do not accumulate rounding drift). Caller holds mu.
func (t *g711Transcoder) fedAt8() int64 {
	if t.inRate <= 0 {
		return 0
	}
	return t.fedIn * 8000 / int64(t.inRate)
}

// Close stops the transcoder.
func (t *g711Transcoder) Close() {
	t.mu.Lock()
	if t.closed && t.in == nil {
		t.mu.Unlock()
		return
	}
	t.closed = true
	in := t.in
	t.in = nil
	t.mu.Unlock()
	if in != nil {
		close(in) // writeLoop closes FFmpeg's stdin
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
	}()
}
