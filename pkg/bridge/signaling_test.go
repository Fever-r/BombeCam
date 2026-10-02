package bridge

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Fever-r/BombeCam/pkg/osaiovalue"
	"github.com/gorilla/websocket"
)

// wsTestServer is a vendor-signaling stand-in. Each accepted connection is
// handed to onConn; refuse makes dials fail with HTTP 503.
type wsTestServer struct {
	srv     *httptest.Server
	refuse  atomic.Bool
	dials   atomic.Int32
	accepts atomic.Int32
	appID   atomic.Value // the appid header of the last dial
	mu      sync.Mutex
	conns   []*websocket.Conn
	onConn  func(c *websocket.Conn)
}

func newWSTestServer(t *testing.T, onConn func(c *websocket.Conn)) *wsTestServer {
	t.Helper()
	ts := &wsTestServer{onConn: onConn}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.dials.Add(1)
		ts.appID.Store(r.Header.Get("appid"))
		if ts.refuse.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ts.mu.Lock()
		ts.conns = append(ts.conns, c)
		ts.mu.Unlock()
		ts.accepts.Add(1) // after registering, so dropAll sees it
		if ts.onConn != nil {
			go ts.onConn(c)
		}
	}))
	t.Cleanup(func() {
		ts.mu.Lock()
		for _, c := range ts.conns {
			_ = c.Close()
		}
		ts.mu.Unlock()
		ts.srv.Close()
	})
	return ts
}

func (ts *wsTestServer) url() string { return "ws" + strings.TrimPrefix(ts.srv.URL, "http") }

// dropAll closes every server-side connection. It first waits until the
// handler has registered the n-th connection, since the client's dial
// returns before the handler finishes.
func (ts *wsTestServer) dropAll(t *testing.T, n int32) {
	t.Helper()
	waitFor(t, 3*time.Second, "server to register the connection", func() bool { return ts.accepts.Load() >= n })
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, c := range ts.conns {
		_ = c.Close()
	}
	ts.conns = nil
}

// drain reads (and so answers pings) until the connection closes.
func drain(c *websocket.Conn) {
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
	}
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", d, what)
}

func TestSignalingReconnectsAfterDrop(t *testing.T) {
	ts := newWSTestServer(t, drain)
	s, err := connectHeader(ts.url(), http.Header{}, readDeadline)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ts.dropAll(t, 1)
	waitFor(t, 3*time.Second, "reconnect after drop", func() bool {
		return ts.accepts.Load() == 2 && s.Connected()
	})
	if err := s.Send("atr.get", []string{"LedOnOff"}); err != nil {
		t.Fatalf("send after reconnect: %v", err)
	}
}

// A control sent while the socket is down must not wait out the backoff.
func TestSignalingSendWhileDownRetriesNow(t *testing.T) {
	ts := newWSTestServer(t, drain)
	s, err := connectHeader(ts.url(), http.Header{}, readDeadline)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ts.refuse.Store(true)
	ts.dropAll(t, 1)
	// Attempts at about 0 s, 1 s and 3 s; the loop then waits 4 s.
	waitFor(t, 6*time.Second, "three refused reconnects", func() bool { return ts.dials.Load() >= 4 })
	time.Sleep(100 * time.Millisecond)
	ts.refuse.Store(false)

	err = s.Send("service.MovePTZ", map[string]any{"direction": 1})
	if !errors.Is(err, ErrReconnecting) {
		t.Fatalf("send while down = %v, want ErrReconnecting", err)
	}
	start := time.Now()
	waitFor(t, 2500*time.Millisecond, "reconnect woken by a control", s.Connected)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("reconnect took %v after the control; the backoff wasn't cut short", took)
	}
}

// A socket that goes silent without closing is replaced once the read
// deadline passes, instead of waiting for the OS's TCP timeout.
func TestSignalingSilentSocketIsReplaced(t *testing.T) {
	var first atomic.Bool
	ts := newWSTestServer(t, func(c *websocket.Conn) {
		if first.CompareAndSwap(false, true) {
			// One message, then silence: no reads (so no pongs), no close.
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"method":"event.Heartbeat"}`))
			return
		}
		drain(c)
	})
	s, err := connectHeader(ts.url(), http.Header{}, 400*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	waitFor(t, 3*time.Second, "replacement connection", func() bool {
		return ts.accepts.Load() == 2 && s.Connected()
	})
}

// Close stops the reconnect loop for good.
func TestSignalingCloseStopsReconnect(t *testing.T) {
	ts := newWSTestServer(t, drain)
	s, err := connectHeader(ts.url(), http.Header{}, readDeadline)
	if err != nil {
		t.Fatal(err)
	}
	ts.refuse.Store(true)
	ts.dropAll(t, 1)
	waitFor(t, 3*time.Second, "a refused reconnect", func() bool { return ts.dials.Load() >= 2 })
	_ = s.Close()
	ts.refuse.Store(false)
	time.Sleep(2500 * time.Millisecond)
	if n := ts.accepts.Load(); n != 1 {
		t.Fatalf("reconnected after Close (accepts=%d)", n)
	}
	if err := s.Send("atr.get", nil); err == nil {
		t.Fatal("send after Close succeeded")
	}
}

// A reconnect carries the app ID in use now: none is sent while there is no
// usable app ID, and one saved on the web page meanwhile is used at once.
func TestSignalingReconnectUsesCurrentAppID(t *testing.T) {
	store := osaiovalue.Load(AppIDKind, osaiovalue.Options{
		DefaultFile: filepath.Join(t.TempDir(), AppIDFileName),
		Getenv:      func(string) string { return "" },
	})
	if err := store.Save("first-app-id"); err != nil {
		t.Fatal(err)
	}
	UseAppIDs(store)
	t.Cleanup(func() { UseAppIDs(nil) })

	ts := newWSTestServer(t, drain)
	s, err := Connect(ts.url(), "t1", "u1", "phone")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := ts.appID.Load(); got != "first-app-id" {
		t.Fatalf("connect appid = %v", got)
	}

	if err := store.Reset(); err != nil { // no built-in app ID in tests: none left
		t.Fatal(err)
	}
	ts.dropAll(t, 1)
	time.Sleep(2500 * time.Millisecond) // attempts at about 0 s and 1 s
	if n := ts.dials.Load(); n != 1 {
		t.Fatalf("%d reconnect(s) sent without an app ID", n-1)
	}

	if err := store.Save("second-app-id"); err != nil {
		t.Fatal(err)
	}
	_ = s.Send("atr.get", []string{"LedOnOff"}) // wakes the reconnect
	waitFor(t, 3*time.Second, "reconnect with the new app ID", s.Connected)
	if got := ts.appID.Load(); got != "second-app-id" {
		t.Fatalf("reconnect appid = %v; want the app ID saved meanwhile", got)
	}
}
