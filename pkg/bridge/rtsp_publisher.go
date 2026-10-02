package bridge

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

// rtspPublisher is a minimal RTSP client that publishes H.264 (and optionally
// AAC) to MediaMTX over TCP-interleaved RTP (ANNOUNCE/SETUP/RECORD). It lets
// the gateway publish camera media without an external FFmpeg process.
type rtspPublisher struct {
	conn    net.Conn
	bw      *bufio.Writer
	wmu     sync.Mutex
	done    chan struct{}
	once    sync.Once
	readErr error

	video     codecs.H264Payloader
	videoSeq  uint16
	videoSSRC uint32

	hasAudio   bool
	audioRate  int
	audioSeq   uint16
	audioSSRC  uint32
	writeLimit time.Duration

	// Optional G.711 µ-law copy of the audio (trackID=2) for WebRTC viewers,
	// which cannot play AAC. MediaMTX serves each reader the track it supports.
	hasG711  bool
	g711Seq  uint16
	g711SSRC uint32
}

// aacConfig describes the published AAC track.
type aacConfig struct {
	SampleRate  int
	Channels    int
	FreqIndex   byte
	ChannelConf byte
}

// audioSpecificConfig returns the 2-byte MPEG-4 AudioSpecificConfig (AAC-LC).
func (c aacConfig) audioSpecificConfig() []byte {
	v := uint16(2)<<11 | uint16(c.FreqIndex&0x0f)<<7 | uint16(c.ChannelConf&0x0f)<<3
	return []byte{byte(v >> 8), byte(v)}
}

// aacConfigFromADTS derives the track configuration from an ADTS header.
func aacConfigFromADTS(b []byte) (aacConfig, bool) {
	if len(b) < 7 || b[0] != 0xFF || b[1]&0xF0 != 0xF0 {
		return aacConfig{}, false
	}
	idx := (b[2] >> 2) & 0x0f
	rate, ok := sampleRateTable[idx]
	if !ok {
		return aacConfig{}, false
	}
	ch := ((b[2] & 0x01) << 2) | ((b[3] >> 6) & 0x03)
	if ch == 0 {
		ch = 1
	}
	return aacConfig{SampleRate: rate, Channels: int(ch), FreqIndex: idx, ChannelConf: ch}, true
}

// splitADTS splits a buffer of concatenated ADTS frames into raw AAC access
// units (headers stripped). Malformed trailing data is ignored.
func splitADTS(b []byte) [][]byte {
	var out [][]byte
	for len(b) >= 7 {
		if b[0] != 0xFF || b[1]&0xF0 != 0xF0 {
			break
		}
		hdrLen := 7
		if b[1]&0x01 == 0 {
			hdrLen = 9
		}
		frameLen := (int(b[3]&0x03) << 11) | (int(b[4]) << 3) | (int(b[5]&0xE0) >> 5)
		if frameLen <= hdrLen || frameLen > len(b) {
			break
		}
		out = append(out, b[hdrLen:frameLen])
		b = b[frameLen:]
	}
	return out
}

// dialRTSPPublisher connects to rtspURL and starts a RECORD session.
// sps/pps are advertised in the SDP; audio may be nil for video-only.
func dialRTSPPublisher(ctx context.Context, rtspURL string, sps, pps []byte, audio *aacConfig, withG711 ...bool) (*rtspPublisher, error) {
	g711 := audio != nil && len(withG711) > 0 && withG711[0]
	u, err := url.Parse(rtspURL)
	if err != nil || u.Scheme != "rtsp" {
		return nil, fmt.Errorf("invalid rtsp url %q", rtspURL)
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "554")
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	p := &rtspPublisher{
		conn:       conn,
		bw:         bufio.NewWriterSize(conn, 64*1024),
		done:       make(chan struct{}),
		videoSeq:   uint16(rand.Intn(65535)),
		videoSSRC:  rand.Uint32(),
		audioSeq:   uint16(rand.Intn(65535)),
		audioSSRC:  rand.Uint32(),
		g711Seq:    uint16(rand.Intn(65535)),
		g711SSRC:   rand.Uint32(),
		writeLimit: 5 * time.Second,
	}
	p.video.DisableStapA = true
	br := bufio.NewReader(conn)
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	base := strings.TrimRight(rtspURL, "/")
	cseq := 0
	request := func(method, target string, headers map[string]string, body []byte) (map[string]string, error) {
		cseq++
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s %s RTSP/1.0\r\nCSeq: %d\r\nUser-Agent: BombeCam\r\n", method, target, cseq)
		for k, v := range headers {
			fmt.Fprintf(&sb, "%s: %s\r\n", k, v)
		}
		if len(body) > 0 {
			fmt.Fprintf(&sb, "Content-Length: %d\r\n", len(body))
		}
		sb.WriteString("\r\n")
		if _, err := conn.Write(append([]byte(sb.String()), body...)); err != nil {
			return nil, err
		}
		return readRTSPResponse(br, method)
	}

	sdp := buildPublishSDP(sps, pps, audio, g711)
	if _, err := request("ANNOUNCE", base, map[string]string{"Content-Type": "application/sdp"}, []byte(sdp)); err != nil {
		conn.Close()
		return nil, err
	}
	resp, err := request("SETUP", base+"/trackID=0", map[string]string{"Transport": "RTP/AVP/TCP;unicast;interleaved=0-1;mode=record"}, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	session := strings.TrimSpace(strings.Split(resp["Session"], ";")[0])
	if session == "" {
		conn.Close()
		return nil, fmt.Errorf("rtsp SETUP returned no session")
	}
	if audio != nil {
		if _, err := request("SETUP", base+"/trackID=1", map[string]string{"Transport": "RTP/AVP/TCP;unicast;interleaved=2-3;mode=record", "Session": session}, nil); err != nil {
			conn.Close()
			return nil, err
		}
		p.hasAudio = true
		p.audioRate = audio.SampleRate
	}
	if g711 {
		if _, err := request("SETUP", base+"/trackID=2", map[string]string{"Transport": "RTP/AVP/TCP;unicast;interleaved=4-5;mode=record", "Session": session}, nil); err != nil {
			conn.Close()
			return nil, err
		}
		p.hasG711 = true
	}
	if _, err := request("RECORD", base, map[string]string{"Session": session, "Range": "npt=0.000-"}, nil); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})

	// Drain whatever the server sends (RTCP receiver reports, responses) so
	// its writes never block; a read error means the session is gone.
	go func() {
		_, err := io.Copy(io.Discard, br)
		if err == nil {
			err = io.EOF
		}
		p.closeWith(err)
	}()
	return p, nil
}

func buildPublishSDP(sps, pps []byte, audio *aacConfig, g711 bool) string {
	var sb strings.Builder
	sb.WriteString("v=0\r\no=- 0 0 IN IP4 127.0.0.1\r\ns=BombeCam\r\nc=IN IP4 0.0.0.0\r\nt=0 0\r\n")
	sb.WriteString("m=video 0 RTP/AVP 96\r\na=rtpmap:96 H264/90000\r\n")
	fmtp := "packetization-mode=1"
	if len(sps) >= 4 && len(pps) > 0 {
		fmtp += ";profile-level-id=" + strings.ToUpper(hex.EncodeToString(sps[1:4]))
		fmtp += ";sprop-parameter-sets=" + base64.StdEncoding.EncodeToString(sps) + "," + base64.StdEncoding.EncodeToString(pps)
	}
	sb.WriteString("a=fmtp:96 " + fmtp + "\r\na=control:trackID=0\r\n")
	if audio != nil {
		fmt.Fprintf(&sb, "m=audio 0 RTP/AVP 97\r\na=rtpmap:97 mpeg4-generic/%d/%d\r\n", audio.SampleRate, audio.Channels)
		fmt.Fprintf(&sb, "a=fmtp:97 profile-level-id=1;mode=AAC-hbr;sizelength=13;indexlength=3;indexdeltalength=3;config=%s\r\n",
			strings.ToUpper(hex.EncodeToString(audio.audioSpecificConfig())))
		sb.WriteString("a=control:trackID=1\r\n")
	}
	if audio != nil && g711 {
		sb.WriteString("m=audio 0 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\na=control:trackID=2\r\n")
	}
	return sb.String()
}

func readRTSPResponse(br *bufio.Reader, method string) (map[string]string, error) {
	tp := textproto.NewReader(br)
	for {
		status, err := tp.ReadLine()
		if err != nil {
			return nil, fmt.Errorf("rtsp %s: %w", method, err)
		}
		if strings.HasPrefix(status, "$") || status == "" {
			continue
		}
		parts := strings.SplitN(status, " ", 3)
		if len(parts) < 2 || !strings.HasPrefix(parts[0], "RTSP/") {
			return nil, fmt.Errorf("rtsp %s: unexpected response %q", method, status)
		}
		code, _ := strconv.Atoi(parts[1])
		hdr, err := tp.ReadMIMEHeader()
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("rtsp %s: %w", method, err)
		}
		out := map[string]string{}
		for k, v := range hdr {
			if len(v) > 0 {
				out[k] = v[0]
			}
		}
		if n, _ := strconv.Atoi(out["Content-Length"]); n > 0 {
			if _, err := io.CopyN(io.Discard, br, int64(n)); err != nil {
				return nil, err
			}
		}
		if code != 200 {
			return nil, fmt.Errorf("rtsp %s rejected: %s", method, status)
		}
		return out, nil
	}
}

func (p *rtspPublisher) closeWith(err error) {
	p.once.Do(func() {
		p.readErr = err
		_ = p.conn.Close()
		close(p.done)
	})
}

// Done is closed when the RTSP session ends.
func (p *rtspPublisher) Done() <-chan struct{} { return p.done }

// Close ends the session.
func (p *rtspPublisher) Close() { p.closeWith(fmt.Errorf("closed")) }

func (p *rtspPublisher) writeInterleaved(channel byte, pkts []*rtp.Packet) error {
	select {
	case <-p.done:
		return fmt.Errorf("rtsp session closed: %v", p.readErr)
	default:
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(p.writeLimit))
	for _, pkt := range pkts {
		raw, err := pkt.Marshal()
		if err != nil {
			return err
		}
		var hdr [4]byte
		hdr[0] = '$'
		hdr[1] = channel
		binary.BigEndian.PutUint16(hdr[2:], uint16(len(raw)))
		if _, err := p.bw.Write(hdr[:]); err != nil {
			p.closeWith(err)
			return err
		}
		if _, err := p.bw.Write(raw); err != nil {
			p.closeWith(err)
			return err
		}
	}
	if err := p.bw.Flush(); err != nil {
		p.closeWith(err)
		return err
	}
	return nil
}

// WriteH264 publishes one access unit with a 90 kHz timestamp.
func (p *rtspPublisher) WriteH264(nalus [][]byte, ts uint32) error {
	var pkts []*rtp.Packet
	for _, n := range nalus {
		if len(n) == 0 {
			continue
		}
		for _, pl := range p.video.Payload(1400, n) {
			p.videoSeq++
			pkts = append(pkts, &rtp.Packet{
				Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: p.videoSeq, Timestamp: ts, SSRC: p.videoSSRC},
				Payload: pl,
			})
		}
	}
	if len(pkts) == 0 {
		return nil
	}
	pkts[len(pkts)-1].Marker = true
	return p.writeInterleaved(0, pkts)
}

// WriteAAC publishes raw AAC access units (no ADTS headers); ts is in
// sample-rate units and is the timestamp of the first unit.
func (p *rtspPublisher) WriteAAC(units [][]byte, ts uint32) error {
	if !p.hasAudio {
		return nil
	}
	pkts := make([]*rtp.Packet, 0, len(units))
	for i, au := range units {
		if len(au) == 0 || len(au) > 8191 {
			continue
		}
		pl := make([]byte, 4+len(au))
		pl[0], pl[1] = 0x00, 0x10 // AU-headers-length: 16 bits
		pl[2] = byte(len(au) >> 5)
		pl[3] = byte(len(au)&0x1f) << 3
		copy(pl[4:], au)
		p.audioSeq++
		pkts = append(pkts, &rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: 97, SequenceNumber: p.audioSeq, Timestamp: ts + uint32(i*1024), SSRC: p.audioSSRC, Marker: true},
			Payload: pl,
		})
	}
	if len(pkts) == 0 {
		return nil
	}
	return p.writeInterleaved(2, pkts)
}

// WriteG711 publishes µ-law samples (8 kHz mono); ts is in 8 kHz units.
func (p *rtspPublisher) WriteG711(samples []byte, ts uint32) error {
	if !p.hasG711 || len(samples) == 0 {
		return nil
	}
	p.g711Seq++
	return p.writeInterleaved(4, []*rtp.Packet{{
		Header:  rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: p.g711Seq, Timestamp: ts, SSRC: p.g711SSRC},
		Payload: samples,
	}})
}
