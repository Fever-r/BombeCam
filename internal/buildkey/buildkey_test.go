package buildkey

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Fever-r/BombeCam/pkg/bridge"
	"github.com/Fever-r/BombeCam/pkg/serverkey"
)

const (
	testKey   = "0123abcdEF+/=._-key"
	testAppID = "synthetic-app-id-77"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func write(t *testing.T, root, body string) string {
	t.Helper()
	path := filepath.Join(root, File)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEnvNamesMatchRunTime(t *testing.T) {
	if ServerKey.Env != serverkey.EnvKey || AppID.Env != bridge.EnvAppID {
		t.Fatal("build-time and run-time environment variable names differ")
	}
}

func TestFindReadsTheFileAndEnvironmentWins(t *testing.T) {
	root := t.TempDir()
	path := write(t, root, "# note\nserver_key = file-key\napp_id=file-app\n")
	f, err := Find(root, env(map[string]string{AppID.Env: " env-app "}))
	if err != nil {
		t.Fatal(err)
	}
	if f.Value["server_key"] != "file-key" || f.From["server_key"] != path || f.Value["app_id"] != "env-app" || f.From["app_id"] != AppID.Env {
		t.Fatalf("Find = %+v", f)
	}
	if len(f.Missing()) != 0 {
		t.Fatalf("Missing = %v", f.Missing())
	}
}

func TestFindWithNothingSetUp(t *testing.T) {
	f, err := Find(t.TempDir(), env(nil))
	if err != nil || len(f.Missing()) != 2 || LDFlags(f) != "" {
		t.Fatalf("Find = %+v, %v", f, err)
	}
	root := t.TempDir()
	write(t, root, "server_key=\napp_id=\n") // an empty value means not set up
	if f, err := Find(root, env(nil)); err != nil || len(f.Missing()) != 2 {
		t.Fatalf("empty values: %+v, %v", f, err)
	}
}

// A value that is set but broken stops the build instead of building without it.
func TestFindRejectsBrokenValues(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"two words", "quote\"", "dollar$sign", "back`tick", "semi;colon"} {
		_, err := Find(root, env(map[string]string{ServerKey.Env: bad}))
		var se *SourceError
		if !errors.As(err, &se) || se.From != ServerKey.Env || !errors.Is(err, ErrUnusable) {
			t.Errorf("Find(%q) error = %v; want a SourceError for %s", bad, err, ServerKey.Env)
		}
	}
	for body, want := range map[string]Value{
		"just-a-key\n":                         {}, // the file as a whole
		"colour=red\n":                         {},
		"server_key=a\nserver_key=b\n":         ServerKey,
		"server_key=good\napp_id=has space\n":  AppID,
		"server_key=good\napp_id=obs1.!!!\n":   AppID,
		"app_id=fine\nserver_key=bad\"quote\n": ServerKey,
	} {
		path := write(t, root, body)
		_, err := Find(root, env(nil))
		var se *SourceError
		if !errors.As(err, &se) || se.From != path || se.Value != want {
			t.Errorf("file %q: error = %v; want a SourceError for %q in the file", body, err, want.Name)
		}
		if want.Name != "" && !strings.HasPrefix(err.Error(), want.Label) {
			t.Errorf("file %q: error %q must name the %s", body, err, want.Label)
		}
	}
}

// One broken line never hides the other value, and a variable that supplies
// the broken one lets the build go on.
func TestBrokenLineKeepsTheOtherValue(t *testing.T) {
	root := t.TempDir()
	path := write(t, root, "server_key=good-key\napp_id=has space\n")
	got, err := ReadFile(path)
	if err == nil || got["server_key"] != "good-key" {
		t.Fatalf("ReadFile = %v, %v; want the key and an error", got, err)
	}
	f, err := Find(root, env(map[string]string{AppID.Env: "env-app"}))
	if err != nil || f.Value["server_key"] != "good-key" || f.Value["app_id"] != "env-app" {
		t.Fatalf("Find with the app ID from the environment = %+v, %v", f, err)
	}
}

func TestWriteObscuresAndReadReveals(t *testing.T) {
	path := filepath.Join(t.TempDir(), File)
	if err := Write(path, map[string]string{"server_key": " " + testKey + "\n", "app_id": testAppID}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testKey) || strings.Contains(string(raw), testAppID) {
		t.Fatalf("the file shows a value in plain text:\n%s", raw)
	}
	got, err := ReadFile(path)
	if err != nil || got["server_key"] != testKey || got["app_id"] != testAppID {
		t.Fatalf("ReadFile = %v, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, %v; want 0600", st.Mode().Perm(), err)
		}
	}
	if err := Write(path, map[string]string{"app_id": "has space"}); err == nil {
		t.Fatal("Write accepted a broken value")
	}
}

func TestLDFlagsHideTheValues(t *testing.T) {
	f := Found{Value: map[string]string{"server_key": testKey, "app_id": testAppID}}
	flags := LDFlags(f)
	if !strings.Contains(flags, "-X "+ServerKey.Symbol+"=obs1.") || !strings.Contains(flags, "-X "+AppID.Symbol+"=obs1.") {
		t.Fatalf("LDFlags = %q", flags)
	}
	if strings.Contains(flags, testKey) || strings.Contains(flags, testAppID) {
		t.Fatal("LDFlags shows a value in plain text")
	}
}

// The flags must set both values in a real build, and neither may appear in
// the binary as plain text: a renamed variable would otherwise make every
// release silently lack them.
func TestLDFlagsSetTheValues(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program")
	}
	bin := filepath.Join(t.TempDir(), "printkey")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	f := Found{Value: map[string]string{"server_key": testKey, "app_id": testAppID}}
	build := exec.Command("go", "build", "-ldflags", LDFlags(f), "-o", bin, "./testdata/printkey")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	run := exec.Command(bin)
	run.Env = append(os.Environ(), AppID.Env+"=")
	out, err := run.Output()
	if err != nil || strings.TrimSpace(string(out)) != testKey+" "+testAppID {
		t.Fatalf("built-in values = %q, %v; want %q", out, err, testKey+" "+testAppID)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(testKey)) || bytes.Contains(data, []byte(testAppID)) {
		t.Fatal("the binary contains a value in plain text")
	}
}

// Neither value is in the source: the variables the linker sets are never
// given a value in the code of pkg/serverkey or pkg/bridge (no initial value,
// no assignment anywhere), and LatestKnown only reveals what the linker set.
func TestNoValuesInSource(t *testing.T) {
	linkerSet := map[string]bool{"builtIn": true, "builtInAppID": true, "LatestKnown": true, "LatestKnownAppID": true}
	// The plain forms may only be revealed from the linker-set variables.
	revealedFrom := map[string]string{"LatestKnown": "builtIn", "LatestKnownAppID": "builtInAppID"}
	for _, dir := range []string{"../../pkg/serverkey", "../../pkg/bridge"} {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, pkg := range pkgs {
			for name, file := range pkg.Files {
				ast.Inspect(file, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.ValueSpec:
						for i, id := range n.Names {
							if !linkerSet[id.Name] || i >= len(n.Values) {
								continue
							}
							call, ok := n.Values[i].(*ast.CallExpr)
							if from := revealedFrom[id.Name]; from == "" || !ok || len(call.Args) != 2 || fmt.Sprint(call.Args[1]) != from {
								t.Errorf("%s: %s is given a value in the source; it comes from %s at build time", name, id.Name, File)
							}
						}
					case *ast.AssignStmt:
						for _, lhs := range n.Lhs {
							if id, ok := lhs.(*ast.Ident); ok && linkerSet[id.Name] {
								t.Errorf("%s: %s is assigned in the source; it comes from %s at build time", name, id.Name, File)
							}
						}
					}
					return true
				})
			}
		}
	}
}
