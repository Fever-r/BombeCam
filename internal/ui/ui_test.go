package ui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUI_Handler_ServesEmbeddedAssets(t *testing.T) {
	handler := Handler()

	tests := []struct {
		path         string
		expectedCode int
		expectedType string
		contains     string
	}{
		{"/", http.StatusOK, "text/html", "<title>BombeCam</title>"},
		{"/index.html", http.StatusOK, "text/html", "<title>BombeCam</title>"},
		{"/style.css", http.StatusOK, "text/css", "--bg-body"},
		{"/app.js", http.StatusOK, "javascript", "fetchCSRFToken"},
		{"/hls.min.js", http.StatusOK, "javascript", "Hls"},
		{"/blocking-options.html", http.StatusOK, "text/html", "Ways to block camera traffic"},
		{"/integrations.html", http.StatusOK, "text/html", "Use your cameras with Frigate and Home Assistant"},
		{"/integrations.js", http.StatusOK, "javascript", "/api/v1/integrations/settings"},
		{"/nonexistent.file", http.StatusNotFound, "text/plain", "404 page not found"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != tt.expectedCode {
			t.Errorf("path %s: expected status %d, got %d", tt.path, tt.expectedCode, rec.Code)
		}

		contentType := rec.Header().Get("Content-Type")
		if !strings.Contains(contentType, tt.expectedType) {
			t.Errorf("path %s: expected Content-Type containing %q, got %q", tt.path, tt.expectedType, contentType)
		}

		body := rec.Body.String()
		if !strings.Contains(body, tt.contains) {
			t.Errorf("path %s: expected body containing %q, got body: %s", tt.path, tt.contains, body)
		}
	}
}

func TestUI_FS_Readable(t *testing.T) {
	fsys := FS()
	f, err := fsys.Open("index.html")
	if err != nil {
		t.Fatalf("failed to open index.html from embedded FS: %v", err)
	}
	defer f.Close()

	content, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("failed to read index.html: %v", err)
	}
	if !strings.Contains(string(content), "BombeCam Gateway") {
		t.Errorf("embedded index.html content missing expected string")
	}
}
