package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Fever-r/BombeCam/pkg/auth"
	"github.com/Fever-r/BombeCam/pkg/profile"
)

type adminClient struct {
	t       *testing.T
	handler http.Handler
	remote  bool
	cookie  *http.Cookie
	csrf    string
}

func (c *adminClient) do(method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	c.t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Host = "127.0.0.1:8654"
	req.RemoteAddr = "127.0.0.1:50000"
	if c.remote {
		req.RemoteAddr = "192.0.2.77:50000"
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
		if method != http.MethodGet {
			req.Header.Set(auth.CSRFHeaderName, c.csrf)
		}
	}
	rr := httptest.NewRecorder()
	c.handler.ServeHTTP(rr, req)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	for _, ck := range rr.Result().Cookies() {
		if ck.Name == auth.SessionCookieName {
			if ck.MaxAge < 0 {
				c.cookie = nil
			} else {
				c.cookie = ck
			}
		}
	}
	if tok, ok := out["csrf_token"].(string); ok {
		c.csrf = tok
	}
	return rr, out
}

func adminEnv(t *testing.T) (http.Handler, profile.ProfileManager) {
	t.Helper()
	_, _, pm, _, mux, _ := setupFullTestEnvironment(t)
	prev := GatewayOperatorManager()
	SetGatewayOperatorManager(auth.NewOperatorManager(auth.OperatorConfig{}))
	operatorSignIns.succeeded("192.0.2.77")
	operatorSignIns.succeeded("127.0.0.1")
	t.Cleanup(func() { SetGatewayOperatorManager(prev) })
	return SecurityBoundaryHandler(mux), pm
}

// First start: the administrator is created on this PC, once, and never
// from another device.
func TestAdminCreatedOnThisPCOnly(t *testing.T) {
	h, pm := adminEnv(t)
	lan := &adminClient{t: t, handler: h, remote: true}
	local := &adminClient{t: t, handler: h}

	_, st := local.do("GET", "/api/v1/admin/status", "")
	if st["admin_exists"] != false || st["can_create"] != true {
		t.Fatalf("first-run status: %v", st)
	}
	if _, st := lan.do("GET", "/api/v1/admin/status", ""); st["can_create"] != false {
		t.Fatalf("another device must not be offered to create the administrator: %v", st)
	}
	if rr, _ := lan.do("POST", "/api/v1/admin/setup", `{"name":"admin","password":"long enough pw"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("setup from the LAN: %d", rr.Code)
	}
	if rr, _ := local.do("POST", "/api/v1/admin/setup", `{"name":"admin","password":"short"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("short password: %d", rr.Code)
	}
	if rr, out := local.do("POST", "/api/v1/admin/setup", `{"name":"admin","password":"long enough pw","require_local_sign_in":true}`); rr.Code != 200 || local.cookie == nil || local.csrf == "" {
		t.Fatalf("setup: %d %v", rr.Code, out)
	}
	if rr, _ := local.do("POST", "/api/v1/admin/setup", `{"name":"other","password":"long enough pw"}`); rr.Code != http.StatusConflict {
		t.Fatalf("a second administrator: %d", rr.Code)
	}
	admin, ok := pm.GetProfile().Admin()
	if !ok || admin.Name != "admin" || !pm.GetProfile().Security.RequireLocalSignIn || !auth.VerifyPassword(admin.PasswordHash.Expose(), "long enough pw") {
		t.Fatalf("stored admin: %+v", admin)
	}
}

// Other devices always sign in; this PC only when the administrator chose so.
func TestAdminSignInRules(t *testing.T) {
	h, pm := adminEnv(t)
	seedAdmin(t, pm, "admin", "long enough pw", false)

	local := &adminClient{t: t, handler: h}
	if rr, _ := local.do("GET", "/api/v1/accounts", ""); rr.Code != 200 {
		t.Fatalf("this PC without local sign-in: %d", rr.Code)
	}

	lan := &adminClient{t: t, handler: h, remote: true}
	if rr, _ := lan.do("GET", "/api/v1/accounts", ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("another device without sign-in: %d", rr.Code)
	}
	if rr, _ := lan.do("GET", "/", ""); rr.Code != 200 {
		t.Fatalf("the page itself must load so it can sign in: %d", rr.Code)
	}
	if rr, out := lan.do("POST", "/api/v1/admin/signin", `{"name":"admin","password":"wrong password"}`); rr.Code != 401 || out["error"] != "wrong_password" {
		t.Fatalf("wrong password: %d %v", rr.Code, out)
	}
	if rr, _ := lan.do("POST", "/api/v1/admin/signin", `{"name":"Admin","password":"long enough pw"}`); rr.Code != 200 || lan.cookie == nil {
		t.Fatalf("sign-in: %d", rr.Code)
	}
	if rr, _ := lan.do("GET", "/api/v1/accounts", ""); rr.Code != 200 {
		t.Fatalf("signed in from another device: %d", rr.Code)
	}
	if _, st := lan.do("GET", "/api/v1/admin/status", ""); st["signed_in"] != true || st["name"] != "admin" {
		t.Fatalf("status when signed in: %v", st)
	}
	lan.do("POST", "/api/v1/admin/signout", "")
	if rr, _ := lan.do("GET", "/api/v1/accounts", ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("after sign-out: %d", rr.Code)
	}

	// Ask for sign-in on this PC too (needs the password).
	if rr, _ := local.do("POST", "/api/v1/admin/local-sign-in", `{"password":"wrong password","require_local_sign_in":true}`); rr.Code != 401 {
		t.Fatalf("changing the setting needs the password: %d", rr.Code)
	}
	if rr, _ := local.do("POST", "/api/v1/admin/local-sign-in", `{"password":"long enough pw","require_local_sign_in":true}`); rr.Code != 200 {
		t.Fatalf("turn on local sign-in: %d", rr.Code)
	}
	if rr, _ := local.do("GET", "/api/v1/accounts", ""); rr.Code != 200 {
		t.Fatalf("the browser that turned it on stays signed in: %d", rr.Code)
	}
	other := &adminClient{t: t, handler: h}
	if rr, _ := other.do("GET", "/api/v1/accounts", ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("another browser on this PC must sign in now: %d", rr.Code)
	}
	if _, st := other.do("GET", "/api/v1/admin/status", ""); st["sign_in_required"] != true || st["name"] != nil {
		t.Fatalf("status before sign-in must not reveal details: %v", st)
	}
}

// Two-step: an authenticator code (each once), or a recovery code (each once).
func TestAdminTwoStep(t *testing.T) {
	h, pm := adminEnv(t)
	seedAdmin(t, pm, "admin", "long enough pw", false)
	local := &adminClient{t: t, handler: h}

	rr, start := local.do("POST", "/api/v1/admin/two-step/start", `{"password":"long enough pw"}`)
	secret, _ := start["secret"].(string)
	if rr.Code != 200 || secret == "" || !strings.HasPrefix(start["qr"].(string), "data:image/png;base64,") || !strings.HasPrefix(start["uri"].(string), "otpauth://totp/BombeCam:admin?") {
		t.Fatalf("start: %d %v", rr.Code, start)
	}
	if a, _ := pm.GetProfile().Admin(); a.TwoStep() {
		t.Fatal("two-step must not be on before a code proves the app has the secret")
	}
	if rr, _ := local.do("POST", "/api/v1/admin/two-step/confirm", `{"code":"000000"}`); rr.Code != 401 {
		t.Fatalf("wrong confirm code: %d", rr.Code)
	}
	prev := time.Now().Add(-30 * time.Second)
	rr, conf := local.do("POST", "/api/v1/admin/two-step/confirm", `{"code":"`+auth.TOTPCode(secret, prev)+`"}`)
	codes, _ := conf["recovery_codes"].([]any)
	if rr.Code != 200 || len(codes) != auth.RecoveryCodeCount {
		t.Fatalf("confirm: %d %v", rr.Code, conf)
	}

	lan := &adminClient{t: t, handler: h, remote: true}
	if _, out := lan.do("POST", "/api/v1/admin/signin", `{"name":"admin","password":"long enough pw"}`); out["error"] != "code_required" {
		t.Fatalf("password alone: %v", out)
	}
	if _, out := lan.do("POST", "/api/v1/admin/signin", `{"name":"admin","password":"long enough pw","code":"`+auth.TOTPCode(secret, prev)+`"}`); out["error"] != "wrong_code" {
		t.Fatalf("the code used to turn it on must not work again: %v", out)
	}
	now := auth.TOTPCode(secret, time.Now())
	if rr, _ := lan.do("POST", "/api/v1/admin/signin", `{"name":"admin","password":"long enough pw","code":"`+now+`"}`); rr.Code != 200 {
		t.Fatalf("current code: %d", rr.Code)
	}
	lan2 := &adminClient{t: t, handler: h, remote: true}
	if _, out := lan2.do("POST", "/api/v1/admin/signin", `{"name":"admin","password":"long enough pw","code":"`+now+`"}`); out["error"] != "wrong_code" {
		t.Fatalf("a code must work only once: %v", out)
	}
	operatorSignIns.succeeded("192.0.2.77")
	rec := codes[0].(string)
	if rr, out := lan2.do("POST", "/api/v1/admin/signin", `{"name":"admin","password":"long enough pw","code":"`+rec+`"}`); rr.Code != 200 || out["used_recovery_code"] != true || out["recovery_codes_left"] != float64(auth.RecoveryCodeCount-1) {
		t.Fatalf("recovery code: %d %v", rr.Code, out)
	}
	lan3 := &adminClient{t: t, handler: h, remote: true}
	if _, out := lan3.do("POST", "/api/v1/admin/signin", `{"name":"admin","password":"long enough pw","code":"`+rec+`"}`); out["error"] != "wrong_code" {
		t.Fatalf("a recovery code must work only once: %v", out)
	}
	operatorSignIns.succeeded("192.0.2.77")

	// Turning it off needs the password and a code.
	if rr, _ := local.do("POST", "/api/v1/admin/two-step/disable", `{"password":"long enough pw","code":"000000"}`); rr.Code != 401 {
		t.Fatalf("disable with a wrong code: %d", rr.Code)
	}
	if rr, _ := local.do("POST", "/api/v1/admin/two-step/disable", `{"password":"long enough pw","code":"`+codes[1].(string)+`"}`); rr.Code != 200 {
		t.Fatalf("disable: %d", rr.Code)
	}
	if a, _ := pm.GetProfile().Admin(); a.TwoStep() || len(a.RecoveryCodes) != 0 {
		t.Fatal("two-step should be off")
	}
}

// A new password signs every browser out except the one that changed it.
func TestAdminPasswordChangeSignsOthersOut(t *testing.T) {
	h, pm := adminEnv(t)
	seedAdmin(t, pm, "admin", "long enough pw", false)
	a := &adminClient{t: t, handler: h, remote: true}
	b := &adminClient{t: t, handler: h, remote: true}
	a.do("POST", "/api/v1/admin/signin", `{"password":"long enough pw"}`)
	b.do("POST", "/api/v1/admin/signin", `{"password":"long enough pw"}`)
	if rr, _ := a.do("POST", "/api/v1/admin/password", `{"password":"long enough pw","new_password":"a new long password"}`); rr.Code != 200 {
		t.Fatalf("change: %d", rr.Code)
	}
	if rr, _ := a.do("GET", "/api/v1/accounts", ""); rr.Code != 200 {
		t.Fatalf("the browser that changed it stays signed in: %d", rr.Code)
	}
	if rr, _ := b.do("GET", "/api/v1/accounts", ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("other browsers are signed out: %d", rr.Code)
	}
	if adm, _ := pm.GetProfile().Admin(); !auth.VerifyPassword(adm.PasswordHash.Expose(), "a new long password") {
		t.Fatal("new password not saved")
	}
}

// Delete all stored information clears Osaio logins, cameras and settings
// but keeps the administrator: the password changes only under Sign-in
// settings, and whoever deleted stays signed in, on any device.
func TestDeleteAllKeepsAdmin(t *testing.T) {
	h, pm := adminEnv(t)
	seedAdmin(t, pm, "admin", "long enough pw", true)
	_, _ = pm.Update(context.Background(), func(p *profile.Profile) error {
		p.UpdateAdmin(func(u *profile.UserProfile) { u.PasswordHint = "a hint" })
		p.UpsertAccount(profile.CloudCredentials{AccountEmail: "a@example.test", Password: "osaio"})
		p.Cameras = map[string]profile.CameraProfile{"cam": {UUID: "cam", AccountEmail: "a@example.test"}}
		p.Integrations.Snapshots = true
		return nil
	})
	lan := &adminClient{t: t, handler: h, remote: true}
	lan.do("POST", "/api/v1/admin/signin", `{"password":"long enough pw"}`)
	rr, out := lan.do("POST", "/api/v1/profile/forget", "")
	if rr.Code != 200 || out["admin_kept"] != true {
		t.Fatalf("delete all: %d %v", rr.Code, out)
	}
	prof := pm.GetProfile()
	if prof == nil || len(prof.Accounts) != 0 || len(prof.Cameras) != 0 || prof.Integrations.Snapshots {
		t.Fatalf("stored information left: %s", prof)
	}
	a, ok := prof.Admin()
	if !ok || !auth.VerifyPassword(a.PasswordHash.Expose(), "long enough pw") || a.PasswordHint != "a hint" || !prof.Security.RequireLocalSignIn {
		t.Fatalf("administrator not kept: %+v %+v", a, prof.Security)
	}
	if rr, _ := lan.do("GET", "/api/v1/accounts", ""); rr.Code != 200 {
		t.Fatalf("still signed in after delete all: %d", rr.Code)
	}
	if _, st := lan.do("GET", "/api/v1/onboarding/status", ""); st["logins_count"] != float64(0) {
		t.Fatalf("status after delete all: %v", st)
	}
}

// Delete all needs the sign-in like everything else (a forgotten password
// is handled by starting over on the PC, see TestStartOverOnThisPCOnly).
func TestDeleteAllNeedsSignIn(t *testing.T) {
	h, pm := adminEnv(t)
	seedAdmin(t, pm, "admin", "long enough pw", true)
	for _, c := range []*adminClient{{t: t, handler: h}, {t: t, handler: h, remote: true}} {
		if rr, _ := c.do("POST", "/api/v1/profile/forget", ""); rr.Code != http.StatusUnauthorized {
			t.Fatalf("signed out (remote=%v): %d", c.remote, rr.Code)
		}
	}
	if _, ok := pm.GetProfile().Admin(); !ok {
		t.Fatal("admin gone")
	}
}

func TestEnsureAdminFromEnv(t *testing.T) {
	_, _, pm, _, _, _ := setupFullTestEnvironment(t)
	env := map[string]string{"BOMBECAM_ADMIN_NAME": "root", "BOMBECAM_ADMIN_PASSWORD": "from the env 123"}
	ensureAdminFromEnv(pm, func(k string) string { return env[k] })
	a, ok := pm.GetProfile().Admin()
	if !ok || a.Name != "root" || !auth.VerifyPassword(a.PasswordHash.Expose(), "from the env 123") {
		t.Fatalf("admin from env: %+v", a)
	}
	env["BOMBECAM_ADMIN_PASSWORD"] = "a different one 456"
	ensureAdminFromEnv(pm, func(k string) string { return env[k] })
	if a, _ := pm.GetProfile().Admin(); !auth.VerifyPassword(a.PasswordHash.Expose(), "from the env 123") {
		t.Fatal("an existing administrator must not be changed from the environment")
	}
}

// A profile that can't be opened is reported to this PC, which can then
// only start again with Delete all.
func TestAdminStatusReportsUnreadableProfile(t *testing.T) {
	_, _, pm, _, mux, path := setupFullTestEnvironment(t)
	_ = pm
	prev := GatewayOperatorManager()
	SetGatewayOperatorManager(auth.NewOperatorManager(auth.OperatorConfig{}))
	t.Cleanup(func() { SetGatewayOperatorManager(prev) })
	if err := os.WriteFile(path, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	local := &adminClient{t: t, handler: SecurityBoundaryHandler(mux)}
	if _, st := local.do("GET", "/api/v1/admin/status", ""); st["profile_unreadable"] != true {
		t.Fatalf("status: %v", st)
	}
	if rr, out := local.do("POST", "/api/v1/profile/forget", ""); rr.Code != 200 {
		t.Fatalf("delete all: %d %v", rr.Code, out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the damaged profile should be gone")
	}
	if _, st := local.do("GET", "/api/v1/admin/status", ""); st["profile_unreadable"] == true || st["can_create"] != true {
		t.Fatalf("after delete all: %v", st)
	}
}

// The password hint is shown only on the PC running BombeCam, may not give
// the password away, and can be changed with the password.
func TestAdminPasswordHint(t *testing.T) {
	h, pm := adminEnv(t)
	local := &adminClient{t: t, handler: h}
	if rr, out := local.do("POST", "/api/v1/admin/setup", `{"password":"blue horse 77","hint":"BLUE HORSE 77 obviously"}`); rr.Code != 400 || out["error"] != "bad_hint" {
		t.Fatalf("a hint containing the password: %d %v", rr.Code, out)
	}
	if rr, _ := local.do("POST", "/api/v1/admin/setup", `{"password":"blue horse 77","hint":"first pony, plus our old door number","require_local_sign_in":true}`); rr.Code != 200 {
		t.Fatalf("setup: %d", rr.Code)
	}
	fresh := &adminClient{t: t, handler: h}
	if _, st := fresh.do("GET", "/api/v1/admin/status", ""); st["password_hint"] != "first pony, plus our old door number" {
		t.Fatalf("this PC, signed out, should see the hint: %v", st)
	}
	lan := &adminClient{t: t, handler: h, remote: true}
	if _, st := lan.do("GET", "/api/v1/admin/status", ""); st["password_hint"] != nil {
		t.Fatalf("other devices must not see the hint: %v", st)
	}
	if rr, _ := local.do("POST", "/api/v1/admin/hint", `{"password":"wrong one 123","hint":"x"}`); rr.Code != 401 {
		t.Fatalf("changing the hint needs the password: %d", rr.Code)
	}
	if rr, _ := local.do("POST", "/api/v1/admin/hint", `{"password":"blue horse 77","hint":""}`); rr.Code != 200 {
		t.Fatalf("clear the hint: %d", rr.Code)
	}
	if a, _ := pm.GetProfile().Admin(); a.PasswordHint != "" {
		t.Fatal("hint not cleared")
	}
	local.do("POST", "/api/v1/admin/hint", `{"password":"blue horse 77","hint":"a new pony!! for sure"}`)
	if rr, out := local.do("POST", "/api/v1/admin/password", `{"password":"blue horse 77","new_password":"a new pony!!"}`); rr.Code != 400 || out["error"] != "bad_hint" {
		t.Fatalf("a new password the hint gives away: %d %v", rr.Code, out)
	}
}

// After a forgotten password, the PC running BombeCam (and only that PC)
// can delete all saved data, administrator included, and shut down.
func TestStartOverOnThisPCOnly(t *testing.T) {
	h, pm := adminEnv(t)
	shutdowns := make(chan struct{}, 2) // the handler shuts down on its own goroutine
	prev := startOverShutdown
	startOverShutdown = func() { shutdowns <- struct{}{} }
	t.Cleanup(func() { startOverShutdown = prev })
	seedAdmin(t, pm, "admin", "long enough pw", true)

	lan := &adminClient{t: t, handler: h, remote: true}
	if rr, _ := lan.do("POST", "/api/v1/admin/start-over", ""); rr.Code != http.StatusForbidden {
		t.Fatalf("another device: %d", rr.Code)
	}
	if _, ok := pm.GetProfile().Admin(); !ok {
		t.Fatal("a refused start-over changed something")
	}
	local := &adminClient{t: t, handler: h} // signed out, sign-in required on this PC
	rr, out := local.do("POST", "/api/v1/admin/start-over", "")
	if rr.Code != 200 || out["status"] != "shutting_down" {
		t.Fatalf("start over: %d %v", rr.Code, out)
	}
	if has, _ := pm.HasProfile(context.Background()); has {
		t.Fatal("saved data still on disk")
	}
	select {
	case <-shutdowns:
	case <-time.After(2 * time.Second):
		t.Fatal("BombeCam should shut down")
	}
	time.Sleep(20 * time.Millisecond)
	if n := len(shutdowns); n != 0 {
		t.Fatalf("BombeCam shut down %d more time(s)", n)
	}
}
