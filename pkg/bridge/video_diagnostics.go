package bridge

import (
	"fmt"
	"strings"
	"time"
)

const (
	videoDiagWindow = 5 * time.Second
	videoDiagMax    = 3
)

// videoPacketDiagnostics keeps bounded, payload-free summaries of incoming
// video RTP and the NAL units reconstructed from it.
type videoPacketDiagnostics struct {
	startedAt      time.Time
	windows        int
	packets        uint64
	rtpTypes       [64]uint64
	fuStarts       [64]uint64
	fuMiddle       [64]uint64
	fuEnds         [64]uint64
	nalTypes       [64]uint64
	spsErrors      uint64
	lastSPSErr     string
	idrAccessUnits uint64
}

func (d *videoPacketDiagnostics) recordRTPPayload(payload []byte, now time.Time) {
	if len(payload) == 0 {
		return
	}
	if d.startedAt.IsZero() {
		d.startedAt = now
	}
	d.packets++
	typ := int((payload[0] >> 1) & 0x3f)
	d.rtpTypes[typ]++
	if typ != 49 || len(payload) < 3 {
		return
	}
	fuType := int(payload[2] & 0x3f)
	start := payload[2]&0x80 != 0
	end := payload[2]&0x40 != 0
	switch {
	case start && end:
		d.fuStarts[fuType]++
		d.fuEnds[fuType]++
	case start:
		d.fuStarts[fuType]++
	case end:
		d.fuEnds[fuType]++
	default:
		d.fuMiddle[fuType]++
	}
}

func (d *videoPacketDiagnostics) recordNALType(typ byte) {
	if int(typ) < len(d.nalTypes) {
		d.nalTypes[typ]++
	}
}

func (d *videoPacketDiagnostics) recordSPSParseError(err error) {
	d.spsErrors++
	if err != nil {
		d.lastSPSErr = err.Error()
	}
}

func (d *videoPacketDiagnostics) windowReady(now time.Time) bool {
	return d.windows < videoDiagMax && !d.startedAt.IsZero() && now.Sub(d.startedAt) >= videoDiagWindow
}

func (d *videoPacketDiagnostics) resetWindow(now time.Time) {
	d.windows++
	d.startedAt = now
	d.packets = 0
	d.rtpTypes = [64]uint64{}
	d.fuStarts = [64]uint64{}
	d.fuMiddle = [64]uint64{}
	d.fuEnds = [64]uint64{}
	d.nalTypes = [64]uint64{}
	d.spsErrors = 0
	d.lastSPSErr = ""
	d.idrAccessUnits = 0
}

func (d *videoPacketDiagnostics) summary() string {
	return fmt.Sprintf("packets=%d RTP-NAL=%s FU-start=%s FU-middle=%s FU-end=%s rebuilt-NAL=%s SPS-parse-errors=%d last-SPS-error=%q",
		d.packets, formatTypeCounts(d.rtpTypes[:]), formatTypeCounts(d.fuStarts[:]),
		formatTypeCounts(d.fuMiddle[:]), formatTypeCounts(d.fuEnds[:]),
		formatTypeCounts(d.nalTypes[:]), d.spsErrors, d.lastSPSErr)
}

func formatTypeCounts(counts []uint64) string {
	var b strings.Builder
	b.WriteByte('{')
	sep := ""
	for typ, n := range counts {
		if n == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s%d:%d", sep, typ, n)
		sep = ","
	}
	b.WriteByte('}')
	return b.String()
}
