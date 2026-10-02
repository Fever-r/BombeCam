package bridge

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// ---- extract parameters from pion's offer SDP ----
func sdpField(sdp, prefix string) string {
	for _, ln := range strings.Split(sdp, "\n") {
		ln = strings.TrimRight(ln, "\r")
		if strings.HasPrefix(ln, prefix) {
			return strings.TrimPrefix(ln, prefix)
		}
	}
	return ""
}

var reFp = regexp.MustCompile(`a=fingerprint:(sha-256 [0-9A-Fa-f:]+)`)

func extractCrypto(sdp string) (ufrag, pwd, fp string) {
	ufrag = sdpField(sdp, "a=ice-ufrag:")
	pwd = sdpField(sdp, "a=ice-pwd:")
	if m := reFp.FindStringSubmatch(sdp); m != nil {
		fp = m[1]
	}
	return
}

var reSsrc = regexp.MustCompile(`(?m)^a=ssrc:(\d+)\s+cname:(\S+)`)

func extractAudioSSRCAndCNAME(sdp string) (uint32, string) {
	if m := reSsrc.FindStringSubmatch(sdp); len(m) >= 3 {
		if ssrc, err := strconv.ParseUint(m[1], 10, 32); err == nil {
			return uint32(ssrc), m[2]
		}
	}
	return 0, ""
}

func randInts(n int) []int {
	out := make([]int, n)
	for i := range out {
		b, _ := rand.Int(rand.Reader, big.NewInt(256))
		out[i] = int(b.Int64())
	}
	return out
}
func randUint32() uint32 {
	b, _ := rand.Int(rand.Reader, big.NewInt(1<<31))
	return uint32(b.Int64())
}

// ---- ENCODE: build the compact WebrtcSdp offer (matches the app's structure) ----
func encodeOffer(ufrag, pwd, fp string, audioSSRC uint32, cname string) (webrtcSdp string, outSSRC uint32, outCname string) {
	if audioSSRC == 0 {
		audioSSRC = randUint32()
	}
	if cname == "" {
		cname = "Lo"
	}
	sk := randInts(30)
	iO := fmt.Sprintf("%d 2 IN IP4 127.0.0.1", randUint32())
	m := map[string]any{
		"video": map[string]any{
			"mid": 1, "sr": 0, "dc": 1, "pt": 127, "fb": 35, "fbc": 22, "rm": 1, "rr": 1,
			"sc": []any{}, "ce": "", "md": "", "m": 9, "pts": 90000,
			"rtx": []int{123, 122}, "red": 126, "fec": 125,
			"fmtp": []string{"123 apt=127", "122 apt=126"},
		},
		"audio": map[string]any{
			"mid": 0, "sr": 1, "dc": 1, "pt": 97, "8000": 96, "rm": 1,
			"sc": []string{fmt.Sprintf("%d", audioSSRC)}, "ce": cname, "md": "Lo AID", "pts": 16000, "m": 9,
		},
		"com": map[string]any{
			"v": 1, "iO": iO, "is": 1, "iT": "0 0", "iG": "0 1", "isc": "WMS Lo",
			"I": "IP4 0.0.0.0", "r": "9 IN IP4 0.0.0.0", "u": ufrag, "p": pwd,
			"o": "trickle renomination", "ft": fp, "s": "actpass",
			"ll": 1, "abs": 7, "tcc": 3, "pp": 10, "sk": sk, "rk": sk,
		},
	}
	b, _ := json.Marshal(m)
	return "00\r\n" + string(b), audioSSRC, cname
}

// ---- DECODE: turn the compact WebrtcSdp (camera answer) into a real SDP for pion ----
type compactMline struct {
	Mid int    `json:"mid"`
	Pt  int    `json:"pt"`
	Pts int    `json:"pts"`
	Sc  any    `json:"sc"`
	Ce  string `json:"ce"`
	Md  string `json:"md"`
}
type compactCom struct {
	IO  string `json:"iO"`
	IT  string `json:"iT"`
	IG  string `json:"iG"`
	Isc string `json:"isc"`
	R   string `json:"r"`
	Ic  string `json:"ic"`
	U   string `json:"u"`
	P   string `json:"p"`
	O   string `json:"o"`
	Ft  string `json:"ft"`
	S   string `json:"s"`
}
type compactSDP struct {
	Com   compactCom   `json:"com"`
	Audio compactMline `json:"audio"`
	Video compactMline `json:"video"`
}

func ssrcStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%d", int64(x))
	}
	return "0"
}

func decodeAnswer(webrtcSdp string) (string, error) {
	s := webrtcSdp
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:] // strip the "00\r\n" prefix
	}
	var c compactSDP
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return "", fmt.Errorf("decode answer json: %v", err)
	}
	o := c.Com.IO
	if !strings.HasPrefix(o, "-") {
		o = "- " + o
	}
	fp := c.Com.Ft
	setup := c.Com.S
	if setup == "" {
		setup = "active"
	}
	ice := c.Com.O
	if ice == "" {
		ice = "trickle"
	}
	iG := c.Com.IG
	if !strings.HasPrefix(iG, "BUNDLE") {
		iG = "BUNDLE " + iG
	}
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\r\n", a...) }
	w("v=0")
	w("o=%s", o)
	w("s=-")
	w("t=0 0")
	w("a=group:%s", iG)
	w("a=msid-semantic: %s", strings.TrimSpace(c.Com.Isc))
	// shared helper for one m-line
	mline := func(kind string, pt, clock int, dir, codec string, extraFb []string, ml compactMline) {
		w("m=%s 9 UDP/TLS/RTP/SAVPF %d", kind, pt)
		w("c=IN IP4 0.0.0.0")
		w("a=rtcp:9 IN IP4 0.0.0.0")
		if c.Com.Ic != "" {
			w("a=candidate:%s", c.Com.Ic)
		}
		w("a=ice-ufrag:%s", c.Com.U)
		w("a=ice-pwd:%s", c.Com.P)
		w("a=ice-options:%s", ice)
		w("a=fingerprint:%s", fp)
		w("a=setup:%s", setup)
		w("a=mid:%d", ml.Mid)
		w("a=%s", dir)
		w("a=rtcp-mux")
		w("a=rtpmap:%d %s/%d", pt, codec, clock)
		for _, fb := range extraFb {
			w("a=rtcp-fb:%d %s", pt, fb)
		}
		if s := ssrcStr(ml.Sc); s != "0" && s != "" {
			w("a=ssrc:%s cname:%s", s, ml.Ce)
		}
	}
	// audio (mid 0) sendrecv AAC (kept in the answer, not muxed yet)
	ac := c.Audio.Pts
	if ac == 0 {
		ac = 16000
	}
	mline("audio", c.Audio.Pt, ac, "sendrecv", "AAC", []string{"transport-cc"}, c.Audio)
	// video (mid 1) sendonly H265
	vc := c.Video.Pts
	if vc == 0 {
		vc = 90000
	}
	mline("video", c.Video.Pt, vc, "sendonly", "H265", []string{"transport-cc", "nack", "nack pli", "goog-remb"}, c.Video)
	return b.String(), nil
}

// compactAudioSummary returns the audio section of a compact SDP as JSON (for
// the log: it tells which codec/clock the camera chose for two-way audio; it
// holds no ICE credentials).
func compactAudioSummary(webrtcSdp string) string {
	s := webrtcSdp
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return "?"
	}
	if a, ok := m["audio"]; ok {
		return string(a)
	}
	return "none"
}
