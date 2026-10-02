package bridge

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// TalkFormat is one way of sending the user's voice to the camera's speaker.
type TalkFormat struct {
	ID    string `json:"id"`
	Label string `json:"label"`

	codec   string // "aac", "pcmu", "pcma", "opus"
	rate    int    // encoder sample rate (Hz); 0 = the camera's own
	clock   uint32 // RTP clock (Hz); 0 = the camera's own
	pt      int    // RTP payload type; -1 = the negotiated one
	packing string // "adts", "raw", "rfc3640", "g711", "opus"
	samples uint32 // samples per packet at the encoder rate (pacing)
	// markerEach sets the RTP marker on every packet, as the camera does on
	// its own audio; otherwise only on the first packet of a talkspurt.
	markerEach bool
	// mirror uses the camera's observed RTP step per frame (AAC only).
	mirror bool
}

// NeedsFFmpeg reports whether the format is encoded with FFmpeg (G.711 is
// encoded by BombeCam itself).
func (f TalkFormat) NeedsFFmpeg() bool { return f.codec == "aac" || f.codec == "opus" }

// DefaultTalkFormatID is the established format 1 used by Talk.
const DefaultTalkFormatID = "aac-adts"

var talkFormats = []TalkFormat{
	{ID: "aac-adts", Label: "AAC with ADTS header (the camera's own format)",
		codec: "aac", pt: -1, packing: "adts", samples: 1024, markerEach: true, mirror: true},
	{ID: "aac-raw", Label: "AAC without header",
		codec: "aac", pt: -1, packing: "raw", samples: 1024, markerEach: true, mirror: true},
	{ID: "aac-rfc3640", Label: "AAC, standard RTP packing (RFC 3640)",
		codec: "aac", pt: -1, packing: "rfc3640", samples: 1024, markerEach: true, mirror: true},
	{ID: "aac-16k", Label: "AAC 16 kHz, payload type 97",
		codec: "aac", rate: 16000, clock: 16000, pt: 97, packing: "adts", samples: 1024, markerEach: true},
	{ID: "pcmu", Label: "G.711 µ-law",
		codec: "pcmu", rate: 8000, clock: 8000, pt: 0, packing: "g711", samples: 160},
	{ID: "pcma", Label: "G.711 A-law",
		codec: "pcma", rate: 8000, clock: 8000, pt: 8, packing: "g711", samples: 160},
	{ID: "pcma-96", Label: "G.711 A-law, payload type 96",
		codec: "pcma", rate: 8000, clock: 8000, pt: -1, packing: "g711", samples: 160},
	{ID: "opus", Label: "Opus",
		codec: "opus", rate: 48000, clock: 48000, pt: 111, packing: "opus", samples: 960},
}

// TalkFormats lists the supported encoder formats.
func TalkFormats() []TalkFormat {
	out := make([]TalkFormat, len(talkFormats))
	copy(out, talkFormats)
	return out
}

// TalkFormatByID returns the format with this ID.
func TalkFormatByID(id string) (TalkFormat, bool) {
	for _, f := range talkFormats {
		if f.ID == id {
			return f, true
		}
	}
	return TalkFormat{}, false
}

// talkPacket is one RTP payload ready to send.
type talkPacket struct {
	payload []byte
	samples uint32 // at the encoder rate, for pacing
}

// ---- AAC packing ----

// adtsHeaderLen is 7, or 9 when the frame carries a CRC.
func adtsHeaderLen(frame []byte) int {
	if len(frame) >= 2 && frame[1]&0x01 == 0 {
		return 9
	}
	return 7
}

// packAAC turns one ADTS frame into the payload for the given packing.
func packAAC(frame []byte, packing string) []byte {
	switch packing {
	case "raw":
		return append([]byte(nil), frame[adtsHeaderLen(frame):]...)
	case "rfc3640":
		// mpeg4-generic, AAC-hbr: AU-headers-length (16 bits), then one
		// AU-header of 13-bit size and 3-bit index.
		raw := frame[adtsHeaderLen(frame):]
		n := len(raw)
		out := make([]byte, 4+n)
		out[0], out[1] = 0x00, 0x10
		out[2] = byte(n >> 5)
		out[3] = byte(n&0x1F) << 3
		copy(out[4:], raw)
		return out
	default:
		return append([]byte(nil), frame...)
	}
}

// ---- G.711 (encoded here, no FFmpeg) ----

// linearToULaw encodes one 16-bit sample as G.711 µ-law.
func linearToULaw(s int16) byte {
	const bias = 0x84
	const clip = 32635
	x := int(s)
	sign := 0
	if x < 0 {
		x = -x
		sign = 0x80
	}
	if x > clip {
		x = clip
	}
	x += bias
	exp := 7
	for mask := 0x4000; x&mask == 0 && exp > 0; mask >>= 1 {
		exp--
	}
	mant := (x >> (exp + 3)) & 0x0F
	return ^byte(sign | exp<<4 | mant)
}

// linearToALaw encodes one 16-bit sample as G.711 A-law.
func linearToALaw(s int16) byte {
	x := int(s) >> 3 // 13-bit
	sign := 0x80
	if x < 0 {
		x = -x - 1
		sign = 0
	}
	var out int
	if x < 32 {
		out = x >> 1
	} else {
		exp := 1
		for v := x >> 5; v > 1 && exp < 7; v >>= 1 {
			exp++
		}
		if x >= 4096 {
			exp, x = 7, 4095
		}
		out = exp<<4 | (x>>exp)&0x0F
	}
	return byte(out|sign) ^ 0x55
}

// g711Encoder turns 16 kHz s16le PCM into 20 ms G.711 packets at 8 kHz.
type g711Encoder struct {
	alaw    bool
	out     chan<- talkPacket
	pending []byte  // odd byte left from the last write
	buf     []int16 // 8 kHz samples waiting for a full packet
	prev    int     // last 16 kHz sample, for the anti-alias filter
	mu      sync.Mutex
	closed  bool
}

func (e *g711Encoder) Write(pcm []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return 0, io.ErrClosedPipe
	}
	data := append(e.pending, pcm...)
	n := len(data) / 4 * 4 // pairs of 16 kHz samples
	for i := 0; i < n; i += 4 {
		a := int(int16(binary.LittleEndian.Uint16(data[i:])))
		b := int(int16(binary.LittleEndian.Uint16(data[i+2:])))
		// half-band low-pass [1/4 1/2 1/4] then keep every second sample
		v := (e.prev + 2*a + b) / 4
		e.prev = b
		e.buf = append(e.buf, int16(v))
	}
	e.pending = append([]byte(nil), data[n:]...)
	for len(e.buf) >= 160 {
		p := make([]byte, 160)
		for i := 0; i < 160; i++ {
			if e.alaw {
				p[i] = linearToALaw(e.buf[i])
			} else {
				p[i] = linearToULaw(e.buf[i])
			}
		}
		e.buf = e.buf[160:]
		select {
		case e.out <- talkPacket{payload: p, samples: 160}:
		default: // pacer far behind: drop
		}
	}
	return len(pcm), nil
}

func (e *g711Encoder) Close() error {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	return nil
}

// ---- Ogg (Opus from FFmpeg) ----

// oggPacketReader returns the packets of an Ogg stream one at a time.
type oggPacketReader struct {
	r       io.Reader
	queue   [][]byte
	partial []byte
}

var errNotOgg = errors.New("not an Ogg page")

func (o *oggPacketReader) Next() ([]byte, error) {
	for len(o.queue) == 0 {
		if err := o.readPage(); err != nil {
			return nil, err
		}
	}
	p := o.queue[0]
	o.queue = o.queue[1:]
	return p, nil
}

func (o *oggPacketReader) readPage() error {
	hdr := make([]byte, 27)
	if _, err := io.ReadFull(o.r, hdr); err != nil {
		return err
	}
	if string(hdr[:4]) != "OggS" {
		return errNotOgg
	}
	nseg := int(hdr[26])
	segs := make([]byte, nseg)
	if _, err := io.ReadFull(o.r, segs); err != nil {
		return err
	}
	total := 0
	for _, s := range segs {
		total += int(s)
	}
	body := make([]byte, total)
	if _, err := io.ReadFull(o.r, body); err != nil {
		return err
	}
	off := 0
	for _, s := range segs {
		o.partial = append(o.partial, body[off:off+int(s)]...)
		off += int(s)
		if s < 255 {
			o.queue = append(o.queue, o.partial)
			o.partial = nil
		}
	}
	return nil
}

// isOpusHeader reports the OpusHead / OpusTags packets that start a stream.
func isOpusHeader(p []byte) bool {
	return len(p) >= 8 && (string(p[:8]) == "OpusHead" || string(p[:8]) == "OpusTags")
}

// ---- FFmpeg encoder arguments ----

// talkFFmpegArgs builds the encoder's command line. relabel > 0 plays the
// 16 kHz input as if it were recorded at relabel Hz before resampling to
// rate (see Viewer.talkPlan).
func talkFFmpegArgs(f TalkFormat, rate, relabel int) []string {
	// -probesize/-analyzeduration: without them FFmpeg reads about 2 s of
	// the pipe before its first output, and Talk started 2 s late.
	in := []string{"-hide_banner", "-loglevel", "error",
		"-probesize", "32", "-analyzeduration", "0",
		"-f", "s16le", "-ar", "16000", "-ac", "1", "-i", "pipe:0"}
	switch f.codec {
	case "opus":
		return append(in,
			"-c:a", "libopus", "-application", "voip", "-frame_duration", "20", "-b:a", "24k",
			"-ar", "48000", "-ac", "1", "-page_duration", "20000",
			"-flush_packets", "1", "-f", "ogg", "pipe:1")
	default: // aac
		if relabel > 0 {
			in = append(in, "-af", fmt.Sprintf("asetrate=%d,aresample=%d", relabel, rate))
		}
		return append(in,
			"-c:a", "aac", "-profile:a", "aac_low", "-b:a", "32k",
			"-ar", fmt.Sprintf("%d", rate), "-ac", "1",
			"-flush_packets", "1", "-f", "adts", "pipe:1")
	}
}

// ---- the audio track, with the payload type under our control ----

// talkTrack is the call's outgoing audio track. pion's static track rewrites
// every packet's payload type to the negotiated one; alternate formats need
// to send G.711 and Opus with their own, so it also keeps the binding's
// writer, SSRC and negotiated payload type.
type talkTrack struct {
	*webrtc.TrackLocalStaticRTP
	mu    sync.Mutex
	ws    webrtc.TrackLocalWriter
	ssrc  webrtc.SSRC
	pt    webrtc.PayloadType
	codec webrtc.RTPCodecParameters
}

func newTalkTrack() (*talkTrack, error) {
	t, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: "audio/AAC", ClockRate: 16000, Channels: 1},
		"AID",
		"Lo",
	)
	if err != nil {
		return nil, err
	}
	return &talkTrack{TrackLocalStaticRTP: t}, nil
}

// Bind records the negotiated writer, SSRC and payload type.
func (t *talkTrack) Bind(ctx webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	c, err := t.TrackLocalStaticRTP.Bind(ctx)
	if err == nil {
		t.mu.Lock()
		t.ws, t.ssrc, t.pt, t.codec = ctx.WriteStream(), ctx.SSRC(), c.PayloadType, c
		t.mu.Unlock()
	}
	return c, err
}

// Unbind forgets the writer.
func (t *talkTrack) Unbind(ctx webrtc.TrackLocalContext) error {
	t.mu.Lock()
	t.ws = nil
	t.mu.Unlock()
	return t.TrackLocalStaticRTP.Unbind(ctx)
}

// binding describes the negotiated send side, for the log.
func (t *talkTrack) binding() (bound bool, ssrc uint32, pt uint8, codec string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ws != nil, uint32(t.ssrc), uint8(t.pt),
		fmt.Sprintf("%s/%d", t.codec.MimeType, t.codec.ClockRate)
}

var errTalkNotBound = errors.New("the call's audio track is not connected yet")

// writeWithPT sends one packet; pt < 0 means the negotiated payload type.
func (t *talkTrack) writeWithPT(h rtp.Header, payload []byte, pt int) (int, error) {
	t.mu.Lock()
	ws, ssrc, npt := t.ws, t.ssrc, t.pt
	t.mu.Unlock()
	if ws == nil {
		return 0, errTalkNotBound
	}
	h.Version = 2
	h.SSRC = uint32(ssrc)
	if pt < 0 {
		h.PayloadType = uint8(npt)
	} else {
		h.PayloadType = uint8(pt)
	}
	return ws.WriteRTP(&h, payload)
}
