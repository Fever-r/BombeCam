package bridge

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// Build complete minimal SPS syntax, rather than just changing profile bytes
// on a Baseline SPS (High has extra fields before frame_num).
func regressionSPS(profile, constraints, level byte, width, height, frameWidth uint) []byte {
	w := sliceBitWriter{}
	w.bits(uint(profile), 8)
	w.bits(uint(constraints), 8)
	w.bits(uint(level), 8)
	w.ue(0) // SPS ID
	if profile == 100 {
		w.ue(1) // 4:2:0
		w.ue(0) // 8-bit luma/chroma
		w.ue(0)
		w.bits(0, 1) // transform bypass
		w.bits(0, 1) // scaling matrix absent
	}
	w.ue(frameWidth - 4)
	w.ue(2) // POC type 2
	w.ue(1) // max reference frames
	w.bits(0, 1)
	w.ue(width/16 - 1)
	w.ue(height/16 - 1)
	w.bits(1, 1) // frame_mbs_only
	w.bits(1, 1) // direct_8x8_inference
	w.bits(0, 1) // no cropping
	w.bits(0, 1) // no VUI
	w.bits(1, 1) // RBSP trailing bit
	return append([]byte{0x67}, w.b...)
}

func TestSPSRegressionValidHeaders(t *testing.T) {
	for _, profile := range []byte{66, 77, 100} {
		for _, level := range []byte{30, 41, 50, 51} {
			for _, constraints := range []byte{0, 0x80, 0x40, 0x20, 0x10, 0x08, 0x04, 0xfc} {
				t.Run(fmt.Sprintf("profile%d/level%d/flags%02x", profile, level, constraints), func(t *testing.T) {
					nal := regressionSPS(profile, constraints, level, 2304, 1296, 4)
					s, err := parseH264SPS(nal)
					if err != nil {
						t.Fatalf("SPS %x rejected: %v", nal, err)
					}
					if s.Width != 2304 || s.Height != 1296 || s.ProfileIDC != int(profile) || s.LevelIDC != int(level) || s.POCType != 2 || s.FPS != 0 {
						t.Fatalf("SPS %x: unexpected info %+v", nal, s)
					}
				})
			}
		}
	}
}

func TestKeyframeRegressionRandomAndTruncated(t *testing.T) {
	rng := rand.New(rand.NewSource(20261009))
	fixedNow := func() time.Time { return time.Unix(0, 0) }
	for i := 0; i < 4096; i++ {
		full := make([]byte, 64)
		rng.Read(full)
		for n := 0; n <= len(full); n++ {
			p := full[:n]
			// A fresh detector has no SPS: unknown types must never guess IDR.
			d := keyframeDetector{now: fixedNow}
			got := d.startHeader(p)
			known := n >= 3 && (p[2]&63 == 18 || p[2]&63 == 19 || p[2]&63 == 20 || p[2]&63 == 50)
			want := byte(0x41)
			if known {
				want = 0x65
			}
			if got != want {
				t.Fatalf("iteration=%d payload=%x: header=%x want=%x", i, p, got, want)
			}
			d.observeNAL(p)
			d.startHeader(p)
			d.observeNAL(spsBaseline720p)
			d.startHeader(p)
			// Exercise the SPS parser even when the random first byte is not SPS.
			if n > 0 {
				forced := append([]byte(nil), p...)
				forced[0] = 0x67
				d.observeNAL(forced)
				d.startHeader(p)
			}
		}
	}
}
