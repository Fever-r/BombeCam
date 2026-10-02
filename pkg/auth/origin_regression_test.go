package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMutationOriginMustMatchGateway(t *testing.T) {
	om := NewOperatorManager(OperatorConfig{})
	tests := []struct {
		origin string
		want   int
	}{
		{"", http.StatusOK},
		{"http://127.0.0.1:8654", http.StatusOK},
		{"http://127.0.0.1:9000", http.StatusForbidden},
		{"http://192.0.2.10:8654", http.StatusForbidden},
		{"http://localhost:8654", http.StatusForbidden},
		{"https://127.0.0.1:8654", http.StatusForbidden},
		{"http://127.0.0.1:8654/path", http.StatusForbidden},
		{"null", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.origin, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8654/api", nil)
			r.Header.Set("Origin", tt.origin)
			w := httptest.NewRecorder()
			om.ValidateSecurityBoundary(w, r, false)
			if w.Code != tt.want {
				t.Fatalf("origin %q: want %d, got %d", tt.origin, tt.want, w.Code)
			}
		})
	}
}
