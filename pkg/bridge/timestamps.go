package bridge

// Monotonic publication timestamps.
//
// MediaMTX (and every recorder behind it: Frigate's segmenter, ffmpeg's HLS
// muxer) refuses a track whose DTS moves backwards and restarts its muxer when
// it happens. The camera's own clock is not a safe source for a publication
// timeline: it can reset, it can jump, and our own resync paths can step the
// derived clock back onto the wall clock. A backwards step of 0.88 s is
// enough to restart the HLS muxer, so it is not a cosmetic problem.
//
// tsMonotonic turns the raw 32-bit RTP timestamps of one track into a
// strictly increasing line:
//
//   - the incoming 32-bit value is unwrapped relative to the last value
//     written, so ordinary wrap-around (every 13.25 h at 90 kHz) stays
//     continuous rather than being read as a huge jump backwards;
//   - if the unwrapped value would not advance, the line is held at last+1
//     instead. A camera clock reset therefore shows up as a short gap, which
//     every consumer already handles, rather than as a backwards DTS.
//
// The guard is deliberately not a filter on the whole stream: it only ever
// holds a value that would have gone backwards, so a healthy stream is passed
// through untouched.

type tsMonotonic struct {
	hasLast bool
	last    int64 // the last value actually emitted, in unwrapped 64-bit ticks
}

// next returns the value to publish for the raw 32-bit timestamp ts, together
// with how many ticks the line was held back (0 when nothing was clamped).
func (m *tsMonotonic) next(ts uint32) (uint32, int64) {
	if !m.hasLast {
		m.hasLast = true
		m.last = int64(ts)
		return ts, 0
	}
	// Unwrap relative to the last emitted value: the signed difference is
	// exact as long as the true step is below 2^31 ticks, which holds for
	// every timestamp jump a camera can realistically produce.
	ext := m.last + int64(int32(ts-uint32(m.last)))
	if ext > m.last {
		m.last = ext
		return uint32(ext), 0
	}
	held := m.last + 1 - ext
	m.last = m.last + 1
	return uint32(m.last), held
}

// last returns the most recent emitted value and whether one was emitted.
func (m *tsMonotonic) current() (uint32, bool) {
	if !m.hasLast {
		return 0, false
	}
	return uint32(m.last), true
}

// reset forgets the timeline, so the next value starts a fresh line. It is
// used when a new RTSP publication is announced: MediaMTX re-bases on the
// first packet it receives, so there is no reason to carry the old line over.
func (m *tsMonotonic) reset() {
	m.hasLast = false
	m.last = 0
}
