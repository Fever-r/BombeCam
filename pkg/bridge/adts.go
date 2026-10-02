package bridge

import (
	"fmt"
	"io"
)

type ADTSHeader struct {
	Syncword          uint16
	ProtectionAbsent  bool
	Profile           uint8
	SamplingFreqIndex uint8
	SampleRate        int
	ChannelConfig     uint8
	FrameLength       int
	HeaderLength      int
	PayloadLength     int
}

var sampleRateTable = map[uint8]int{
	0: 96000, 1: 88200, 2: 64000, 3: 48000,
	4: 44100, 5: 32000, 6: 24000, 7: 22050,
	8: 16000, 9: 12000, 10: 11025, 11: 8000, 12: 7350,
}

// ParseADTSHeader parses and validates the 7 (or 9) byte ADTS header.
// Enforces 12-bit syncword 0xFFF, layer 0, profile AAC-LC, 16000 Hz, mono.
func ParseADTSHeader(b []byte) (*ADTSHeader, error) {
	if len(b) < 7 {
		return nil, fmt.Errorf("buffer too short for ADTS header: %d bytes (min 7)", len(b))
	}
	// Check 12-bit syncword: 0xFFF
	if b[0] != 0xFF || (b[1]&0xF0) != 0xF0 {
		return nil, fmt.Errorf("invalid ADTS syncword: 0x%02X%02X (expected 0xFFF)", b[0], b[1]&0xF0)
	}

	layer := (b[1] >> 1) & 0x03
	if layer != 0 {
		return nil, fmt.Errorf("invalid ADTS layer: %d (must be 0 for AAC)", layer)
	}

	protAbsent := (b[1] & 0x01) == 1
	hdrLen := 7
	if !protAbsent {
		hdrLen = 9
		if len(b) < 9 {
			return nil, fmt.Errorf("buffer too short for ADTS CRC: %d bytes (min 9)", len(b))
		}
	}

	profile := (b[2] >> 6) & 0x03
	if profile != 1 {
		return nil, fmt.Errorf("invalid ADTS profile: %d (expected 1 for AAC-LC)", profile)
	}

	srIdx := (b[2] >> 2) & 0x0F
	if srIdx != 8 {
		return nil, fmt.Errorf("invalid ADTS sampling frequency index: %d (expected 8 for 16000 Hz)", srIdx)
	}
	sampleRate := sampleRateTable[srIdx]

	channelConfig := ((b[2] & 0x01) << 2) | ((b[3] >> 6) & 0x03)
	if channelConfig != 1 {
		return nil, fmt.Errorf("invalid ADTS channel config: %d (expected 1 for mono)", channelConfig)
	}

	frameLen := (int(b[3]&0x03) << 11) | (int(b[4]) << 3) | (int(b[5]&0xE0) >> 5)
	if frameLen < hdrLen {
		return nil, fmt.Errorf("invalid ADTS frame length: %d (less than header %d)", frameLen, hdrLen)
	}

	return &ADTSHeader{
		Syncword:          0xFFF,
		ProtectionAbsent:  protAbsent,
		Profile:           profile,
		SamplingFreqIndex: srIdx,
		SampleRate:        sampleRate,
		ChannelConfig:     channelConfig,
		FrameLength:       frameLen,
		HeaderLength:      hdrLen,
		PayloadLength:     frameLen - hdrLen,
	}, nil
}

// ReadAllADTSFrames extracts and validates all contiguous ADTS frames from an io.Reader.
// Returns an error if the stream is empty, corrupted, or contains malformed frames.
func ReadAllADTSFrames(r io.Reader) ([][]byte, error) {
	buf, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(buf) == 0 {
		return nil, fmt.Errorf("empty audio stream (0 bytes)")
	}

	var frames [][]byte
	offset := 0
	for offset < len(buf) {
		remaining := buf[offset:]
		if len(remaining) < 7 {
			return nil, fmt.Errorf("trailing truncated ADTS header (%d bytes remaining at offset %d)", len(remaining), offset)
		}
		hdr, err := ParseADTSHeader(remaining)
		if err != nil {
			return nil, fmt.Errorf("invalid ADTS frame at offset %d: %w", offset, err)
		}
		if len(remaining) < hdr.FrameLength {
			return nil, fmt.Errorf("truncated ADTS frame at offset %d (need %d bytes, have %d)", offset, hdr.FrameLength, len(remaining))
		}
		frame := make([]byte, hdr.FrameLength)
		copy(frame, remaining[:hdr.FrameLength])
		frames = append(frames, frame)
		offset += hdr.FrameLength
	}

	if len(frames) == 0 {
		return nil, fmt.Errorf("no valid ADTS frames found")
	}
	return frames, nil
}
