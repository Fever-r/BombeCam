package bridge

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func TestBuildPublishSDPHEVC(t *testing.T) {
	vps, sps, pps := []byte{0x40, 1, 2}, []byte{0x42, 1, 3}, []byte{0x44, 1, 4}
	sdp := buildPublishSDPHEVC(vps, sps, pps, nil, false)
	for _, want := range []string{
		"m=video 0 RTP/AVP 96",
		"a=rtpmap:96 H265/90000",
		"sprop-vps=" + "QAEC",
		"sprop-sps=" + "QgED",
		"sprop-pps=" + "RAEE",
	} {
		if !strings.Contains(sdp, want) {
			t.Errorf("HEVC SDP does not contain %q:\n%s", want, sdp)
		}
	}
	if strings.Contains(sdp, "H264/") || strings.Contains(sdp, "sprop-parameter-sets") {
		t.Fatalf("HEVC SDP contains H.264 parameters:\n%s", sdp)
	}
}

func TestRTSPPublisherWriteH265FragmentsAndMarksAccessUnit(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	p := &rtspPublisher{
		conn: client, bw: bufio.NewWriter(client), done: make(chan struct{}),
		videoSeq: 10, videoSSRC: 0x12345678, writeLimit: 5 * time.Second,
	}
	// Large enough to require several HEVC FU packets.
	nal := make([]byte, 3000)
	nal[0], nal[1] = 19<<1, 1
	for i := 2; i < len(nal); i++ {
		nal[i] = byte(i)
	}
	writeErr := make(chan error, 1)
	go func() { writeErr <- p.WriteH265([][]byte{nal}, 9000) }()

	var packets []rtp.Packet
	for {
		var frameHeader [4]byte
		if _, err := io.ReadFull(server, frameHeader[:]); err != nil {
			t.Fatalf("read interleaved frame header: %v", err)
		}
		if frameHeader[0] != '$' || frameHeader[1] != 0 {
			t.Fatalf("unexpected RTSP interleaved header: %x", frameHeader)
		}
		payload := make([]byte, binary.BigEndian.Uint16(frameHeader[2:]))
		if _, err := io.ReadFull(server, payload); err != nil {
			t.Fatalf("read RTP packet: %v", err)
		}
		var packet rtp.Packet
		if err := packet.Unmarshal(payload); err != nil {
			t.Fatalf("unmarshal RTP: %v", err)
		}
		packets = append(packets, packet)
		if packet.Marker {
			break
		}
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("WriteH265: %v", err)
	}
	if len(packets) < 2 {
		t.Fatalf("expected fragmented HEVC output, got %d packet", len(packets))
	}
	for i, packet := range packets {
		if packet.PayloadType != 96 || packet.Timestamp != 9000 || packet.SSRC != 0x12345678 {
			t.Errorf("packet %d header = %+v", i, packet.Header)
		}
		if got := hevcRTPType(packet.Payload); got != 49 {
			t.Errorf("packet %d NAL type = %d, want FU type 49", i, got)
		}
		if packet.Marker != (i == len(packets)-1) {
			t.Errorf("packet %d marker=%v, want %v", i, packet.Marker, i == len(packets)-1)
		}
	}
}
