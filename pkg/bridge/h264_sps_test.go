package bridge

import (
	"testing"
	"time"
)

// Real SPS bytes taken from x264 output (ffmpeg testsrc, yuv420p), so a
// mistake in the Exp-Golomb reader cannot hide behind a hand-built buffer that
// happens to match the decoder. (More vectors: TestParseH264SPS in
// native_sink_ts_test.go.)

var (
	spsBaseline1080p = []byte{
		0x67, 0x42, 0xc0, 0x28, 0xda, 0x01, 0xe0, 0x08, 0x9f, 0x97, 0x01, 0x10,
		0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x01, 0xe8, 0xf1, 0x83,
		0x2a, 0x00,
	}
	spsBaseline720p = []byte{
		0x67, 0x42, 0xc0, 0x1f, 0xda, 0x01, 0x40, 0x16, 0xec, 0x04, 0x40, 0x00,
		0x00, 0x03, 0x00, 0x40, 0x00, 0x00, 0x07, 0xa3, 0xc6, 0x0c, 0xa8, 0x00,
	}
	spsBaseline480p = []byte{
		0x67, 0x42, 0xc0, 0x16, 0xda, 0x02, 0x80, 0xf6, 0xc0, 0x44, 0x00, 0x00,
		0x03, 0x00, 0x04, 0x00, 0x00, 0x03, 0x00, 0x52, 0x3c, 0x58, 0xba, 0x80,
		0x00,
	}
	// The High profile puts chroma_format_idc and the scaling lists in front
	// of the size, which is the part of the syntax most likely to be misread.
	spsHigh1080p = []byte{
		0x67, 0x64, 0x00, 0x28, 0xac, 0xb4, 0x03, 0xc0, 0x11, 0x3f, 0x2e, 0x02,
		0x20, 0x00, 0x00, 0x03, 0x00, 0x20, 0x00, 0x00, 0x03, 0x03, 0xd1, 0xe3,
		0x06, 0x54, 0x00,
	}
)

func TestParseH264SPS_X264Sizes(t *testing.T) {
	for _, c := range []struct {
		name string
		sps  []byte
		w, h int
	}{
		{"baseline 1080p", spsBaseline1080p, 1920, 1080},
		{"baseline 720p", spsBaseline720p, 1280, 720},
		{"baseline 480p", spsBaseline480p, 640, 480},
		{"high 1080p", spsHigh1080p, 1920, 1080},
	} {
		info, err := parseH264SPS(c.sps)
		if err != nil {
			t.Fatalf("%s: rejected: %v", c.name, err)
		}
		if info.Width != c.w || info.Height != c.h {
			t.Fatalf("%s: got %dx%d, want %dx%d", c.name, info.Width, info.Height, c.w, c.h)
		}
	}
}

func TestParseH264SPS_RejectsGarbage(t *testing.T) {
	for _, b := range [][]byte{
		nil,
		{0x67},
		{0x67, 0x42},
		{0x67, 0xff, 0xff, 0xff, 0xff}, // profile_idc 255 is not an H.264 profile
		{0x67, 0x00, 0x0a, 0x00, 0x00}, // reserved profile_idc
	} {
		if _, err := parseH264SPS(b); err == nil {
			t.Fatalf("garbage %v was accepted", b)
		}
	}
}

// A stream that changes size mid-run (the Osaio cameras switch resolution
// with the night-vision or quality setting) must report the new size, not the
// first one it ever saw.
func TestNativeSinkTracksResolutionChanges(t *testing.T) {
	s, _ := newTestSink(t, false)
	for _, c := range []struct {
		sps  []byte
		w, h int
	}{{spsBaseline1080p, 1920, 1080}, {spsBaseline720p, 1280, 720}} {
		s.auNALs = [][]byte{c.sps}
		s.haveAU = true
		s.flushAULocked()
		got, ok := s.VideoInfo()
		if !ok || got.Width != c.w || got.Height != c.h {
			t.Fatalf("want %dx%d, got %+v (ok=%v)", c.w, c.h, got, ok)
		}
	}
}

// The frame rate is measured from arrivals, so a camera clock reset does not
// lose it.
func TestNativeSinkFPSIgnoresCameraClockResets(t *testing.T) {
	s, clk := newTestSink(t, false)
	cam := uint32(0)
	for i := 0; i < 15*25; i++ {
		clk.advance(time.Second / 15)
		cam += 6000
		if i == 15*12 {
			cam = 42 // camera clock reset
		}
		if i%30 == 0 {
			s.OnVideoPacket(spsBaseline720p, cam, false)
		}
		s.OnVideoPacket([]byte{0x41, 0x9a, 1, 2}, cam, true)
	}
	info, ok := s.VideoInfo()
	if !ok || info.FPS < 14.5 || info.FPS > 15.5 {
		t.Fatalf("frame rate across a camera clock reset: %+v (ok=%v)", info, ok)
	}
}
