package ui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The page and its script are revalidated on every load, so an open tab
// picks up a new build instead of running an older app.js.
func TestAssetsAreNotCached(t *testing.T) {
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		rec := httptest.NewRecorder()
		Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control = %q, want no-cache", path, got)
		}
	}
}
