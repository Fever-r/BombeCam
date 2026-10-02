package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Fever-r/BombeCam/internal/mediamtx"
	"github.com/Fever-r/BombeCam/pkg/bridge"
	"github.com/Fever-r/BombeCam/pkg/profile"
)

// settingsEnv: a profile with two cameras, both enrolled, two LAN addresses.
func settingsEnv(t *testing.T) (*StreamManager, *http.ServeMux, profile.ProfileManager) {
	t.Helper()
	withLAN(t, LANAddress{IP: "10.0.0.5", Interface: "Ethernet", Default: true}, LANAddress{IP: "192.168.8.2", Interface: "Wi-Fi"})
	resetIntegrations(t)
	oldOM := GatewayOperatorManager()
	SetGatewayOperatorManager(nil)
	t.Cleanup(func() { SetGatewayOperatorManager(oldOM) })
	_, sm, pm, _, mux, _ := setupFullTestEnvironment(t)
	sm.SetPublisher(bridge.PublisherNative)
	seedPrivacyProfile(t, pm, map[string]profile.CameraProfile{
		"a1b2c3d4e5f60718293a4b5c6d7e8f90": {UUID: "a1b2c3d4e5f60718293a4b5c6d7e8f90", Name: "Test Camera", Model: "WS03", EnrolledAt: time.Unix(100, 0)},
		"0123456789abcdef0123456789abcdef": {UUID: "0123456789abcdef0123456789abcdef", Name: "Test Camera", Model: "WS03", EnrolledAt: time.Unix(200, 0)},
	})
	if _, err := sm.Enroll([]string{"a1b2c3d4e5f60718293a4b5c6d7e8f90", "0123456789abcdef0123456789abcdef"}, []bridge.Device{
		{UUID: "a1b2c3d4e5f60718293a4b5c6d7e8f90", Name: "Test Camera", Type: "WS03"},
		{UUID: "0123456789abcdef0123456789abcdef", Name: "Test Camera", Type: "WS03"},
	}); err != nil {
		t.Fatal(err)
	}
	syncStreamNames(context.Background(), pm, sm)
	return sm, mux, pm
}

const camA, camB = "a1b2c3d4e5f60718293a4b5c6d7e8f90", "0123456789abcdef0123456789abcdef"

func TestStreamNames_AssignedOnceAndUnique(t *testing.T) {
	p := &profile.Profile{Cameras: map[string]profile.CameraProfile{
		"a": {Name: "Test Camera", EnrolledAt: time.Unix(1, 0)},
		"b": {Name: "Test Camera", EnrolledAt: time.Unix(2, 0)},
		"c": {Name: "2nd floor / hall", EnrolledAt: time.Unix(3, 0)},
		"d": {Name: "", EnrolledAt: time.Unix(4, 0)},
		"e": {Name: "All Others", EnrolledAt: time.Unix(5, 0)},
		"f": {Name: "Kept", StreamConfig: profile.CameraStreamConfig{RTSPPath: "garage"}},
		"g": {Name: strings.Repeat("very long name ", 10), EnrolledAt: time.Unix(6, 0)},
	}}
	if !assignStreamNames(p) {
		t.Fatal("names should have been assigned")
	}
	want := map[string]string{"a": "test_camera", "b": "test_camera_2", "c": "cam_2nd_floor_hall", "d": "camera", "e": "cam_all_others", "f": "garage"}
	for id, w := range want {
		if got := p.Cameras[id].StreamConfig.RTSPPath; got != w {
			t.Errorf("%s: %q, want %q", id, got, w)
		}
	}
	if g := p.Cameras["g"].StreamConfig.RTSPPath; len(g) > maxStreamNameLen || validateStreamName(g) != nil {
		t.Errorf("long name gave %q", g)
	}
	// renaming the camera later does not change its stream name
	c := p.Cameras["a"]
	c.Name = "Kitchen"
	p.Cameras["a"] = c
	if assignStreamNames(p) || p.Cameras["a"].StreamConfig.RTSPPath != "test_camera" {
		t.Error("an existing stream name must stay")
	}
}

func TestValidateStreamName(t *testing.T) {
	for _, ok := range []string{"garage", "front_door_2", "a"} {
		if err := validateStreamName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Garage", "2cam", "front door", "all_others", "birdseye", "a1b2c3d4e5f60718293a4b5c6d7e8f90", strings.Repeat("a", 41), "a/b", "../x"} {
		if err := validateStreamName(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestSyncStreamNames_PersistsAndAdvertises(t *testing.T) {
	sm, mux, pm := settingsEnv(t)
	prof := pm.GetProfile()
	if prof.Cameras[camA].StreamConfig.RTSPPath != "test_camera" || prof.Cameras[camB].StreamConfig.RTSPPath != "test_camera_2" {
		t.Fatalf("names not saved: %+v", prof.Cameras)
	}
	if got := sm.ConsumerRTSPURL(camA, nil); got != "rtsp://10.0.0.5:8554/test_camera" {
		t.Errorf("friendly URL %q", got)
	}
	// Frigate uses the stream name as camera and go2rtc name
	code, _ := doJSON(t, mux, http.MethodGet, "/api/v1/integrations/frigate", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/integrations/frigate?format=yaml", nil))
	if code != 200 || !strings.Contains(rr.Body.String(), "    test_camera_2:\n      - rtsp://10.0.0.5:8554/test_camera_2\n") {
		t.Errorf("frigate yaml:\n%s", rr.Body.String())
	}

	// rename on the Cameras tab
	code, resp := doJSON(t, mux, http.MethodPost, "/api/v1/cameras/"+camB+"/stream-name", map[string]any{"stream_name": "garage"})
	if code != 200 || resp["rtsp_url"] != "rtsp://10.0.0.5:8554/garage" || resp["previous"] != "test_camera_2" {
		t.Fatalf("rename: %d %v", code, resp)
	}
	if sm.StreamName(camB) != "garage" || pm.GetProfile().Cameras[camB].StreamConfig.RTSPPath != "garage" {
		t.Error("rename not applied")
	}
	for _, tc := range []struct {
		name string
		code int
	}{{"test_camera", http.StatusConflict}, {"Bad Name", http.StatusBadRequest}, {camA, http.StatusBadRequest}} {
		if code, resp := doJSON(t, mux, http.MethodPost, "/api/v1/cameras/"+camB+"/stream-name", map[string]any{"stream_name": tc.name}); code != tc.code {
			t.Errorf("%q: %d %v", tc.name, code, resp)
		}
	}
	if code, _ := doJSON(t, mux, http.MethodPost, "/api/v1/cameras/nope/stream-name", map[string]any{"stream_name": "x"}); code != http.StatusNotFound {
		t.Errorf("unknown camera: %d", code)
	}
}

// fakeMediaAPI imitates MediaMTX's /v3/config/paths API.
type fakeMediaAPI struct {
	paths map[string]mediamtx.ConfigPath
	calls []string
}

func (f *fakeMediaAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	switch {
	case r.URL.Path == "/v3/config/paths/list":
		items := []mediamtx.ConfigPath{}
		for _, p := range f.paths {
			items = append(items, p)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	case strings.HasPrefix(r.URL.Path, "/v3/config/paths/add/"), strings.HasPrefix(r.URL.Path, "/v3/config/paths/patch/"):
		var c struct {
			Source         string `json:"source"`
			SourceOnDemand bool   `json:"sourceOnDemand"`
		}
		_ = json.NewDecoder(r.Body).Decode(&c)
		f.paths[name] = mediamtx.ConfigPath{Name: name, Source: c.Source, SourceOnDemand: c.SourceOnDemand}
	case strings.HasPrefix(r.URL.Path, "/v3/config/paths/delete/"):
		delete(f.paths, name)
	default:
		http.NotFound(w, r)
	}
}

func TestSyncMediaAliases(t *testing.T) {
	api := &fakeMediaAPI{paths: map[string]mediamtx.ConfigPath{
		"all_others":  {Name: "all_others", Source: "publisher"},
		"users_cam":   {Name: "users_cam", Source: "rtsp://10.0.0.9/stream"},                             // the user's own: untouched
		"old_name":    {Name: "old_name", Source: "rtsp://127.0.0.1:8554/" + camA, SourceOnDemand: true}, // stale alias
		"test_camera": {Name: "test_camera", Source: "rtsp://127.0.0.1:8554/" + camB, SourceOnDemand: true},
	}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	err := syncMediaAliases(srv.URL, 8554, map[string]string{camA: "test_camera", camB: "garage"})
	if err != nil {
		t.Fatal(err)
	}
	if p := api.paths["test_camera"]; p.Source != "rtsp://127.0.0.1:8554/"+camA || !p.SourceOnDemand {
		t.Errorf("test_camera: %+v", p)
	}
	if p := api.paths["garage"]; p.Source != "rtsp://127.0.0.1:8554/"+camB {
		t.Errorf("garage: %+v", p)
	}
	if _, ok := api.paths["old_name"]; ok {
		t.Error("stale alias not removed")
	}
	if _, ok := api.paths["users_cam"]; !ok {
		t.Error("the user's own path must stay")
	}
	// a name clashing with the user's own path is reported, not overwritten
	if err := syncMediaAliases(srv.URL, 8554, map[string]string{camA: "users_cam"}); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Errorf("clash: %v", err)
	}
	if api.paths["users_cam"].Source != "rtsp://10.0.0.9/stream" {
		t.Error("user's path changed")
	}
}

func TestIntegrationSettings_AddressChoice(t *testing.T) {
	sm, mux, pm := settingsEnv(t)
	code, v := doJSON(t, mux, http.MethodGet, "/api/v1/integrations/settings", nil)
	if code != 200 || v["advertised_address"] != "10.0.0.5" || v["address_source"] != addrFromDefault {
		t.Fatalf("settings: %d %v", code, v)
	}
	if addrs, _ := v["addresses"].([]any); len(addrs) != 2 {
		t.Fatalf("addresses %v", v["addresses"])
	}
	// pick the GL.iNet network
	code, resp := doJSON(t, mux, http.MethodPost, "/api/v1/integrations/settings", map[string]any{"nvr_address": "192.168.8.2"})
	if code != 200 {
		t.Fatalf("%d %v", code, resp)
	}
	if got := sm.ConsumerRTSPURL(camA, nil); got != "rtsp://192.168.8.2:8554/test_camera" {
		t.Errorf("after choosing: %q", got)
	}
	if pm.GetProfile().Integrations.NVRAddress != "192.168.8.2" {
		t.Error("choice not saved")
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/integrations/frigate?format=yaml", nil))
	if !strings.Contains(rr.Body.String(), "rtsp://192.168.8.2:8554/test_camera") || strings.Contains(rr.Body.String(), "10.0.0.5") {
		t.Errorf("snippet does not use the chosen address:\n%s", rr.Body.String())
	}
	// an address this PC doesn't have is refused; "" goes back to automatic
	if code, _ := doJSON(t, mux, http.MethodPost, "/api/v1/integrations/settings", map[string]any{"nvr_address": "8.8.8.8"}); code != http.StatusBadRequest {
		t.Errorf("foreign address: %d", code)
	}
	if code, _ := doJSON(t, mux, http.MethodPost, "/api/v1/integrations/settings", map[string]any{"nvr_address": ""}); code != 200 || sm.NVRAddress() != "" {
		t.Errorf("back to automatic: %d %q", code, sm.NVRAddress())
	}
}

func TestIntegrationSettings_StreamPasswordAndValidation(t *testing.T) {
	sm, mux, pm := settingsEnv(t)
	setPortsLocked(true, "test: MediaMTX not managed") // no MediaMTX to restart here
	for _, request := range []map[string]any{{"stream_auth": true}, {"regenerate_password": true, "stream_auth": true}} {
		code, response := doJSON(t, mux, http.MethodPost, "/api/v1/integrations/settings", request)
		if code != http.StatusBadRequest || response["ok"] == true {
			t.Fatalf("unmanaged authentication change accepted: %d %v", code, response)
		}
		saved := pm.GetProfile().Integrations
		if saved.StreamAuth || !saved.StreamPassword.IsEmpty() {
			t.Fatal("rejected authentication preference was saved")
		}
		if currentIntegrationSettings().StreamAuth || strings.Contains(sm.ConsumerRTSPURL(camA, nil), "@") {
			t.Fatal("rejected password was advertised")
		}
	}

	for _, bad := range []map[string]any{
		{"ports": map[string]any{"rtsp": 80}},
		{"ports": map[string]any{"rtsp": 8888, "hls": 8888}},
		{"ports": map[string]any{"snapshot": 8654}},
		{"mqtt": map[string]any{"enabled": true, "host": ""}},
		{"mqtt": map[string]any{"enabled": true, "host": "ha.local", "discovery_prefix": "home assistant"}},
	} {
		if code, resp := doJSON(t, mux, http.MethodPost, "/api/v1/integrations/settings", bad); code != http.StatusBadRequest {
			t.Errorf("%v: %d %v", bad, code, resp)
		}
	}
	// ports are locked (MediaMTX not managed by BombeCam): changes refused with the reason
	if code, resp := doJSON(t, mux, http.MethodPost, "/api/v1/integrations/settings", map[string]any{"ports": map[string]any{"rtsp": 9554}}); code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(resp["message"]), "MediaMTX not managed") {
		t.Errorf("locked ports: %d %v", code, resp)
	}
	// MQTT settings saved; the password is kept when not re-sent, and never shown
	doJSON(t, mux, http.MethodPost, "/api/v1/integrations/settings", map[string]any{"mqtt": map[string]any{"enabled": true, "host": "broker.invalid", "port": 1883, "username": "ha", "password": "s3cret"}})
	doJSON(t, mux, http.MethodPost, "/api/v1/integrations/settings", map[string]any{"mqtt": map[string]any{"username": "ha2"}})
	m := pm.GetProfile().Integrations.MQTT
	if !m.Enabled || m.Host != "broker.invalid" || m.Username != "ha2" || m.Password.Expose() != "s3cret" {
		t.Errorf("mqtt: %+v", m)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/integrations/settings", nil))
	if strings.Contains(rr.Body.String(), "s3cret") || !strings.Contains(rr.Body.String(), `"has_password":true`) {
		t.Errorf("settings view: %s", rr.Body.String())
	}
	storeIntegrationSettings(profile.IntegrationSettings{}) // stop the MQTT client from connecting
	mqttSync(sm)
}

func TestIntegrationSettings_NeedsAProfile(t *testing.T) {
	withLAN(t, LANAddress{IP: "10.0.0.5", Default: true})
	resetIntegrations(t)
	oldOM := GatewayOperatorManager()
	SetGatewayOperatorManager(nil)
	t.Cleanup(func() { SetGatewayOperatorManager(oldOM) })
	_, _, _, _, mux, _ := setupFullTestEnvironment(t)
	if code, _ := doJSON(t, mux, http.MethodPost, "/api/v1/integrations/settings", map[string]any{"nvr_address": ""}); code != http.StatusConflict {
		t.Errorf("without a profile: %d", code)
	}
	if code, v := doJSON(t, mux, http.MethodGet, "/api/v1/integrations/settings", nil); code != 200 || v["has_profile"] != false {
		t.Errorf("GET without a profile: %d %v", code, v)
	}
	if code, _ := doJSON(t, mux, http.MethodPut, "/api/v1/integrations/settings", nil); code != http.StatusMethodNotAllowed {
		t.Errorf("PUT: %d", code)
	}
}

func TestSnapshot_LocalRouteCacheAndErrors(t *testing.T) {
	sm, mux, _ := settingsEnv(t)
	var calls atomic.Int32
	old := snapshots.grab
	snapshots.grab = func(ctx context.Context, ffmpeg, src string) ([]byte, error) {
		calls.Add(1)
		if !strings.HasSuffix(src, "/"+camA) || !strings.HasPrefix(src, "rtsp://127.0.0.1:") {
			return nil, errors.New("wrong source " + src)
		}
		time.Sleep(50 * time.Millisecond)
		return []byte("\xff\xd8fake-jpeg"), nil
	}
	snapshots.mu.Lock()
	snapshots.cache = nil
	snapshots.mu.Unlock()
	t.Cleanup(func() { snapshots.grab = old })

	// by camera ID and by stream name; concurrent requests share one grab
	done := make(chan int, 4)
	for _, id := range []string{camA, "test_camera", camA, "test_camera"} {
		go func(id string) {
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/cameras/"+id+"/snapshot.jpg", nil))
			if rr.Header().Get("Content-Type") != "image/jpeg" || !strings.HasPrefix(rr.Body.String(), "\xff\xd8") {
				t.Errorf("%s: %d %q", id, rr.Code, rr.Body.String())
			}
			done <- rr.Code
		}(id)
	}
	for i := 0; i < 4; i++ {
		if c := <-done; c != 200 {
			t.Errorf("status %d", c)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("FFmpeg ran %d times, want 1 (cache + sharing)", n)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/cameras/nope/snapshot.jpg", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown camera: %d", rr.Code)
	}
	// without FFmpeg: a clear 503
	sm.mu.Lock()
	sm.ffmpegPath = ""
	sm.mu.Unlock()
	snapshots.mu.Lock()
	snapshots.cache = nil
	snapshots.mu.Unlock()
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/cameras/"+camB+"/snapshot.jpg", nil))
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "FFmpeg") {
		t.Errorf("no FFmpeg: %d %s", rr.Code, rr.Body.String())
	}
}

// An older picture is returned at once while a new one is taken; a failed
// refresh keeps serving the last good picture for a while.
func TestSnapshot_StaleWhileRefreshing(t *testing.T) {
	_, mux, _ := settingsEnv(t)
	var calls atomic.Int32
	fail := atomic.Bool{}
	old := snapshots.grab
	snapshots.grab = func(ctx context.Context, ffmpeg, src string) ([]byte, error) {
		n := calls.Add(1)
		time.Sleep(300 * time.Millisecond)
		if fail.Load() {
			return nil, errors.New("camera offline")
		}
		return []byte(fmt.Sprintf("\xff\xd8picture-%d", n)), nil
	}
	snapshots.mu.Lock()
	snapshots.cache = nil
	snapshots.mu.Unlock()
	t.Cleanup(func() {
		snapshots.mu.Lock()
		snapshots.grab, snapshots.cache = old, nil
		snapshots.mu.Unlock()
	})

	get := func() (int, string, time.Duration) {
		start := time.Now()
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/cameras/"+camA+"/snapshot.jpg", nil))
		return rr.Code, rr.Body.String(), time.Since(start)
	}
	age := func(d time.Duration) {
		snapshots.mu.Lock()
		e := snapshots.cache[camA]
		e.at = e.at.Add(-d)
		e.goodAt = e.goodAt.Add(-d)
		snapshots.mu.Unlock()
	}
	waitIdle := func() {
		for i := 0; i < 100; i++ {
			snapshots.mu.Lock()
			_, busy := snapshots.inflight[camA]
			snapshots.mu.Unlock()
			if !busy {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("refresh did not finish")
	}

	if code, body, _ := get(); code != 200 || !strings.HasSuffix(body, "picture-1") {
		t.Fatalf("first: %d %q", code, body)
	}
	age(10 * time.Second)
	code, body, took := get()
	if code != 200 || !strings.HasSuffix(body, "picture-1") || took > 200*time.Millisecond {
		t.Errorf("stale picture should come at once: %d %q after %v", code, body, took)
	}
	waitIdle()
	if code, body, _ := get(); code != 200 || !strings.HasSuffix(body, "picture-2") {
		t.Errorf("after refresh: %d %q", code, body)
	}

	// the camera stops: the last good picture is still served for a while
	fail.Store(true)
	age(10 * time.Second)
	get()
	waitIdle()
	if code, body, _ := get(); code != 200 || !strings.HasSuffix(body, "picture-2") {
		t.Errorf("failed refresh should keep the last picture: %d %q", code, body)
	}
	// ...but not forever
	age(2 * time.Minute)
	get()
	waitIdle()
	if code, _, _ := get(); code == 200 {
		t.Error("a picture older than a minute must not be served when the camera fails")
	}
}

// The network listener serves snapshots only: no control or settings route,
// the stream password when on, and IP-address hosts only.
func TestSnapshot_NetworkListenerIsReadOnly(t *testing.T) {
	sm, _, _ := settingsEnv(t)
	old := snapshots.grab
	snapshots.grab = func(ctx context.Context, ffmpeg, src string) ([]byte, error) { return []byte("\xff\xd8jpeg"), nil }
	snapshots.mu.Lock()
	snapshots.cache = nil
	snapshots.mu.Unlock()
	t.Cleanup(func() { snapshots.grab = old })

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	s := profile.IntegrationSettings{Snapshots: true, SnapshotPort: port}
	applyIntegrationRuntime(sm, s)
	t.Cleanup(func() { snapshots.Configure(sm, profile.IntegrationSettings{}) })
	if st := snapshots.Status(); !strings.HasPrefix(st, "listening") {
		t.Fatalf("status %q", st)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	get := func(path string, hdr map[string]string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		for k, v := range hdr {
			if k == "Host" {
				req.Host = v
			} else {
				req.Header.Set(k, v)
			}
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("/api/v1/cameras/test_camera/snapshot.jpg", nil); code != 200 || !strings.HasPrefix(body, "\xff\xd8") {
		t.Errorf("snapshot: %d %q", code, body)
	}
	for _, p := range []string{"/", "/api/v1/cameras/" + camA + "/control", "/api/v1/cameras/" + camA + "/ptz", "/api/v1/integrations/settings", "/api/v1/privacy", "/index.html", "/api/v1/cameras/" + camA + "/stream"} {
		if code, _ := get(p, nil); code != http.StatusNotFound {
			t.Errorf("%s answered %d on the network listener", p, code)
		}
	}
	if code, _ := get("/api/v1/cameras/test_camera/snapshot.jpg", map[string]string{"Host": "evil.example.com"}); code != http.StatusForbidden {
		t.Errorf("DNS-rebinding host: %d", code)
	}
	// stream password on: basic auth needed
	sm.SetStreamCredentials("bombecam", "pw123")
	if code, _ := get("/api/v1/cameras/test_camera/snapshot.jpg", nil); code != http.StatusUnauthorized {
		t.Errorf("without password: %d", code)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/cameras/test_camera/snapshot.jpg", nil)
	req.SetBasicAuth("bombecam", "pw123")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Errorf("with password: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
	// and the advertised URL carries it
	if u := snapshotURLFor(sm, nil, camA); u != fmt.Sprintf("http://bombecam:pw123@10.0.0.5:%d/api/v1/cameras/test_camera/snapshot.jpg", port) {
		t.Errorf("snapshot URL %q", u)
	}
	// off: the listener closes
	applyIntegrationRuntime(sm, profile.IntegrationSettings{})
	if st := snapshots.Status(); st != "off" {
		t.Errorf("status after off: %q", st)
	}
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond); err == nil {
		t.Error("listener still open")
	}
}

func TestHLSRelay(t *testing.T) {
	sm, mux, _ := settingsEnv(t)
	var gotPath, gotCookie string
	hls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotCookie = r.URL.Path, r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n"))
	}))
	defer hls.Close()
	sm.SetHLSBase(hls.URL)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cameras/"+camA+"/hls/stream.m3u8?_HLS_msn=3", nil)
	req.Header.Set("Cookie", "bombecam_session=secret")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 || rr.Body.String() != "#EXTM3U\n" || gotPath != "/"+camA+"/stream.m3u8" || gotCookie != "" {
		t.Fatalf("relay: %d %q path %q cookie %q", rr.Code, rr.Body.String(), gotPath, gotCookie)
	}
	for _, bad := range []string{"/api/v1/cameras/nope/hls/index.m3u8", "/api/v1/cameras/" + camA + "/hls/../../x"} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, bad, nil))
		if rr.Code == 200 {
			t.Errorf("%s: %d", bad, rr.Code)
		}
	}
}

func TestMQTTDiscoveryPayload(t *testing.T) {
	p := haDiscoveryPayload(camA, "Test Camera", "WS03")
	b, _ := json.Marshal(p)
	var d struct {
		Dev  map[string]any            `json:"dev"`
		O    map[string]any            `json:"o"`
		Cmps map[string]map[string]any `json:"cmps"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if d.Dev["name"] != "Test Camera" || fmt.Sprint(d.Dev["ids"]) != "[bombecam_"+camA+"]" || d.O["name"] != "BombeCam" {
		t.Errorf("device: %v %v", d.Dev, d.O)
	}
	want := map[string]string{
		"night_vision": "select", "status_light": "switch", "motion_detection": "switch", "sound_detection": "switch",
		"stream": "binary_sensor", "controls": "binary_sensor", "connection": "sensor",
		"pan_left": "button", "pan_right": "button", "tilt_up": "button", "tilt_down": "button",
	}
	for k, platform := range want {
		c, ok := d.Cmps[k]
		if !ok || c["p"] != platform || !strings.HasPrefix(fmt.Sprint(c["unique_id"]), "bombecam_"+camA) {
			t.Errorf("%s: %v", k, c)
		}
	}
	if _, ok := d.Cmps["spotlight"]; ok {
		t.Error("the WS03 has no spotlight")
	}
	if fmt.Sprint(d.Cmps["night_vision"]["options"]) != "[Auto Off]" || d.Cmps["pan_left"]["payload_press"] != "left" ||
		d.Cmps["pan_left"]["command_topic"] != "bombecam/"+camA+"/ptz/set" || d.Cmps["status_light"]["state_topic"] != "bombecam/"+camA+"/status_light" {
		t.Errorf("topics: %v %v", d.Cmps["night_vision"], d.Cmps["pan_left"])
	}
	// the stream sensor stays available while the camera is offline; controls don't
	if fmt.Sprint(d.Cmps["stream"]["availability"]) != "[map[topic:bombecam/status]]" || !strings.Contains(fmt.Sprint(d.Cmps["status_light"]["availability"]), "/available") {
		t.Errorf("availability: %v / %v", d.Cmps["stream"]["availability"], d.Cmps["status_light"]["availability"])
	}
	spot := haDiscoveryPayload("x", "Yard", "GW1")["cmps"].(map[string]any)
	if _, ok := spot["spotlight"]; !ok {
		t.Error("GW1 has a spotlight")
	}
	if _, ok := spot["pan_left"]; ok {
		t.Error("GW1 has no pan/tilt")
	}
	if discoveryTopic("homeassistant", camA) != "homeassistant/device/bombecam_"+camA+"/config" {
		t.Error("discovery topic")
	}
}

func TestParseNetConnectionProfiles(t *testing.T) {
	one, ok := parseNetConnectionProfiles([]byte(`{"Name":"Network 3","InterfaceAlias":"Wi-Fi","NetworkCategory":0}`))
	if !ok || len(one) != 1 || one[0].Category != "Public" || one[0].Interface != "Wi-Fi" {
		t.Errorf("one: %v %v", one, ok)
	}
	two, ok := parseNetConnectionProfiles([]byte(`[{"Name":"Network","InterfaceAlias":"Ethernet","NetworkCategory":1},{"Name":"Network 2","InterfaceAlias":"Wi-Fi","NetworkCategory":"Public"}]`))
	if !ok || len(two) != 2 || two[0].Category != "Private" || two[1].Category != "Public" {
		t.Errorf("two: %v", two)
	}
	if _, ok := parseNetConnectionProfiles([]byte(`not json`)); ok {
		t.Error("junk accepted")
	}

	// warn only when the advertised address is on the Public network
	withLAN(t, LANAddress{IP: "10.0.0.5", Interface: "Ethernet", Default: true}, LANAddress{IP: "192.168.8.2", Interface: "Wi-Fi"})
	oldF := netProfileFunc
	netProfileFunc = func() ([]networkConnection, bool) { return two, true }
	sm := NewStreamManager(nil, "rtsp://127.0.0.1:8554", "http://127.0.0.1:8888", nil, false, "")
	setCurrentStreamManager(sm)
	t.Cleanup(func() { netProfileFunc = oldF; setCurrentStreamManager(nil); netProfAt = time.Time{} })
	netProfAt = time.Time{}
	if v := currentNetworkProfile(); !v.Checked || v.Public {
		t.Errorf("Ethernet is Private: %+v", v)
	}
	sm.SetNVRAddress("192.168.8.2")
	if v := currentNetworkProfile(); !v.Public {
		t.Errorf("Wi-Fi is Public: %+v", v)
	}
}

// TestDocsMentionNewFlags keeps README's option list in step with main.go.
func TestReadmeListsIntegrationFlags(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Skip("README not found")
	}
	for _, f := range []string{"-rtsp-port", "-retry-max-wait"} {
		if !strings.Contains(string(b), f) {
			t.Errorf("README does not mention %s", f)
		}
	}
}
