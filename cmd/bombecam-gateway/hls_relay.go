package main

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// handleHLSRelay serves the viewer's HLS fallback through the gateway
// (GET /api/v1/cameras/{id}/hls/<file>), so the browser stays on BombeCam's
// own origin and MediaMTX's HLS server doesn't have to answer other web
// pages (hlsAllowOrigin is the gateway only).
func handleHLSRelay(w http.ResponseWriter, r *http.Request, camID, file string, sm *StreamManager) {
	if _, ok := sm.GetCamera(camID); !ok {
		http.Error(w, "camera not found", http.StatusNotFound)
		return
	}
	if file == "" || strings.Contains(file, "..") || strings.HasPrefix(file, "/") {
		http.NotFound(w, r)
		return
	}
	target, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", sm.HLSPort()))
	if err != nil {
		http.Error(w, "bad HLS address", http.StatusInternalServerError)
		return
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/" + url.PathEscape(camID) + "/" + file
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = r.URL.RawQuery
			pr.Out.Host = target.Host
			// never forward the browser's session to MediaMTX
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("X-CSRF-Token")
			pr.Out.Header.Del("Origin")
			pr.Out.Header.Del("Referer")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "video server unreachable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}
