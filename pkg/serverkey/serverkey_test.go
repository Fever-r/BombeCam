package serverkey

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testKey = "synthetic-test-key-0001"

func env(vals map[string]string) func(string) string {
	return func(k string) string { return vals[k] }
}

func writeRaw(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadPrecedence(t *testing.T) {
	dir := t.TempDir()
	flagFile := writeRaw(t, dir, "flag.key", "flag-key\n")
	envFile := writeRaw(t, dir, "env.key", "env-file-key\n")
	def := writeRaw(t, dir, FileName, "default-key\n")

	cases := []struct {
		name   string
		opts   Options
		want   string
		source Source
	}{
		{"flag beats everything", Options{FlagFile: flagFile, DefaultFile: def, Getenv: env(map[string]string{EnvKey: "env-key", EnvKeyFile: envFile})}, "flag-key", SourceFlag},
		{"env value beats env file", Options{DefaultFile: def, Getenv: env(map[string]string{EnvKey: "env-key", EnvKeyFile: envFile})}, "env-key", SourceEnv},
		{"env file beats default", Options{DefaultFile: def, Getenv: env(map[string]string{EnvKeyFile: envFile})}, "env-file-key", SourceEnvFile},
		{"default file last", Options{DefaultFile: def, Getenv: env(nil)}, "default-key", SourceFile},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Load(c.opts)
			got, err := s.Key()
			if err != nil || got != c.want {
				t.Fatalf("Key() = %q, %v; want %q", got, err, c.want)
			}
			if st := s.Status(); st.Source != c.source || !st.Configured {
				t.Fatalf("Status() = %+v; want source %q", st, c.source)
			}
		})
	}
}

func TestNothingConfigured(t *testing.T) {
	s := Load(Options{DefaultFile: filepath.Join(t.TempDir(), FileName), Getenv: env(nil)})
	if _, err := s.Key(); !errors.Is(err, ErrNoKey) {
		t.Fatalf("Key() error = %v; want ErrNoKey", err)
	}
	st := s.Status()
	if st.Configured || st.Source != SourceNone || !st.Editable || st.Problem != "" {
		t.Fatalf("Status() = %+v", st)
	}
}

// A source that is set but broken must not fall through to a later one.
func TestBrokenSourceIsReportedNotSkipped(t *testing.T) {
	dir := t.TempDir()
	def := writeRaw(t, dir, FileName, "default-key\n")
	s := Load(Options{FlagFile: filepath.Join(dir, "missing.key"), DefaultFile: def, Getenv: env(nil)})
	_, err := s.Key()
	if !errors.Is(err, ErrNoKey) || !strings.Contains(err.Error(), "the file named by -server-key-file does not exist") {
		t.Fatalf("Key() error = %v", err)
	}
	if st := s.Status(); st.Configured || st.Editable || st.Source != SourceFlag {
		t.Fatalf("Status() = %+v", st)
	}
}

func TestReadFileFormat(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		body, want, errPart string
	}{
		{"abc123\n", "abc123", ""},
		{"\uFEFF# note\r\n\r\n  abc123  \r\n", "abc123", ""},
		{"# only a note\n", "", "has no key"},
		{"one\ntwo\n", "", "more than one key line"},
		{"has space\n", "", "spaces or line breaks"},
		{strings.Repeat("k", MaxLength+1), "", "longer than"},
		{strings.Repeat("#", maxFileSize+1), "", "larger than 16 KB"},
	}
	for i, c := range cases {
		p := writeRaw(t, dir, "k"+string(rune('a'+i)), c.body)
		got, err := ReadFile(p)
		if c.errPart == "" {
			if err != nil || got != c.want {
				t.Errorf("case %d: got %q, %v; want %q", i, got, err, c.want)
			}
			continue
		}
		if !errors.Is(err, ErrNoKey) || !strings.Contains(err.Error(), c.errPart) {
			t.Errorf("case %d: error = %v; want one mentioning %q", i, err, c.errPart)
		}
	}
}

func TestSaveWritesDefaultFileAndAppliesAtOnce(t *testing.T) {
	def := filepath.Join(t.TempDir(), "data", FileName)
	s := Load(Options{DefaultFile: def, Getenv: env(nil)})
	if err := s.Save("  " + testKey + "\n"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Key(); err != nil || got != testKey {
		t.Fatalf("Key() after Save = %q, %v", got, err)
	}
	if got, err := ReadFile(def); err != nil || got != testKey {
		t.Fatalf("file after Save = %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(def)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("key file mode = %v; want 0600", fi.Mode().Perm())
		}
	}
	// A restart reads the saved file back.
	if got, _ := Load(Options{DefaultFile: def, Getenv: env(nil)}).Key(); got != testKey {
		t.Fatalf("reloaded key = %q", got)
	}
}

func TestSaveRefusedWhenSetOutsideThePage(t *testing.T) {
	def := filepath.Join(t.TempDir(), FileName)
	s := Load(Options{DefaultFile: def, Getenv: env(map[string]string{EnvKey: "env-key"})})
	if err := s.Save(testKey); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("Save() error = %v; want ErrNotEditable", err)
	}
	if got, _ := s.Key(); got != "env-key" {
		t.Fatalf("Key() = %q; the environment key must stay in use", got)
	}
	if _, err := os.Stat(def); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Save wrote %s although it was refused", def)
	}
}

func TestSaveRejectsBadKeyAndKeepsOldOne(t *testing.T) {
	def := filepath.Join(t.TempDir(), FileName)
	s := Load(Options{DefaultFile: def, Getenv: env(nil)})
	if err := s.Save(testKey); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("two words"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("Save(bad) error = %v", err)
	}
	if got, _ := s.Key(); got != testKey {
		t.Fatalf("Key() = %q after a rejected save", got)
	}
}

// The status must never carry the key itself.
func TestStatusDoesNotRevealKey(t *testing.T) {
	s := Load(Options{Getenv: env(map[string]string{EnvKey: testKey})})
	st := s.Status()
	if strings.Contains(st.Problem, testKey) || string(st.Source) == testKey {
		t.Fatalf("Status() leaks the key: %+v", st)
	}
}

const builtInKey = "synthetic-built-in-key"

// With nothing else set, the built-in key is used, and the page can replace
// it, which takes effect at once.
func TestBuiltInKeyIsUsedAndCanBeReplaced(t *testing.T) {
	def := filepath.Join(t.TempDir(), FileName)
	s := Load(Options{DefaultFile: def, BuiltIn: builtInKey, Getenv: env(nil)})
	if got, err := s.Key(); err != nil || got != builtInKey {
		t.Fatalf("Key() = %q, %v; want the built-in key", got, err)
	}
	if st := s.Status(); st.Source != SourceBuiltIn || !st.Configured || !st.Editable || !st.HasBuiltIn {
		t.Fatalf("Status() = %+v", st)
	}
	if err := s.Save(testKey); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Key(); got != testKey || s.Status().Source != SourceFile {
		t.Fatalf("after Save: key %q source %q", got, s.Status().Source)
	}
	// A restart keeps the saved key over the built-in one.
	if got, _ := Load(Options{DefaultFile: def, BuiltIn: builtInKey, Getenv: env(nil)}).Key(); got != testKey {
		t.Fatalf("restart used %q; want the saved key", got)
	}
}

// Reset, or saving the built-in value, removes the saved key so a newer
// BombeCam's built-in key is not hidden by an old saved one.
func TestResetReturnsToBuiltInKey(t *testing.T) {
	for _, how := range []string{"reset", "save built-in"} {
		t.Run(how, func(t *testing.T) {
			def := filepath.Join(t.TempDir(), FileName)
			s := Load(Options{DefaultFile: def, BuiltIn: builtInKey, Getenv: env(nil)})
			if err := s.Save(testKey); err != nil {
				t.Fatal(err)
			}
			var err error
			if how == "reset" {
				err = s.Reset()
			} else {
				err = s.Save(" " + builtInKey + "\n")
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := s.Key(); got != builtInKey || s.Status().Source != SourceBuiltIn {
				t.Fatalf("key %q source %q; want the built-in key", got, s.Status().Source)
			}
			if _, err := os.Stat(def); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s still exists after %s", FileName, how)
			}
		})
	}
}

// A key set on the command line or in the environment beats the built-in
// key, and the page cannot reset it.
func TestEnvironmentKeyBeatsBuiltIn(t *testing.T) {
	s := Load(Options{DefaultFile: filepath.Join(t.TempDir(), FileName), BuiltIn: builtInKey, Getenv: env(map[string]string{EnvKey: "env-key"})})
	if got, _ := s.Key(); got != "env-key" {
		t.Fatalf("Key() = %q; want the environment key", got)
	}
	if err := s.Reset(); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("Reset() error = %v; want ErrNotEditable", err)
	}
}

func TestStatic(t *testing.T) {
	if k, err := Static(testKey).Key(); err != nil || k != testKey {
		t.Fatalf("Static.Key() = %q, %v", k, err)
	}
	if _, err := Static("").Key(); !errors.Is(err, ErrNoKey) {
		t.Fatalf("empty Static error = %v", err)
	}
}
