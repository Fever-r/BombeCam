package bridge

import "fmt"

// VideoInfo describes the camera's H.264 stream as BombeCam publishes it.
type VideoInfo struct {
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	FPS     float64 `json:"fps"`               // measured from the stream (0 until known)
	SPSFPS  float64 `json:"sps_fps,omitempty"` // declared in the SPS timing info, if any
	Profile string  `json:"profile,omitempty"`
	Level   string  `json:"level,omitempty"`
	POCType int     `json:"poc_type"`
}

// h264SPS holds the fields BombeCam needs from a sequence parameter set.
type h264SPS struct {
	ProfileIDC          int
	LevelIDC            int
	ChromaFormat        int
	Log2MaxFrameNum     int
	SeparateColourPlane bool
	POCType             int
	Width               int
	Height              int
	// FPS from the VUI timing info (time_scale / (2 * num_units_in_tick)); 0 if absent.
	FPS float64
}

func (s h264SPS) profileName() string {
	switch s.ProfileIDC {
	case 66:
		return "Baseline"
	case 77:
		return "Main"
	case 88:
		return "Extended"
	case 100:
		return "High"
	case 110:
		return "High 10"
	case 122:
		return "High 4:2:2"
	case 244:
		return "High 4:4:4"
	}
	return fmt.Sprintf("profile %d", s.ProfileIDC)
}

func (s h264SPS) levelName() string {
	if s.LevelIDC <= 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d", s.LevelIDC/10, s.LevelIDC%10)
}

// bitReader reads an RBSP (emulation-prevention bytes already removed). The
// first read past the end sets err; later reads return zero.
type bitReader struct {
	b   []byte
	pos int // bit position
	err error
}

func (r *bitReader) bit() uint {
	if r.err != nil {
		return 0
	}
	if r.pos >= len(r.b)*8 {
		r.err = fmt.Errorf("sps: truncated")
		return 0
	}
	v := (r.b[r.pos/8] >> (7 - uint(r.pos%8))) & 1
	r.pos++
	return uint(v)
}

func (r *bitReader) bits(n int) uint {
	var v uint
	for i := 0; i < n; i++ {
		v = v<<1 | r.bit()
	}
	return v
}

// ue reads an unsigned Exp-Golomb value.
func (r *bitReader) ue() uint {
	zeros := 0
	for r.bit() == 0 {
		if r.err != nil {
			return 0
		}
		zeros++
		if zeros > 31 {
			r.err = fmt.Errorf("sps: bad exp-golomb value")
			return 0
		}
	}
	if zeros == 0 {
		return 0
	}
	return (1 << uint(zeros)) - 1 + r.bits(zeros)
}

// se reads a signed Exp-Golomb value.
func (r *bitReader) se() int {
	v := r.ue()
	if v%2 == 1 {
		return int(v+1) / 2
	}
	return -int(v / 2)
}

// unescapeRBSP removes emulation-prevention bytes (00 00 03 -> 00 00).
func unescapeRBSP(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

func skipScalingList(r *bitReader, size int) {
	last, next := 8, 8
	for j := 0; j < size && r.err == nil; j++ {
		if next != 0 {
			next = (last + r.se() + 256) % 256
		}
		if next != 0 {
			last = next
		}
	}
}

// parseH264SPS parses an H.264 SPS NAL unit (including its 1-byte NAL header).
func parseH264SPS(nal []byte) (h264SPS, error) {
	var s h264SPS
	if len(nal) < 4 || h264NALType(nal) != 7 {
		return s, fmt.Errorf("sps: not an SPS")
	}
	r := &bitReader{b: unescapeRBSP(nal[1:])}
	s.ProfileIDC = int(r.bits(8))
	r.bits(8) // constraint flags + reserved
	s.LevelIDC = int(r.bits(8))
	r.ue() // seq_parameter_set_id
	s.ChromaFormat = 1
	switch s.ProfileIDC {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		s.ChromaFormat = int(r.ue())
		if s.ChromaFormat == 3 {
			s.SeparateColourPlane = r.bit() == 1
		}
		r.ue()            // bit_depth_luma_minus8
		r.ue()            // bit_depth_chroma_minus8
		r.bit()           // qpprime_y_zero_transform_bypass_flag
		if r.bit() == 1 { // seq_scaling_matrix_present_flag
			n := 8
			if s.ChromaFormat == 3 {
				n = 12
			}
			for i := 0; i < n; i++ {
				if r.bit() == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					skipScalingList(r, size)
				}
			}
		}
	}
	s.Log2MaxFrameNum = int(r.ue()) + 4
	s.POCType = int(r.ue())
	switch s.POCType {
	case 0:
		r.ue() // log2_max_pic_order_cnt_lsb_minus4
	case 1:
		r.bit() // delta_pic_order_always_zero_flag
		r.se()  // offset_for_non_ref_pic
		r.se()  // offset_for_top_to_bottom_field
		n := r.ue()
		for i := uint(0); i < n && i < 256 && r.err == nil; i++ {
			r.se()
		}
	}
	r.ue()  // max_num_ref_frames
	r.bit() // gaps_in_frame_num_value_allowed_flag
	wMbs := int(r.ue()) + 1
	hMapUnits := int(r.ue()) + 1
	frameMbsOnly := int(r.bit())
	if frameMbsOnly == 0 {
		r.bit() // mb_adaptive_frame_field_flag
	}
	r.bit() // direct_8x8_inference_flag
	var cl, cr, ct, cb int
	if r.bit() == 1 { // frame_cropping_flag
		cl, cr = int(r.ue()), int(r.ue())
		ct, cb = int(r.ue()), int(r.ue())
	}
	if r.err != nil {
		return h264SPS{}, r.err
	}
	chroma := s.ChromaFormat
	if s.SeparateColourPlane {
		chroma = 0
	}
	cropX, cropY := 1, 2-frameMbsOnly
	switch chroma {
	case 1:
		cropX, cropY = 2, 2*(2-frameMbsOnly)
	case 2:
		cropX, cropY = 2, 2-frameMbsOnly
	case 3:
		cropX, cropY = 1, 2-frameMbsOnly
	}
	s.Width = wMbs*16 - cropX*(cl+cr)
	s.Height = (2-frameMbsOnly)*hMapUnits*16 - cropY*(ct+cb)
	if s.Width <= 0 || s.Height <= 0 || s.Width > 16384 || s.Height > 16384 {
		return h264SPS{}, fmt.Errorf("sps: implausible size %dx%d", s.Width, s.Height)
	}

	// VUI: only the timing info is of interest. A VUI cut short keeps the size.
	if r.bit() == 1 { // vui_parameters_present_flag
		if r.bit() == 1 { // aspect_ratio_info_present_flag
			if r.bits(8) == 255 {
				r.bits(32) // sar_width, sar_height
			}
		}
		if r.bit() == 1 { // overscan_info_present_flag
			r.bit()
		}
		if r.bit() == 1 { // video_signal_type_present_flag
			r.bits(4)
			if r.bit() == 1 { // colour_description_present_flag
				r.bits(24)
			}
		}
		if r.bit() == 1 { // chroma_loc_info_present_flag
			r.ue()
			r.ue()
		}
		if r.bit() == 1 { // timing_info_present_flag
			units := r.bits(32)
			scale := r.bits(32)
			if r.err == nil && units > 0 && scale > 0 {
				s.FPS = float64(scale) / float64(2*units)
			}
		}
	}
	return s, nil
}
