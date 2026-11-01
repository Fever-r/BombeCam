package bridge

import "encoding/binary"

// hevcNALAssembler rebuilds HEVC NAL units from RFC 7798 RTP payloads. The
// camera's payloads have already had the RTP header and extensions removed.
type hevcNALAssembler struct {
	cur    []byte
	inFU   bool
	onNALU func(nalu []byte)
}

func newHEVCNALAssembler(onNALU func(nalu []byte)) *hevcNALAssembler {
	return &hevcNALAssembler{onNALU: onNALU}
}

func hevcNALType(nalu []byte) byte {
	if len(nalu) < 2 {
		return 0
	}
	return (nalu[0] >> 1) & 0x3f
}

func hevcRTPType(payload []byte) byte {
	if len(payload) < 2 {
		return 0
	}
	return (payload[0] >> 1) & 0x3f
}

// isHEVCParameterSet reports the unambiguous in-band HEVC SPS/PPS markers
// used to distinguish true HEVC bitstreams from the legacy H.264 payloads
// carried using the vendor's H.265 signaling profile.
func isHEVCParameterSet(payload []byte) bool {
	typ := hevcRTPType(payload)
	if typ == 33 || typ == 34 {
		return true
	}
	return typ == 49 && len(payload) >= 3 && (payload[2]&0x3f == 33 || payload[2]&0x3f == 34)
}

// Push consumes one HEVC RTP payload. Aggregation packets (type 48) are
// unpacked; fragmentation units (type 49) are reconstructed with their
// original two-byte HEVC NAL header.
func (a *hevcNALAssembler) Push(p []byte) {
	if len(p) < 2 {
		return
	}
	switch hevcRTPType(p) {
	case 49:
		if len(p) < 3 {
			return
		}
		start := p[2]&0x80 != 0
		end := p[2]&0x40 != 0
		typ := p[2] & 0x3f
		if start {
			a.Flush()
			header0 := (p[0] & 0x81) | (typ << 1)
			a.cur = make([]byte, 0, 2+len(p[3:])+4096)
			a.cur = append(a.cur, header0, p[1])
			a.cur = append(a.cur, p[3:]...)
			a.inFU = true
		} else if a.inFU {
			a.cur = append(a.cur, p[3:]...)
		} else {
			return
		}
		if end {
			a.Flush()
		}
	case 48:
		a.Flush()
		a.pushAggregation(p)
	default:
		a.Flush()
		nal := append([]byte(nil), p...)
		a.onNALU(nal)
	}
}

func (a *hevcNALAssembler) pushAggregation(p []byte) {
	// This protocol uses DONL-free aggregation packets. If the packet is
	// malformed, discard it rather than forwarding corrupt parameter sets.
	for rest := p[2:]; len(rest) >= 2; {
		n := int(binary.BigEndian.Uint16(rest[:2]))
		rest = rest[2:]
		if n < 2 || n > len(rest) {
			return
		}
		a.onNALU(append([]byte(nil), rest[:n]...))
		rest = rest[n:]
	}
}

func (a *hevcNALAssembler) Flush() {
	if a.inFU && len(a.cur) >= 2 {
		a.onNALU(a.cur)
	}
	a.cur = nil
	a.inFU = false
}
