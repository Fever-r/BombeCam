package bridge

import (
	"testing"
	"time"
)

// The published RTSP timeline must never move backwards: MediaMTX restarts
// its HLS muxer and Frigate's recorder drops segments when a DTS regresses.

func TestTSMonotonicPassesHealthyStream(t *testing.T) {
	var m tsMonotonic
	var prev uint32
	var first = true
	ts := uint32(0)
	// 15 fps at 90 kHz, as the camera actually sends it.
	for i := 0; i < 3000; i++ {
		ts += 6000
		got, held := m.next(ts)
		if held != 0 {
			t.Fatalf("frame %d: healthy stream was held back by %d ticks", i, held)
		}
		if !first && int32(got-prev) != 6000 {
			t.Fatalf("frame %d: step was %d, want 6000", i, int32(got-prev))
		}
		prev, first = got, false
	}
}

func TestTSMonotonicHoldsBackwardsJump(t *testing.T) {
	var m tsMonotonic
	base := uint32(900000)
	first, _ := m.next(base)
	second, _ := m.next(base + 6000)
	if int32(second-first) != 6000 {
		t.Fatalf("expected a normal 6000 tick step, got %d", int32(second-first))
	}

	// The camera clock jumps 0.88 s backwards, the case seen in the field.
	// The timeline is held for as long as the camera stays behind, which for
	// a 0.88 s jump at 15 fps is about 13 frames, and then goes back to the
	// camera's own spacing.
	cam := base - 79200
	jumped, held := m.next(cam)
	if held <= 0 {
		t.Fatalf("a backwards jump was not reported as held (held=%d)", held)
	}
	if int32(jumped-second) <= 0 {
		t.Fatalf("timeline did not advance across a backwards jump: %d -> %d", second, jumped)
	}

	recovered := false
	for i := 0; i < 64; i++ {
		cam += 6000
		got, h := m.next(cam)
		if h == 0 {
			// The frame that ends the hold carries the catch-up; the frames
			// after it are back to the camera's own spacing.
			if i+1 < 64 {
				cam += 6000
				next, h2 := m.next(cam)
				if h2 != 0 || int32(next-got) != 6000 {
					t.Fatalf("after recovery the step was %d (held=%d), want 6000", int32(next-got), h2)
				}
			}
			recovered = true
			break
		}
		jumped = got
	}
	if !recovered {
		t.Fatal("the timeline never recovered to the camera's own clock")
	}
}

func TestTSMonotonicHandlesCameraClockReset(t *testing.T) {
	var m tsMonotonic
	ts := uint32(5000000)
	for i := 0; i < 100; i++ {
		ts += 3000
		m.next(ts)
	}
	// The camera restarts its clock at a small value.
	ts = 900
	if _, held := m.next(ts); held <= 0 {
		t.Fatalf("a clock reset was not held (held=%d)", held)
	}
	if got, _ := m.current(); got == 0 {
		t.Fatal("timeline lost its value")
	}
}

func TestTSMonotonicSurvivesWrapAround(t *testing.T) {
	var m tsMonotonic
	// 32-bit RTP timestamps wrap every 13.25 h at 90 kHz. Start near the top.
	ts := uint32(0xFFFFFF00)
	var last uint32
	for i := 0; i < 64; i++ {
		ts += 6000
		got, held := m.next(ts)
		if held != 0 {
			t.Fatalf("wrap at step %d was treated as a backwards jump (held=%d)", i, held)
		}
		if i > 0 && int32(got-last) <= 0 {
			t.Fatalf("wrap at step %d did not advance: %d -> %d", i, last, got)
		}
		last = got
	}
}

func TestTSMonotonicResetStartsFresh(t *testing.T) {
	var m tsMonotonic
	m.next(900000)
	m.reset()
	if _, ok := m.current(); ok {
		t.Fatal("reset did not clear the timeline")
	}
	// After a reset a small timestamp is legitimate again.
	got, held := m.next(120)
	if held != 0 || got != 120 {
		t.Fatalf("value after reset was clamped: got=%d held=%d", got, held)
	}
}

// --- video timeline derivation -------------------------------------------

// videoTSLocked must never hand the publisher a smaller value than the one
// before it, even when a camera clock reset resyncs it onto the wall clock. Holding a value for one frame
// is allowed here; the write-time guard turns that into a strict increase.
func TestVideoTSNeverGoesBackwards(t *testing.T) {
	s := newNativeSink("rtsp://127.0.0.1:1/x", "test", false)
	now := time.Now()
	cam := uint32(0)
	last := int64(-1)
	step := func(phase string, advance, arrive time.Duration, reset bool) {
		now = now.Add(arrive)
		if reset {
			cam = 1000 // camera clock reset to nearly zero
		}
		cam += uint32(advance / time.Microsecond * 90 / 1000)
		v := s.videoTSLocked(cam, now)
		if last >= 0 && v < last {
			t.Fatalf("%s: video timeline went backwards: %d -> %d", phase, last, v)
		}
		last = v
	}
	// A camera whose clock runs fast compared with when frames arrive.
	for i := 0; i < 200; i++ {
		step("fast clock", 45*time.Millisecond, 40*time.Millisecond, false)
	}
	for i := 0; i < 200; i++ {
		step("after reset", 45*time.Millisecond, 40*time.Millisecond, true)
	}
}

// The published timeline is the guard's output, and that one is strictly
// increasing even while the derived clock is being held.
func TestPublishedTimelineIsStrictlyIncreasing(t *testing.T) {
	s := newNativeSink("rtsp://127.0.0.1:1/x", "test", false)
	now := time.Now()
	cam := uint32(0)
	var guard tsMonotonic
	var prev uint32
	first := true
	for i := 0; i < 400; i++ {
		now = now.Add(40 * time.Millisecond)
		if i == 200 {
			cam = 1000 // camera clock reset
		}
		cam += 4500
		got, _ := guard.next(uint32(s.videoTSLocked(cam, now)))
		if !first && int32(got-prev) <= 0 {
			t.Fatalf("frame %d: published timeline did not increase: %d -> %d", i, prev, got)
		}
		prev, first = got, false
	}
}

// --- alignment delay decay ------------------------------------------------
//
// Rising and gliding back down is covered in native_sink_ts_test.go
// (TestAlignmentDelay_RisesThenDecays); these pin the two limits.

func TestAlignmentDelayNeverDecaysBelowFloor(t *testing.T) {
	s, clk := newTestSink(t, true)
	s.delay, s.minDelay = 220*time.Millisecond, 220*time.Millisecond
	for i := 0; i < 5000; i++ { // ten minutes of audio right on time
		clk.advance(120 * time.Millisecond)
		s.enqueueLocked(trackAAC, sinkItem{}, clk.now())
		s.queues[trackAAC] = nil
	}
	if s.delay != s.minDelay {
		t.Fatalf("decayed past the floor: %v < %v", s.delay, s.minDelay)
	}
}

func TestAlignmentDelayNeverExceedsCap(t *testing.T) {
	s, clk := newTestSink(t, true)
	s.delay, s.minDelay = 100*time.Millisecond, 100*time.Millisecond
	for i := 0; i < 200; i++ {
		clk.advance(120 * time.Millisecond)
		s.enqueueLocked(trackAAC, sinkItem{}, clk.now().Add(-30*time.Second))
		s.queues[trackAAC] = nil
	}
	if s.delay > maxAlignDelay {
		t.Fatalf("the delay exceeded its cap: %v > %v", s.delay, maxAlignDelay)
	}
}

// The write-time guard is a no-op on the sink's own timeline and strictly
// increasing when fed a step back.
func TestWriteGuardPassesSinkTimelineAndHoldsSteps(t *testing.T) {
	s, _ := newTestSink(t, false)
	for i, ts := range []uint32{1000, 7000, 13000} {
		if got := s.guardLocked(trackVideo, ts); got != ts {
			t.Fatalf("frame %d: healthy timestamp %d changed to %d", i, ts, got)
		}
	}
	if got := s.guardLocked(trackVideo, 5000); got != 13001 {
		t.Fatalf("a step back should be held at 13001, got %d", got)
	}
	s.setPubLocked(nil) // a new publication starts a fresh timeline
	if got := s.guardLocked(trackVideo, 42); got != 42 {
		t.Fatalf("after a new publication: %d", got)
	}
}
