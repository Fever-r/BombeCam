package bridge

import (
	"bytes"
	"testing"
)

func TestHEVCNALAssemblerRebuildsSingleFUAggregation(t *testing.T) {
	var got [][]byte
	a := newHEVCNALAssembler(func(n []byte) { got = append(got, append([]byte(nil), n...)) })

	vps := []byte{0x40, 0x01, 0xaa}
	sps := []byte{0x42, 0x01, 0xbb}
	pps := []byte{0x44, 0x01, 0xcc}
	a.Push(vps)
	a.Push([]byte{49 << 1, 0x01, 0x80 | 19, 1, 2})
	a.Push([]byte{49 << 1, 0x01, 19, 3})
	a.Push([]byte{49 << 1, 0x01, 0x40 | 19, 4})
	ap := []byte{48 << 1, 0x01, 0, byte(len(sps))}
	ap = append(ap, sps...)
	ap = append(ap, 0, byte(len(pps)))
	ap = append(ap, pps...)
	a.Push(ap)

	want := [][]byte{
		vps,
		{byte(19 << 1), 0x01, 1, 2, 3, 4},
		sps,
		pps,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d NALs, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("NAL %d = %x, want %x", i, got[i], want[i])
		}
	}
}

func TestIsHEVCParameterSet(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    []byte
		want bool
	}{
		{"HEVC SPS", []byte{33 << 1, 1, 0xaa}, true},
		{"HEVC PPS", []byte{34 << 1, 1, 0xaa}, true},
		{"fragmented HEVC SPS", []byte{49 << 1, 1, 0x80 | 33, 1}, true},
		{"legacy H264 SPS", []byte{0x67, 0x42, 1}, false},
		{"legacy H264 PPS", []byte{0x68, 0xce, 1}, false},
		{"ordinary HEVC slice", []byte{1 << 1, 1, 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHEVCParameterSet(tc.p); got != tc.want {
				t.Fatalf("isHEVCParameterSet(%x) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

func TestNativeSinkSwitchesToHEVCAndKeepsPrecedingVPS(t *testing.T) {
	s := newNativeSink("rtsp://127.0.0.1:1/test", "hevc-test", true)
	s.OnVideoPacket([]byte{0x40, 0x01, 0xaa}, 1, false) // VPS may precede the SPS.
	s.OnVideoPacket([]byte{0x42, 0x01, 0xbb}, 2, false) // SPS identifies true HEVC.
	s.OnVideoPacket([]byte{0x44, 0x01, 0xcc}, 3, false) // PPS.
	s.OnVideoPacket([]byte{0x26, 0x01, 0xdd}, 4, false) // IDR.
	s.mu.Lock()
	s.hevcAsm.Flush()
	s.flushAULocked()
	defer s.mu.Unlock()
	if !s.videoHEVC {
		t.Fatal("sink did not switch to HEVC")
	}
	for name, got := range map[string][]byte{"VPS": s.vps, "SPS": s.sps, "PPS": s.pps} {
		if len(got) < 2 {
			t.Errorf("%s was not retained: %x", name, got)
		}
	}
}
