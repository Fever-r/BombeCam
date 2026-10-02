package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestGatewayInfo_AndAutostart(t *testing.T) {
	_, _, _, mux := setupTestEnvironment()
	t.Setenv("BOMBECAM_IN_DOCKER", "1")

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/gateway/info", nil))
	var info struct {
		Platform  string         `json:"platform"`
		Docker    bool           `json:"docker"`
		Autostart map[string]any `json:"autostart"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil || rr.Code != http.StatusOK {
		t.Fatalf("info: %d %s", rr.Code, rr.Body)
	}
	if info.Platform != runtime.GOOS || info.Docker != (runtime.GOOS == "linux") {
		t.Fatalf("info %+v", info)
	}
	if runtime.GOOS != "windows" {
		if info.Autostart["supported"] != false {
			t.Fatalf("autostart %+v", info.Autostart)
		}
		rr = httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/gateway/autostart", strings.NewReader(`{"enabled":true}`)))
		if rr.Code != http.StatusNotImplemented {
			t.Fatalf("autostart POST off Windows: %d", rr.Code)
		}
	}
}

func TestAppIcon_IsValidICO(t *testing.T) {
	ico := appIcon()
	if len(ico) < 6 || !bytes.Equal(ico[:4], []byte{0, 0, 1, 0}) {
		t.Fatal("not an ICO header")
	}
	n := int(binary.LittleEndian.Uint16(ico[4:]))
	if n != 3 {
		t.Fatalf("%d images", n)
	}
	for i := 0; i < n; i++ {
		e := ico[6+16*i:]
		size := int(binary.LittleEndian.Uint32(e[8:]))
		off := int(binary.LittleEndian.Uint32(e[12:]))
		img, err := png.Decode(bytes.NewReader(ico[off : off+size]))
		if err != nil {
			t.Fatalf("image %d: %v", i, err)
		}
		if img.Bounds().Dx() != int(e[0]) {
			t.Fatalf("image %d is %d px, directory says %d", i, img.Bounds().Dx(), e[0])
		}
	}
	rr := httptest.NewRecorder()
	handleFavicon(rr, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	if rr.Header().Get("Content-Type") != "image/x-icon" || !bytes.Equal(rr.Body.Bytes(), ico) {
		t.Fatal("favicon")
	}
}
