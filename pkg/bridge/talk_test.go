package bridge

import (
	"encoding/binary"
	"math"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// The camera's own mic stream decides the talkback format.
func TestTalkFormat_MirrorsCameraAudio(t *testing.T) {
	v := &Viewer{model: "t", doneChan: make(chan struct{})}
	if rate, step := v.talkFormat(); rate != 16000 || step != 1024 {
		t.Fatalf("default talk format = %d/%d", rate, step)
	}
	// 8 kHz mono ADTS frame header (freq index 11), two frames per packet, 16 kHz RTP clock
	frame := []byte{0xFF, 0xF1, 0x6C, 0x40, 0x01, 0x7F, 0xFC, 0x01, 0x18, 0x20, 0x07}
	frame[3] = 0x40
	frame[4] = byte(len(frame) >> 3)
	frame[5] = byte(len(frame)&7)<<5 | 0x1F
	pkt := append(append([]byte{}, frame...), frame...)
	ts := uint32(1000)
	for i := 0; i < 30; i++ {
		v.observeCameraAudio(pkt, ts, false, 16000, 96)
		ts += 4096 // 2 frames x 2048 ticks
	}
	rate, step := v.talkFormat()
	if rate != 8000 || step != 2048 {
		t.Fatalf("talk format = %d Hz / step %d, want 8000 / 2048", rate, step)
	}
}

// Browser PCM in -> camera-format AAC frames out, paced in real time.
func TestTalkAudio_EncodesAndPaces(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	v := &Viewer{model: "t", doneChan: make(chan struct{})}
	v.camAudio = camAudioInfo{sampleRate: 8000, channels: 1, clock: 16000, tsStep: 2048}
	var mu sync.Mutex
	var stamps []time.Time
	var steps []uint32
	var srIdx []byte
	v.talkWriteHook = func(f []byte, step uint32, marker bool) error {
		mu.Lock()
		defer mu.Unlock()
		stamps = append(stamps, time.Now())
		steps = append(steps, step)
		srIdx = append(srIdx, (f[2]>>2)&0x0F)
		return nil
	}
	if err := v.StartTalkAudio(ffmpeg); err != nil {
		t.Fatal(err)
	}
	// 2 s of a 440 Hz tone, 16 kHz s16le mono, delivered in 128 ms chunks
	pcm := make([]byte, 2*16000*2)
	for i := 0; i < len(pcm)/2; i++ {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(8000*math.Sin(2*math.Pi*440*float64(i)/16000))))
	}
	// in 384 ms bursts (three frames at once), as a browser's uploads
	// arrive; the pacer spreads each burst out
	for off := 0; off < len(pcm); off += 12288 {
		if err := v.WriteTalkPCM(pcm[off:min(off+12288, len(pcm))]); err != nil {
			t.Fatal(err)
		}
		time.Sleep(384 * time.Millisecond)
	}
	time.Sleep(800 * time.Millisecond)
	v.StopTalkAudio()

	mu.Lock()
	defer mu.Unlock()
	if len(stamps) < 12 {
		t.Fatalf("only %d AAC frames sent for 2 s of speech", len(stamps))
	}
	for i := range srIdx {
		if srIdx[i] != 11 || steps[i] != 2048 {
			t.Fatalf("frame %d: freq index %d step %d, want 11 (8 kHz) / 2048", i, srIdx[i], steps[i])
		}
	}
	// paced: frames ~128 ms apart on average, not a burst
	span := stamps[len(stamps)-1].Sub(stamps[0])
	avg := span / time.Duration(len(stamps)-1)
	if avg < 100*time.Millisecond || avg > 160*time.Millisecond {
		t.Fatalf("average frame spacing %v, want ~128ms", avg)
	}
}
