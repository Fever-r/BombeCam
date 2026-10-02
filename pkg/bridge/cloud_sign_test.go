package bridge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Fever-r/BombeCam/pkg/serverkey"
)

// testServerKey is a made-up key; tests never use a real one.
const testServerKey = serverkey.Static("synthetic-test-key-0001")

// expectedSign recomputes a signature independently of Cloud.sign.
func expectedSign(key, msg string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(msg))
	return base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(m.Sum(nil))))
}

func TestCloudSignsWithSuppliedKey(t *testing.T) {
	var gotSign, gotTS string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSign, gotTS = r.Header.Get("sign"), r.Header.Get("timestamp")
		_, _ = w.Write([]byte(`{"code":1000,"data":{"uid":"u1","api_token":"t1"}}`))
	}))
	defer srv.Close()

	for _, key := range []string{"synthetic-key-a", "synthetic-key-b"} {
		c := NewCloud("1", "phone", serverkey.Static(key))
		c.Web = srv.URL
		if err := c.Login("someone@example.com", "pw"); err != nil {
			t.Fatalf("Login with %s: %v", key, err)
		}
		appID, err := AppID()
		if err != nil {
			t.Fatal(err)
		}
		if want := expectedSign(key, appID+gotTS); gotSign != want {
			t.Fatalf("key %s: sign header %q, want %q", key, gotSign, want)
		}
	}
}

func TestCloudSendsNothingWithoutKey(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"code":1000,"data":{}}`))
	}))
	defer srv.Close()

	for name, keys := range map[string]serverkey.Provider{"nil provider": nil, "empty key": serverkey.Static("")} {
		c := NewCloud("1", "phone", keys)
		c.Web, c.GlobalBase = srv.URL, srv.URL
		c.UID, c.APIToken = "u1", "t1"
		c.GetBaseURL("someone@example.com")
		if err := c.Login("someone@example.com", "pw"); !errors.Is(err, ErrServerKey) {
			t.Errorf("%s: Login error = %v; want ErrServerKey", name, err)
		}
		if _, err := c.DeviceList(); !errors.Is(err, ErrServerKey) {
			t.Errorf("%s: DeviceList error = %v; want ErrServerKey", name, err)
		}
		if _, err := c.VideoCall("cam"); !errors.Is(err, ErrServerKey) {
			t.Errorf("%s: VideoCall error = %v; want ErrServerKey", name, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d request(s) reached the cloud without a key", n)
	}
}

// A clone made to sign in again keeps the same key source, so a key changed
// on the web page reaches renewed sessions too.
func TestCloneKeepsKeySource(t *testing.T) {
	c := NewCloud("1", "phone", testServerKey)
	if c.Clone().keys != c.keys {
		t.Fatal("Clone dropped the key provider")
	}
}
