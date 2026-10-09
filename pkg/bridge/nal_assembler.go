package bridge

import (
	"fmt"
	"strings"
	"time"
)

// nalAssembler rebuilds H.264 NAL units from the camera's video RTP payloads.
//
// Fragmentation units use the HEVC-style FU header (type 49). Start fragments
// use 0x65 for known IDR types 18/19/20 (WS03) and 50 (P5), or for unknown
// types whose SPS-backed slice header has an I/SI slice and frame_num 0;
// otherwise they use 0x41. Payload byte 1 and the fragment data follow the
// rebuilt header; every other payload is a complete NAL unit.
// TestNALAssembler_MatchesLegacyAnnexB pins the original WS03 transform.
type nalAssembler struct {
	cur      []byte
	inFU     bool
	onNALU   func(nalu []byte)
	detector keyframeDetector
}

func newNALAssembler(onNALU func(nalu []byte)) *nalAssembler {
	return &nalAssembler{onNALU: onNALU}
}

// Push consumes one RTP payload (RTP header and extensions already stripped).
func (a *nalAssembler) Push(p []byte) {
	if len(p) == 0 {
		return
	}
	nalType := (p[0] >> 1) & 0x3f
	if nalType == 49 && len(p) >= 3 {
		start := (p[2]>>7)&1 == 1
		end := (p[2]>>6)&1 == 1
		if start {
			a.Flush()
			nalHdr := a.detector.startHeader(p)
			a.cur = make([]byte, 0, 2+len(p[3:])+4096)
			a.cur = append(a.cur, nalHdr, p[1])
			a.cur = append(a.cur, p[3:]...)
			a.inFU = true
		} else if a.inFU {
			a.cur = append(a.cur, p[3:]...)
		} else {
			// continuation without a start fragment (start packet lost): drop
			return
		}
		if end {
			a.Flush()
		}
		return
	}
	// Single NAL unit (SPS, PPS, SEI, small slices)
	a.Flush()
	a.detector.observeNAL(p)
	nal := make([]byte, len(p))
	copy(nal, p)
	a.onNALU(nal)
}

// Flush emits any partially assembled NAL unit.
func (a *nalAssembler) Flush() {
	if a.inFU && len(a.cur) > 0 {
		a.onNALU(a.cur)
	}
	a.cur = nil
	a.inFU = false
}

// h264NALType returns the H.264 nal_unit_type of a NAL unit.
func h264NALType(nalu []byte) byte {
	if len(nalu) == 0 {
		return 0
	}
	return nalu[0] & 0x1f
}

// keyframeDetector is shared by the native and FFmpeg depacketizers. Its zero
// value is ready to use; now may be injected to test the one-shot diagnostic.
type keyframeDetector struct {
	label       string
	now         func() time.Time
	sps         h264SPS
	spsSeen     bool
	haveSPS     bool
	firstStart  time.Time
	started     bool
	keyframe    bool
	logged      bool
	fuCounts    [64]int
	sliceCounts [5]int
}

func (d *keyframeDetector) observeNAL(nal []byte) {
	switch h264NALType(nal) {
	case 7:
		d.spsSeen = true
		var err error
		d.sps, err = parseH264SPS(nal)
		d.haveSPS = err == nil
	case 5:
		d.keyframe = true
	}
	d.diagnose()
}

// startHeader consumes a vendor FU start payload, not a reconstructed NAL.
func (d *keyframeDetector) startHeader(p []byte) byte {
	if len(p) < 3 {
		return 0x41
	}
	if !d.started {
		d.firstStart = d.clock()
		d.started = true
	}
	defer d.diagnose()
	fuType := p[2] & 0x3f
	d.fuCounts[fuType]++
	switch fuType {
	case 18, 19, 20, 50:
		d.keyframe = true
		return 0x65
	}
	// Bound work on corrupt input, preserving byte 1 before fragment data.
	n := min(len(p)-3, 32)
	header := make([]byte, 1, 1+n)
	header[0] = p[1]
	header = append(header, p[3:3+n]...)
	r := &bitReader{b: unescapeRBSP(header)}
	r.ue() // first_mb_in_slice
	sliceType := r.ue()
	ppsID := r.ue()
	if r.err != nil || sliceType > 9 || ppsID > 255 {
		return 0x41
	}
	d.sliceCounts[sliceType%5]++
	if !d.haveSPS || d.sps.Log2MaxFrameNum < 4 || d.sps.Log2MaxFrameNum > 16 {
		return 0x41
	}
	if d.sps.SeparateColourPlane {
		r.bits(2) // colour_plane_id
	}
	frameNum := r.bits(d.sps.Log2MaxFrameNum)
	// Non-IDR I slices at frame_num wrap or from "virtual I-frame" encoders
	// can be relabelled IDR here. This is a heuristic, not proof of IDR syntax.
	// Do not learn per FU type: one type may carry both I and P slices.
	if r.err == nil && (sliceType%5 == 2 || sliceType%5 == 4) && frameNum == 0 {
		d.keyframe = true
		return 0x65
	}
	return 0x41
}

func (d *keyframeDetector) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

func (d *keyframeDetector) diagnose() {
	if !d.started || d.keyframe || d.logged || d.clock().Sub(d.firstStart) < 10*time.Second {
		return
	}
	d.logged = true
	var types, slices []string
	for typ, count := range d.fuCounts {
		if count > 0 {
			types = append(types, fmt.Sprintf("%d×%d", typ, count))
		}
	}
	for typ, count := range d.sliceCounts {
		if count > 0 {
			slices = append(slices, fmt.Sprintf("%s×%d", []string{"P", "B", "I", "SP", "SI"}[typ], count))
		}
	}
	seen := "no"
	if d.spsSeen {
		seen = "yes"
		if !d.haveSPS {
			seen = "yes (could not be parsed)"
		} else if d.sps.Log2MaxFrameNum < 4 || d.sps.Log2MaxFrameNum > 16 {
			seen = fmt.Sprintf("yes (frame_num width %d unsupported)", d.sps.Log2MaxFrameNum)
		}
	}
	if len(slices) == 0 {
		slices = append(slices, "none")
	}
	fmt.Printf("[%s] [video] no keyframe recognised in 10 s: FU start types %s; SPS seen: %s; unknown-type slice types: %s\n",
		d.label, strings.Join(types, " "), seen, strings.Join(slices, " "))
}
