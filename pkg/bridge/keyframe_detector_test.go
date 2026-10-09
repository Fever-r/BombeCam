package bridge

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

type sliceBitWriter struct {
	b   []byte
	pos int
}

func (w *sliceBitWriter) bits(v uint, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.pos%8 == 0 {
			w.b = append(w.b, 0)
		}
		w.b[w.pos/8] |= byte((v>>uint(i))&1) << uint(7-w.pos%8)
		w.pos++
	}
}

func (w *sliceBitWriter) ue(v uint) {
	v++
	n := 0
	for x := v; x > 0; x >>= 1 {
		n++
	}
	w.bits(0, n-1)
	w.bits(v, n)
}

func sliceStart(typ byte, sliceType, frameNum uint, width int, separate bool) []byte {
	w := sliceBitWriter{}
	w.ue(0)
	w.ue(sliceType)
	w.ue(0)
	if separate {
		w.bits(2, 2)
	}
	w.bits(frameNum, width)
	w.bits(1, 1) // trailing bit
	return append([]byte{49 << 1, w.b[0], 0x80 | typ}, w.b[1:]...)
}

func TestKeyframeDetectorHeaders(t *testing.T) {
	sps, err := parseH264SPS(spsBaseline720p)
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []byte{18, 19, 20, 50} {
		d := keyframeDetector{}
		if got := d.startHeader([]byte{49 << 1, 0, 0x80 | typ}); got != 0x65 {
			t.Fatalf("known type %d: header=%x", typ, got)
		}
	}
	for _, c := range []struct {
		name         string
		typ          byte
		slice, frame uint
		haveSPS      bool
		want         byte
	}{
		{"P type 1", 1, 0, 0, true, 0x41},
		{"P type 32", 32, 5, 0, true, 0x41},
		{"I type 1", 1, 2, 0, true, 0x65},
		{"I type 32", 32, 7, 0, true, 0x65},
		{"I nonzero frame", 32, 2, 1, true, 0x41},
		{"I without SPS", 32, 2, 0, false, 0x41},
		{"SI", 32, 4, 0, true, 0x65},
		{"B", 32, 1, 0, true, 0x41},
		{"SP", 32, 3, 0, true, 0x41},
		{"invalid slice type", 32, 10, 0, true, 0x41},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := keyframeDetector{}
			if c.haveSPS {
				d.observeNAL(spsBaseline720p)
			}
			if got := d.startHeader(sliceStart(c.typ, c.slice, c.frame, sps.Log2MaxFrameNum, false)); got != c.want {
				t.Fatalf("header=%x want=%x", got, c.want)
			}
		})
	}
}

func TestKeyframeDetectorMalformed(t *testing.T) {
	for _, p := range [][]byte{nil, {98}, {98, 0}, {98, 0, 0xa0}, {98, 0xff, 0xa0}, {98, 0, 0xa0, 0, 0, 0, 0, 0}} {
		d := keyframeDetector{}
		d.observeNAL(spsBaseline720p)
		if got := d.startHeader(p); got != 0x41 {
			t.Fatalf("payload %x: header=%x", p, got)
		}
	}
	d := keyframeDetector{}
	d.observeNAL(spsBaseline720p)
	d.observeNAL([]byte{0x67}) // latest SPS is broken: do not guess its layout
	if got := d.startHeader(sliceStart(32, 2, 0, 4, false)); got != 0x41 {
		t.Fatalf("invalid SPS: header=%x", got)
	}
}

func TestKeyframeDetectorEmulationPrevention(t *testing.T) {
	// A large first_mb_in_slice puts 00 00 03 inside the header, including
	// across the p[1]/p[3:] boundary. It is irrelevant to IDR recognition.
	w := sliceBitWriter{}
	w.ue((1 << 24) - 1)
	w.ue(2)
	w.ue(0)
	w.bits(0, 4)
	w.bits(1, 1)
	var escaped []byte
	zeros := 0
	for _, b := range w.b {
		if zeros == 2 && b <= 3 {
			escaped = append(escaped, 3)
			zeros = 0
		}
		escaped = append(escaped, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	if !bytes.Contains(escaped, []byte{0, 0, 3}) {
		t.Fatal("test did not insert emulation-prevention byte")
	}
	d := keyframeDetector{}
	d.observeNAL(spsBaseline720p)
	p := append([]byte{98, escaped[0], 0xa0}, escaped[1:]...)
	if got := d.startHeader(p); got != 0x65 {
		t.Fatalf("escaped header=%x", got)
	}
}

func TestKeyframeDetectorSeparateColourPlane(t *testing.T) {
	w := sliceBitWriter{}
	w.bits(100, 8)
	w.bits(0, 8)
	w.bits(40, 8)
	w.ue(0)
	w.ue(3)
	w.bits(1, 1) // SPS ID, chroma format, separate plane
	w.ue(0)
	w.ue(0)
	w.bits(0, 1)
	w.bits(0, 1)
	w.ue(2)
	w.ue(2) // six-bit frame_num, POC type 2
	w.ue(1)
	w.bits(0, 1)
	w.ue(39)
	w.ue(29)
	w.bits(1, 1)
	w.bits(1, 1)
	w.bits(0, 1)
	w.bits(0, 1)
	w.bits(1, 1)
	d := keyframeDetector{}
	d.observeNAL(append([]byte{0x67}, w.b...))
	if !d.haveSPS || !d.sps.SeparateColourPlane || d.sps.Log2MaxFrameNum != 6 {
		t.Fatalf("SPS=%+v valid=%v", d.sps, d.haveSPS)
	}
	for _, frame := range []uint{0, 1} {
		want := byte(0x41)
		if frame == 0 {
			want = 0x65
		}
		if got := d.startHeader(sliceStart(32, 2, frame, 6, true)); got != want {
			t.Fatalf("frame=%d header=%x", frame, got)
		}
	}
}

func TestKeyframeDetectorDiagnostic(t *testing.T) {
	wideSPS, err := hex.DecodeString("67640032ac1cd004801472")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name         string
		sps          []byte
		status       string
		keyframe     bool
		belowMinimum bool
	}{
		{"missing", nil, "no", false, false},
		{"usable", spsBaseline720p, "yes", false, false},
		{"unparsed", []byte{0x67}, "yes (could not be parsed)", false, false},
		{"unsupported", wideSPS, "yes (frame_num width 17 unsupported)", false, false},
		{"below minimum", nil, "yes (frame_num width -1 unsupported)", false, true},
		{"recognised", spsBaseline720p, "yes", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			old := os.Stdout
			os.Stdout = w
			defer func() { os.Stdout = old; r.Close(); w.Close() }()
			now := time.Unix(0, 0)
			d := keyframeDetector{label: "GP5_T6S8A3", now: func() time.Time { return now }}
			if c.sps != nil {
				d.observeNAL(c.sps)
			}
			if c.belowMinimum {
				// Model a corrupt SPS frame width overflowing a 32-bit int.
				d.sps = h264SPS{Log2MaxFrameNum: -1}
				d.spsSeen, d.haveSPS = true, true
				if got := d.startHeader(sliceStart(32, 2, 0, 4, false)); got != 0x41 {
					t.Fatalf("negative frame width: header=%x", got)
				}
			}
			d.startHeader(sliceStart(32, 0, 0, 4, false))
			if c.keyframe {
				d.startHeader([]byte{98, 0, 0xb2})
			}
			now = now.Add(9 * time.Second)
			d.startHeader(sliceStart(1, 0, 0, 4, false))
			if d.logged {
				t.Fatal("logged before deadline")
			}
			now = now.Add(time.Second)
			d.startHeader(sliceStart(32, 0, 0, 4, false))
			now = now.Add(time.Minute)
			d.startHeader(sliceStart(32, 0, 0, 4, false))
			w.Close()
			os.Stdout = old
			out, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("[GP5_T6S8A3] [video] no keyframe recognised in 10 s: FU start types 1×1 32×2; SPS seen: %s; unknown-type slice types: P×3\n", c.status)
			if c.belowMinimum {
				want = fmt.Sprintf("[GP5_T6S8A3] [video] no keyframe recognised in 10 s: FU start types 1×1 32×3; SPS seen: %s; unknown-type slice types: P×3 I×1\n", c.status)
			}
			if c.keyframe {
				want = ""
			}
			if string(out) != want {
				t.Fatalf("log=%q want=%q", out, want)
			}
			if strings.Count(string(out), "no keyframe") > 1 {
				t.Fatal("diagnostic repeated")
			}
		})
	}
}

func TestNALAssemblerUnknownKeyframeWithSPS(t *testing.T) {
	// No captured camera slice is available: use a real x264 SPS and build
	// synthetic headers with the frame_num width parsed from that SPS.
	sps, err := parseH264SPS(spsBaseline720p)
	if err != nil {
		t.Fatal(err)
	}
	var nals [][]byte
	a := newNALAssembler(func(n []byte) { nals = append(nals, n) })
	a.Push(spsBaseline720p)
	// Recognising the unknown-type I slice is intentional new behaviour;
	// the same FU type must still leave subsequent P/B slices non-IDR.
	for _, sliceType := range []uint{2, 0, 1} {
		a.Push(sliceStart(1, sliceType, 0, sps.Log2MaxFrameNum, false))
		a.Flush()
	}
	if len(nals) != 4 || nals[1][0] != 0x65 || nals[2][0] != 0x41 || nals[3][0] != 0x41 {
		t.Fatalf("NALs=%x", nals)
	}
}

func TestSPSHEADCompatibility(t *testing.T) {
	// Expected metadata from HEAD's parser, including out-of-spec headers
	// that it tolerates. Recording extra fields must not change acceptance.
	for _, c := range []struct {
		name, hex              string
		profile, level         int
		profileName, levelName string
		frameWidth             int
	}{
		{"reserved bits", "67640132acb40120051c80", 100, 50, "High", "5.0", 4},
		{"profile", "67000032da0090028e40", 0, 50, "profile 0", "5.0", 4},
		{"level", "67640000acb40120051c80", 100, 0, "High", "", 4},
		{"frame width", "67640032ac1cd004801472", 100, 50, "High", "5.0", 17},
	} {
		t.Run(c.name, func(t *testing.T) {
			nal, err := hex.DecodeString(c.hex)
			if err != nil {
				t.Fatal(err)
			}
			s, err := parseH264SPS(nal)
			if err != nil {
				t.Fatal(err)
			}
			if s.Width != 2304 || s.Height != 1296 || s.ProfileIDC != c.profile || s.LevelIDC != c.level || s.POCType != 2 || s.FPS != 0 || s.profileName() != c.profileName || s.levelName() != c.levelName || s.Log2MaxFrameNum != c.frameWidth {
				t.Fatalf("metadata differs from HEAD: %+v", s)
			}
			d := keyframeDetector{}
			d.observeNAL(nal)
			want := byte(0x65)
			if c.frameWidth > 16 {
				want = 0x41
			}
			if got := d.startHeader(sliceStart(32, 2, 0, c.frameWidth, false)); got != want {
				t.Fatalf("header=%x want=%x", got, want)
			}
		})
	}
}
