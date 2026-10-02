package bridge

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Fever-r/BombeCam/pkg/osaiovalue"
	"github.com/Fever-r/BombeCam/pkg/serverkey"
)

// testAppID is a made-up app ID; tests never use the real one. Tests in this
// package get it through BOMBECAM_APP_ID, as a binary without a built-in one
// would.
const testAppID = "synthetic-app-id"

func init() {
	os.Setenv(EnvAppID, testAppID) // even if one is set in the environment
}

func TestRequestsCarryTheAppID(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("appid")
		_, _ = w.Write([]byte(`{"code":1000,"data":{"uid":"u1","api_token":"t1"}}`))
	}))
	defer srv.Close()
	t.Setenv(EnvAppID, " other-app-id ")
	c := NewCloud("1", "phone", testServerKey)
	c.Web = srv.URL
	if err := c.Login("someone@example.com", "pw"); err != nil || got != "other-app-id" {
		t.Fatalf("appid header = %q, %v; want the BOMBECAM_APP_ID value", got, err)
	}
}

// Without an app ID nothing is sent, and the error says why.
func TestCloudSendsNothingWithoutAppID(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"code":1000,"data":{}}`))
	}))
	defer srv.Close()
	t.Setenv(EnvAppID, "")
	if builtInAppID != "" {
		t.Skip("this test binary has an app ID built in")
	}
	c := NewCloud("1", "phone", serverkey.Static("synthetic-key"))
	c.Web, c.GlobalBase = srv.URL, srv.URL
	c.GetBaseURL("someone@example.com")
	if err := c.Login("someone@example.com", "pw"); !errors.Is(err, ErrNoAppID) {
		t.Fatalf("Login error = %v; want ErrNoAppID", err)
	}
	c.UID, c.APIToken = "u1", "t1"
	if _, err := c.DeviceList(); !errors.Is(err, ErrNoAppID) {
		t.Fatalf("DeviceList error = %v; want ErrNoAppID", err)
	}
	if _, err := Connect("ws://"+srv.Listener.Addr().String(), "t1", "u1", "phone"); !errors.Is(err, ErrNoAppID) {
		t.Fatalf("Connect error = %v; want ErrNoAppID", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d request(s) sent without an app ID", n)
	}
}

// The gateway installs its store; every request then takes the app ID from
// it, and a change on the web page applies to the next request.
func TestAppIDFromStoreAppliesAtOnce(t *testing.T) {
	file := filepath.Join(t.TempDir(), AppIDFileName)
	s := osaiovalue.Load(AppIDKind, osaiovalue.Options{DefaultFile: file, Getenv: func(string) string { return "" }})
	UseAppIDs(s)
	t.Cleanup(func() { UseAppIDs(nil) })
	if _, err := AppID(); !errors.Is(err, ErrNoAppID) {
		t.Fatalf("AppID with an empty store: %v; want ErrNoAppID", err)
	}
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("appid")
		_, _ = w.Write([]byte(`{"code":1000,"data":{"uid":"u1","api_token":"t1"}}`))
	}))
	defer srv.Close()
	c := NewCloud("1", "phone", testServerKey)
	c.Web = srv.URL
	if err := s.Save(" saved-app-id "); err != nil {
		t.Fatal(err)
	}
	if err := c.Login("someone@example.com", "pw"); err != nil || got != "saved-app-id" {
		t.Fatalf("appid header = %q, %v; want the saved app ID", got, err)
	}
	if v, err := AppIDKind.ReadFile(file); err != nil || v != "saved-app-id" {
		t.Fatalf("%s holds %q, %v", AppIDFileName, v, err)
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Reset left %s behind: %v", AppIDFileName, err)
	}
}

func TestAppIDFileFromEnvironment(t *testing.T) {
	file := filepath.Join(t.TempDir(), "my.app.id")
	if err := os.WriteFile(file, []byte("# note\nfile-app-id\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvAppID, "")
	t.Setenv(EnvAppIDFile, file)
	if v, err := AppID(); err != nil || v != "file-app-id" {
		t.Fatalf("AppID = %q, %v; want the value from %s", v, err, EnvAppIDFile)
	}
	// A file that is set but broken is reported, not skipped.
	if err := os.WriteFile(file, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AppID(); !errors.Is(err, ErrNoAppID) || !strings.Contains(err.Error(), "more than one app ID line") {
		t.Fatalf("AppID with a broken file: %v", err)
	}
}
