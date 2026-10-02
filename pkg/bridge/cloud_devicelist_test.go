package bridge

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An Osaio account with no cameras replies {"code":1000,"data":null}; that
// is an empty list, not a crash.
func TestDeviceList_EmptyAndOddReplies(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  int
		bad   bool
	}{
		{"no cameras (data null)", `{"code":1000,"data":null}`, 0, false},
		{"no data field", `{"code":1000}`, 0, false},
		{"inner list null", `{"code":1000,"data":{"data":null}}`, 0, false},
		{"one camera", `{"code":1000,"data":{"data":[{"uuid":"e56","name":"Porch Cam","type":"WS03"}]}}`, 1, false},
		{"list without wrapper", `{"code":1000,"data":[{"uuid":"e56"},{"uuid":"f00"}]}`, 2, false},
		{"data is a string", `{"code":1000,"data":"oops"}`, 0, true},
		{"list items wrong shape", `{"code":1000,"data":{"data":[1,2]}}`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.reply))
			}))
			defer srv.Close()
			c := NewCloud("US", "+1", testServerKey)
			c.Web = srv.URL
			devs, err := c.DeviceList()
			if tc.bad {
				if !errors.Is(err, ErrUpstreamMalformedData) {
					t.Fatalf("want malformed-data error, got devs=%v err=%v", devs, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(devs) != tc.want {
				t.Fatalf("got %d devices, want %d (%v)", len(devs), tc.want, devs)
			}
		})
	}
}

func TestLogin_ReplyWithoutData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":1000,"data":null}`))
	}))
	defer srv.Close()
	c := NewCloud("US", "+1", testServerKey)
	c.Web = srv.URL
	if err := c.Login("someone@example.com", "pw"); !errors.Is(err, ErrUpstreamMalformedData) {
		t.Fatalf("want malformed-data error, got %v", err)
	}
}
