package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/Fever-r/BombeCam/internal/buildkey"
)

// inSource runs the test in an empty source folder with nothing set up.
func inSource(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	if err := os.WriteFile("go.mod", []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, v := range buildkey.Values {
		t.Setenv(v.Env, "")
	}
}

func setup(t *testing.T, input string, given map[string]string, ifMissing, ldflags bool) (string, string, error) {
	t.Helper()
	var out, msg bytes.Buffer
	err := run(strings.NewReader(input), &out, &msg, given, ifMissing, ldflags)
	return out.String(), msg.String(), err
}

func saved(t *testing.T) map[string]string {
	t.Helper()
	v, err := buildkey.ReadFile(buildkey.File)
	if err != nil {
		t.Fatalf("%s: %v", buildkey.File, err)
	}
	return v
}

func TestLDFlags(t *testing.T) {
	inSource(t)
	if out, msg, err := setup(t, "", nil, false, true); err != nil || out != "" || !strings.Contains(msg, "no Osaio server key") || !strings.Contains(msg, "no Osaio app ID") {
		t.Fatalf("nothing set up: out=%q msg=%q err=%v", out, msg, err)
	}
	if err := buildkey.Write(buildkey.File, map[string]string{"server_key": "file-key", "app_id": "file-app"}); err != nil {
		t.Fatal(err)
	}
	out, _, err := setup(t, "", nil, false, true)
	if err != nil || strings.TrimSpace(out) != buildkey.LDFlags(buildkey.Found{Value: map[string]string{"server_key": "file-key", "app_id": "file-app"}}) {
		t.Fatalf("with both: out=%q err=%v", out, err)
	}
	if strings.Contains(out, "file-key") || strings.Contains(out, "file-app") {
		t.Fatal("the flags show a value in plain text")
	}
	if err := os.WriteFile(buildkey.File, []byte("server_key=a\nserver_key=b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _, err := setup(t, "", nil, false, true); err == nil || out != "" {
		t.Fatalf("a broken file must fail the build: out=%q err=%v", out, err)
	}
}

func TestAskSavesSkipsAndKeeps(t *testing.T) {
	inSource(t)
	if out, _, err := setup(t, "\n\n", nil, false, false); err != nil || !strings.Contains(out, "Nothing changed") {
		t.Fatalf("Enter twice: %q %v", out, err)
	}
	if _, err := os.Stat(buildkey.File); !os.IsNotExist(err) {
		t.Fatal("skipping wrote a file")
	}
	if out, _, err := setup(t, " pasted-key\r\n\n", nil, false, false); err != nil || saved(t)["server_key"] != "pasted-key" || !strings.Contains(out, "No Osaio app ID yet") {
		t.Fatalf("key only (CRLF): %q %v", out, err)
	}
	if _, _, err := setup(t, "\npasted-app\n", nil, true, false); err != nil || saved(t)["server_key"] != "pasted-key" || saved(t)["app_id"] != "pasted-app" {
		t.Fatalf("-if-missing must ask for the app ID and keep the key: %v %v", saved(t), err)
	}
	if out, _, err := setup(t, "", nil, true, false); err != nil || !strings.Contains(out, "Builds include") {
		t.Fatalf("-if-missing with both must not ask: %q %v", out, err)
	}
	if _, _, err := setup(t, "", nil, false, false); err != nil || saved(t)["app_id"] != "pasted-app" {
		t.Fatalf("Enter at EOF must keep the values: %v", err)
	}
	if _, _, err := setup(t, "bad value\n", nil, false, false); err == nil {
		t.Fatal("a broken value was accepted")
	}
}

// A broken osaio-setup.txt can't be skipped: the build would fail on it, so
// setup says how to get out (working values, or deleting the file).
func TestBrokenFileCannotBeSkipped(t *testing.T) {
	inSource(t)
	if err := os.WriteFile(buildkey.File, []byte("just-a-key-line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := setup(t, "\n\n", nil, true, false)
	if err == nil || !strings.Contains(err.Error(), "delete the file") || strings.Contains(out, "Nothing changed") {
		t.Fatalf("Enter on a broken file: out=%q err=%v", out, err)
	}
	if _, _, err := setup(t, "good-key\ngood-app\n", nil, true, false); err != nil || saved(t)["server_key"] != "good-key" || saved(t)["app_id"] != "good-app" {
		t.Fatalf("pasted values must replace the broken file: %v", err)
	}
}

// Fixing one broken line keeps the other value, whichever way it is fixed.
func TestFixingOneLineKeepsTheOther(t *testing.T) {
	inSource(t)
	if err := os.WriteFile(buildkey.File, []byte("server_key=good-key\napp_id=bad value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := setup(t, "", map[string]string{"app_id": "good-app"}, false, false); err != nil {
		t.Fatal(err)
	}
	if v := saved(t); v["server_key"] != "good-key" || v["app_id"] != "good-app" {
		t.Fatalf("-app-id on a broken file: saved = %v; want the key kept", v)
	}
	if err := os.WriteFile(buildkey.File, []byte("server_key=good-key\napp_id=bad value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _, err := setup(t, "\ntyped-app\n", nil, true, false); err != nil || saved(t)["server_key"] != "good-key" || !strings.Contains(out, "Enter keeps the one saved") {
		t.Fatalf("interactive fix: out=%q err=%v saved=%v", out, err, saved(t))
	}
	raw, _ := os.ReadFile(buildkey.File)
	if strings.Contains(string(raw), "good-key") || strings.Contains(string(raw), "typed-app") {
		t.Fatalf("setup wrote a value in plain text:\n%s", raw)
	}
}

func TestFlagsSaveWithoutAsking(t *testing.T) {
	inSource(t)
	if _, _, err := setup(t, "", map[string]string{"server_key": "flag-key", "app_id": "flag-app"}, false, false); err != nil {
		t.Fatal(err)
	}
	if v := saved(t); v["server_key"] != "flag-key" || v["app_id"] != "flag-app" {
		t.Fatalf("saved = %v", v)
	}
	if _, _, err := setup(t, "", map[string]string{"app_id": "new-app"}, false, false); err != nil || saved(t)["server_key"] != "flag-key" || saved(t)["app_id"] != "new-app" {
		t.Fatalf("-app-id alone must keep the key: %v %v", saved(t), err)
	}
}

// An environment variable wins over the file, so setup says so, and refuses
// to go on while one is broken.
func TestEnvironmentValues(t *testing.T) {
	inSource(t)
	t.Setenv(buildkey.ServerKey.Env, "env-key")
	out, _, err := setup(t, "", map[string]string{"server_key": "new-key"}, false, false)
	if err != nil || !strings.Contains(out, buildkey.ServerKey.Env+" is set") || saved(t)["server_key"] != "new-key" {
		t.Fatalf("-key with the variable set: out=%q err=%v", out, err)
	}
	t.Setenv(buildkey.AppID.Env, "two words")
	if _, _, err := setup(t, "", map[string]string{"server_key": "other-key"}, false, false); err == nil || !strings.Contains(err.Error(), "clear "+buildkey.AppID.Env) {
		t.Fatalf("with a broken variable: %v", err)
	}
	if saved(t)["server_key"] != "new-key" {
		t.Fatal("a broken variable must stop -key from saving")
	}
}

func TestOutsideTheSource(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, _, err := setup(t, "", nil, false, true); err == nil {
		t.Fatal("ran outside the source folder")
	}
}
