package osaiovalue_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Fever-r/BombeCam/pkg/bridge"
	"github.com/Fever-r/BombeCam/pkg/osaiovalue"
	"github.com/Fever-r/BombeCam/pkg/serverkey"
)

// Every test here runs once for the server key and once for the app ID, so
// the app ID is shown to work exactly like the server key. Values are made
// up and all start with "synthetic-"; file names never do, so a status or
// message that contains "synthetic" has leaked a value.

type kindCase struct {
	name string
	kind osaiovalue.Kind
	// The names and errors that the docs and deploy/ files rely on.
	wantName, wantNoun, wantFlag, wantEnvValue, wantEnvFile, wantFileName string
	wantErrNone, wantErrNotEditable                                       error
}

var kinds = []kindCase{
	{
		name: "server key", kind: serverkey.Kind,
		wantName: "server key", wantNoun: "key",
		wantFlag: "-server-key-file", wantEnvValue: "BOMBECAM_SERVER_KEY", wantEnvFile: "BOMBECAM_SERVER_KEY_FILE",
		wantFileName: "server.key",
		wantErrNone:  serverkey.ErrNoKey, wantErrNotEditable: serverkey.ErrNotEditable,
	},
	{
		name: "app ID", kind: bridge.AppIDKind,
		wantName: "app ID", wantNoun: "app ID",
		wantFlag: "-app-id-file", wantEnvValue: "BOMBECAM_APP_ID", wantEnvFile: "BOMBECAM_APP_ID_FILE",
		wantFileName: "app.id",
		wantErrNone:  bridge.ErrNoAppID, wantErrNotEditable: bridge.ErrAppIDNotEditable,
	},
}

func forEachKind(t *testing.T, f func(t *testing.T, kc kindCase)) {
	t.Helper()
	for _, kc := range kinds {
		t.Run(kc.name, func(t *testing.T) { f(t, kc) })
	}
}

// env returns a Getenv that sees only vals, never the real environment.
func env(vals map[string]string) func(string) string {
	return func(k string) string { return vals[k] }
}

func writeFile(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s exists (stat error %v); want it absent", filepath.Base(path), err)
	}
}

// checkNoLeak fails if the status, as the API sends it, carries a value.
func checkNoLeak(t *testing.T, st osaiovalue.Status) {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "synthetic") {
		t.Fatalf("status JSON carries a value: %s", b)
	}
}

func TestBothKindsDescribeTheirSources(t *testing.T) {
	forEachKind(t, func(t *testing.T, kc kindCase) {
		k := kc.kind
		got := [6]string{k.Name, k.Noun, k.Flag, k.EnvValue, k.EnvFile, k.FileName}
		want := [6]string{kc.wantName, kc.wantNoun, kc.wantFlag, kc.wantEnvValue, kc.wantEnvFile, kc.wantFileName}
		if got != want {
			t.Fatalf("names = %q; want %q", got, want)
		}
		if k.ErrNone != kc.wantErrNone || k.ErrNotEditable != kc.wantErrNotEditable {
			t.Fatalf("errors = %v / %v; want %v / %v", k.ErrNone, k.ErrNotEditable, kc.wantErrNone, kc.wantErrNotEditable)
		}
		if !strings.Contains(k.ErrNone.Error(), kc.wantName) || !strings.Contains(k.ErrNotEditable.Error(), kc.wantName) {
			t.Fatalf("errors %q / %q do not name the %s", k.ErrNone, k.ErrNotEditable, kc.wantName)
		}
		// The header must read back as notes, or the saved file would hold
		// more than one value line.
		if k.FileHeader == "" || !strings.HasSuffix(k.FileHeader, "\n") {
			t.Fatalf("FileHeader = %q; want comment lines ending in a line break", k.FileHeader)
		}
		for _, line := range strings.Split(strings.TrimSuffix(k.FileHeader, "\n"), "\n") {
			if !strings.HasPrefix(line, "#") {
				t.Fatalf("FileHeader line %q is not a comment", line)
			}
		}
	})
}

// The two kinds never read each other's sources or match each other's errors.
func TestKindsAreSeparate(t *testing.T) {
	a, b := kinds[0].kind, kinds[1].kind
	if a.Flag == b.Flag || a.EnvValue == b.EnvValue || a.EnvFile == b.EnvFile || a.FileName == b.FileName {
		t.Fatalf("the kinds share a source name: %+v / %+v", a, b)
	}
	if errors.Is(a.ErrNone, b.ErrNone) || errors.Is(b.ErrNone, a.ErrNone) ||
		errors.Is(a.ErrNotEditable, b.ErrNotEditable) || errors.Is(b.ErrNotEditable, a.ErrNotEditable) {
		t.Fatal("the kinds share an error")
	}
	for i, kc := range kinds {
		other := kinds[1-i].kind
		t.Run(kc.name, func(t *testing.T) {
			dir := t.TempDir()
			otherFile := writeFile(t, filepath.Join(dir, "other-value"), "synthetic-other-file\n")
			writeFile(t, filepath.Join(dir, other.FileName), "synthetic-other-saved\n")
			s := osaiovalue.Load(kc.kind, osaiovalue.Options{
				DefaultFile: filepath.Join(dir, kc.kind.FileName),
				Getenv:      env(map[string]string{other.EnvValue: "synthetic-other-env", other.EnvFile: otherFile}),
			})
			_, err := s.Key()
			if !errors.Is(err, kc.kind.ErrNone) || errors.Is(err, other.ErrNone) {
				t.Fatalf("Key() error = %v; want only the %s's error", err, kc.name)
			}
			if st := s.Status(); st.Source != osaiovalue.SourceNone {
				t.Fatalf("Status() = %+v; the other kind's sources were used", st)
			}
		})
	}
}

func TestBothKindsLoadPrecedence(t *testing.T) {
	forEachKind(t, func(t *testing.T, kc kindCase) {
		k := kc.kind
		dir := t.TempDir()
		flagFile := writeFile(t, filepath.Join(dir, "flag-value"), "synthetic-flag\n")
		envFile := writeFile(t, filepath.Join(dir, "env-value"), "synthetic-env-file\n")
		dataFile := writeFile(t, filepath.Join(dir, "data", k.FileName), "synthetic-data-file\n")
		absentDataFile := filepath.Join(dir, "empty-data", k.FileName)
		const builtIn = "synthetic-built-in"

		cases := []struct {
			name     string
			opts     osaiovalue.Options
			want     string
			source   osaiovalue.Source
			editable bool
		}{
			{"command-line file beats everything",
				osaiovalue.Options{FlagFile: flagFile, DefaultFile: dataFile, BuiltIn: builtIn,
					Getenv: env(map[string]string{k.EnvValue: "synthetic-env", k.EnvFile: envFile})},
				"synthetic-flag", osaiovalue.SourceFlag, false},
			{"value variable beats file variable",
				osaiovalue.Options{FlagFile: "  ", DefaultFile: dataFile, BuiltIn: builtIn,
					Getenv: env(map[string]string{k.EnvValue: " synthetic-env\n", k.EnvFile: envFile})},
				"synthetic-env", osaiovalue.SourceEnv, false},
			{"file variable beats data-folder file",
				osaiovalue.Options{DefaultFile: dataFile, BuiltIn: builtIn,
					Getenv: env(map[string]string{k.EnvValue: "  ", k.EnvFile: envFile})},
				"synthetic-env-file", osaiovalue.SourceEnvFile, false},
			{"data-folder file beats built-in",
				osaiovalue.Options{DefaultFile: dataFile, BuiltIn: builtIn,
					Getenv: env(map[string]string{k.EnvValue: "", k.EnvFile: " "})},
				"synthetic-data-file", osaiovalue.SourceFile, true},
			{"built-in last",
				osaiovalue.Options{DefaultFile: absentDataFile, BuiltIn: " " + builtIn + "\n", Getenv: env(nil)},
				builtIn, osaiovalue.SourceBuiltIn, true},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				s := osaiovalue.Load(k, c.opts)
				if got, err := s.Key(); err != nil || got != c.want {
					t.Fatalf("Key() = %q, %v; want %q", got, err, c.want)
				}
				want := osaiovalue.Status{Configured: true, Source: c.source, Editable: c.editable, HasBuiltIn: true}
				if st := s.Status(); st != want {
					t.Fatalf("Status() = %+v; want %+v", st, want)
				}
				checkNoLeak(t, s.Status())
				if s.Kind().Name != kc.wantName {
					t.Fatalf("Kind() = %q", s.Kind().Name)
				}
			})
		}
	})
}

func TestBothKindsNothingConfigured(t *testing.T) {
	forEachKind(t, func(t *testing.T, kc kindCase) {
		for _, builtIn := range []string{"", "  \n", "synthetic built-in with spaces"} {
			s := osaiovalue.Load(kc.kind, osaiovalue.Options{
				DefaultFile: filepath.Join(t.TempDir(), kc.kind.FileName),
				BuiltIn:     builtIn,
				Getenv:      env(map[string]string{kc.kind.EnvValue: " ", kc.kind.EnvFile: ""}),
			})
			got, err := s.Key()
			if got != "" || !errors.Is(err, kc.kind.ErrNone) {
				t.Fatalf("built-in %q: Key() = %q, %v; want %v", builtIn, got, err, kc.kind.ErrNone)
			}
			if st, want := s.Status(), (osaiovalue.Status{Editable: true}); st != want {
				t.Fatalf("built-in %q: Status() = %+v; want %+v", builtIn, st, want)
			}
		}
	})
}

// A source that is set but broken is reported. BombeCam does not fall back
// to a later source or the built-in value, which could hide the mistake.
func TestBothKindsBrokenSourceIsReportedNotSkipped(t *testing.T) {
	forEachKind(t, func(t *testing.T, kc kindCase) {
		k, noun := kc.kind, kc.wantNoun
		order := []osaiovalue.Source{osaiovalue.SourceFlag, osaiovalue.SourceEnv, osaiovalue.SourceEnvFile, osaiovalue.SourceFile}
		cases := []struct {
			name    string
			broken  osaiovalue.Source
			breakIt func(dir string, o *osaiovalue.Options, vals map[string]string)
			wantMsg string
		}{
			{"missing command-line file", osaiovalue.SourceFlag, func(dir string, o *osaiovalue.Options, vals map[string]string) {
				o.FlagFile = filepath.Join(dir, "missing-file")
			}, "the file named by " + k.Flag + " does not exist"},
			{"malformed value variable", osaiovalue.SourceEnv, func(dir string, o *osaiovalue.Options, vals map[string]string) {
				vals[k.EnvValue] = "synthetic two words"
			}, "the " + noun + " contains spaces or line breaks"},
			{"empty file named in the environment", osaiovalue.SourceEnvFile, func(dir string, o *osaiovalue.Options, vals map[string]string) {
				vals[k.EnvFile] = writeFile(t, filepath.Join(dir, "empty-file"), "")
			}, "the file named in " + k.EnvFile + " has no " + noun + " in it"},
			{"data-folder file with two value lines", osaiovalue.SourceFile, func(dir string, o *osaiovalue.Options, vals map[string]string) {
				writeFile(t, o.DefaultFile, "synthetic-line-one\nsynthetic-line-two\n")
			}, k.FileName + " has more than one " + noun + " line"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				dir := t.TempDir()
				o := osaiovalue.Options{DefaultFile: filepath.Join(dir, "data", k.FileName), BuiltIn: "synthetic-built-in"}
				vals := map[string]string{}
				// Every source after the broken one holds a good value; none
				// of them may be used.
				later := false
				for _, src := range order {
					if !later {
						later = src == c.broken
						continue
					}
					switch src {
					case osaiovalue.SourceEnv:
						vals[k.EnvValue] = "synthetic-env"
					case osaiovalue.SourceEnvFile:
						vals[k.EnvFile] = writeFile(t, filepath.Join(dir, "env-value"), "synthetic-env-file\n")
					case osaiovalue.SourceFile:
						writeFile(t, o.DefaultFile, "synthetic-data-file\n")
					}
				}
				c.breakIt(dir, &o, vals)
				o.Getenv = env(vals)

				s := osaiovalue.Load(k, o)
				got, err := s.Key()
				if got != "" || !errors.Is(err, k.ErrNone) {
					t.Fatalf("Key() = %q, %v; want an error wrapping %v", got, err, k.ErrNone)
				}
				if !strings.Contains(err.Error(), c.wantMsg) {
					t.Fatalf("Key() error = %q; want it to say %q", err, c.wantMsg)
				}
				// Only the data-folder file is the web page's to fix.
				editable := c.broken == osaiovalue.SourceFile
				want := osaiovalue.Status{Source: c.broken, Editable: editable, HasBuiltIn: true, Problem: err.Error()}
				if st := s.Status(); st != want {
					t.Fatalf("Status() = %+v; want %+v", st, want)
				}
				checkNoLeak(t, s.Status())
				if !editable {
					if err := s.Save("synthetic-new"); !errors.Is(err, k.ErrNotEditable) {
						t.Fatalf("Save() error = %v; want %v", err, k.ErrNotEditable)
					}
					return
				}
				if err := s.Save("synthetic-new"); err != nil {
					t.Fatal(err)
				}
				if got, err := s.Key(); err != nil || got != "synthetic-new" {
					t.Fatalf("Key() after Save = %q, %v", got, err)
				}
				want = osaiovalue.Status{Configured: true, Source: osaiovalue.SourceFile, Editable: true, HasBuiltIn: true}
				if st := s.Status(); st != want {
					t.Fatalf("Status() after Save = %+v; want %+v", st, want)
				}
			})
		}
	})
}

func TestBothKindsSave(t *testing.T) {
	forEachKind(t, func(t *testing.T, kc kindCase) {
		k := kc.kind
		for _, c := range []struct{ name, builtIn string }{
			{"without a built-in value", ""},
			{"over the built-in value", "synthetic-built-in"},
		} {
			builtIn := c.builtIn
			t.Run(c.name, func(t *testing.T) {
				dataFile := filepath.Join(t.TempDir(), "data", k.FileName) // the folder does not exist yet
				o := osaiovalue.Options{DefaultFile: dataFile, BuiltIn: builtIn, Getenv: env(nil)}
				s := osaiovalue.Load(k, o)
				for _, v := range []string{"synthetic-first", "synthetic-second"} {
					if err := s.Save("\uFEFF  " + v + " \r\n"); err != nil {
						t.Fatal(err)
					}
					if got, err := s.Key(); err != nil || got != v {
						t.Fatalf("Key() right after Save = %q, %v; want %q", got, err, v)
					}
					want := osaiovalue.Status{Configured: true, Source: osaiovalue.SourceFile, Editable: true, HasBuiltIn: builtIn != ""}
					if st := s.Status(); st != want {
						t.Fatalf("Status() = %+v; want %+v", st, want)
					}
					checkNoLeak(t, s.Status())
					if got, want := readFile(t, dataFile), k.FileHeader+v+"\n"; got != want {
						t.Fatalf("%s holds %q; want %q", k.FileName, got, want)
					}
				}
				if runtime.GOOS != "windows" {
					fi, err := os.Stat(dataFile)
					if err != nil {
						t.Fatal(err)
					}
					if fi.Mode().Perm() != 0o600 {
						t.Fatalf("%s mode = %v; want 0600", k.FileName, fi.Mode().Perm())
					}
				}
				// A bad value is refused and the saved one stays.
				if err := s.Save("synthetic bad value"); !errors.Is(err, k.ErrNone) {
					t.Fatalf("Save(bad) error = %v; want %v", err, k.ErrNone)
				}
				if got, _ := s.Key(); got != "synthetic-second" {
					t.Fatalf("Key() = %q after a refused Save", got)
				}
				if got := readFile(t, dataFile); got != k.FileHeader+"synthetic-second\n" {
					t.Fatalf("a refused Save changed %s to %q", k.FileName, got)
				}
				// A restart reads the saved value back.
				if got, err := osaiovalue.Load(k, o).Key(); err != nil || got != "synthetic-second" {
					t.Fatalf("after restart Key() = %q, %v", got, err)
				}
				if got, err := k.ReadFile(dataFile); err != nil || got != "synthetic-second" {
					t.Fatalf("ReadFile = %q, %v", got, err)
				}
			})
		}
	})
}

// Saving the built-in value removes the saved file, so a newer BombeCam's
// built-in value is not hidden by an old saved one.
func TestBothKindsSaveBuiltInRemovesFile(t *testing.T) {
	forEachKind(t, func(t *testing.T, kc kindCase) {
		k := kc.kind
		const builtIn = "synthetic-built-in"
		dataFile := filepath.Join(t.TempDir(), k.FileName)
		s := osaiovalue.Load(k, osaiovalue.Options{DefaultFile: dataFile, BuiltIn: builtIn, Getenv: env(nil)})
		if err := s.Save(builtIn); err != nil {
			t.Fatal(err)
		}
		mustNotExist(t, dataFile)
		if err := s.Save("synthetic-saved"); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(" " + builtIn + "\n"); err != nil {
			t.Fatal(err)
		}
		mustNotExist(t, dataFile)
		if got, err := s.Key(); err != nil || got != builtIn {
			t.Fatalf("Key() = %q, %v; want the built-in value", got, err)
		}
		want := osaiovalue.Status{Configured: true, Source: osaiovalue.SourceBuiltIn, Editable: true, HasBuiltIn: true}
		if st := s.Status(); st != want {
			t.Fatalf("Status() = %+v; want %+v", st, want)
		}
	})
}

func TestBothKindsReset(t *testing.T) {
	forEachKind(t, func(t *testing.T, kc kindCase) {
		k := kc.kind
		for _, c := range []struct {
			name, builtIn string
			// saved in this run, or found in the data folder at start
			savedHere bool
		}{
			{"to built-in after Save", "synthetic-built-in", true},
			{"to built-in from a file found at start", "synthetic-built-in", false},
			{"to none after Save", "", true},
			{"to none from a file found at start", "", false},
		} {
			t.Run(c.name, func(t *testing.T) {
				dataFile := filepath.Join(t.TempDir(), k.FileName)
				if !c.savedHere {
					writeFile(t, dataFile, "# a note\nsynthetic-found\n")
				}
				s := osaiovalue.Load(k, osaiovalue.Options{DefaultFile: dataFile, BuiltIn: c.builtIn, Getenv: env(nil)})
				if c.savedHere {
					if err := s.Save("synthetic-saved"); err != nil {
						t.Fatal(err)
					}
				}
				if st := s.Status(); st.Source != osaiovalue.SourceFile {
					t.Fatalf("Status() before Reset = %+v", st)
				}
				if err := s.Reset(); err != nil {
					t.Fatal(err)
				}
				mustNotExist(t, dataFile)
				got, err := s.Key()
				if c.builtIn != "" {
					if err != nil || got != c.builtIn {
						t.Fatalf("Key() after Reset = %q, %v; want the built-in value", got, err)
					}
					want := osaiovalue.Status{Configured: true, Source: osaiovalue.SourceBuiltIn, Editable: true, HasBuiltIn: true}
					if st := s.Status(); st != want {
						t.Fatalf("Status() = %+v; want %+v", st, want)
					}
				} else {
					if got != "" || !errors.Is(err, k.ErrNone) {
						t.Fatalf("Key() after Reset = %q, %v; want %v", got, err, k.ErrNone)
					}
					if st, want := s.Status(), (osaiovalue.Status{Editable: true}); st != want {
						t.Fatalf("Status() = %+v; want %+v", st, want)
					}
				}
				// Resetting again, with no file left, is fine.
				if err := s.Reset(); err != nil {
					t.Fatalf("second Reset: %v", err)
				}
			})
		}
	})
}

// A value set on the command line or in the environment is read once at
// start; the web page can neither change nor reset it, and writes nothing.
func TestBothKindsNotEditableFromFlagOrEnvironment(t *testing.T) {
	forEachKind(t, func(t *testing.T, kc kindCase) {
		k := kc.kind
		for _, c := range []struct {
			name   string
			opts   func(dir string) osaiovalue.Options
			want   string
			source osaiovalue.Source
		}{
			{"command-line file", func(dir string) osaiovalue.Options {
				return osaiovalue.Options{FlagFile: writeFile(t, filepath.Join(dir, "flag-value"), "synthetic-flag\n"), Getenv: env(nil)}
			}, "synthetic-flag", osaiovalue.SourceFlag},
			{"value variable", func(dir string) osaiovalue.Options {
				return osaiovalue.Options{Getenv: env(map[string]string{k.EnvValue: "synthetic-env"})}
			}, "synthetic-env", osaiovalue.SourceEnv},
			{"file variable", func(dir string) osaiovalue.Options {
				return osaiovalue.Options{Getenv: env(map[string]string{k.EnvFile: writeFile(t, filepath.Join(dir, "env-value"), "synthetic-env-file\n")})}
			}, "synthetic-env-file", osaiovalue.SourceEnvFile},
		} {
			for _, dataFileExists := range []bool{false, true} {
				name := c.name
				if dataFileExists {
					name += " with a data-folder file"
				}
				t.Run(name, func(t *testing.T) {
					dir := t.TempDir()
					o := c.opts(dir)
					o.DefaultFile = filepath.Join(dir, "data", k.FileName)
					o.BuiltIn = "synthetic-built-in"
					const dataBody = "synthetic-data-file\n"
					if dataFileExists {
						writeFile(t, o.DefaultFile, dataBody)
					}
					s := osaiovalue.Load(k, o)
					before := s.Status()
					if before.Editable || before.Source != c.source {
						t.Fatalf("Status() = %+v; want source %q, not editable", before, c.source)
					}
					if err := s.Save("synthetic-new"); !errors.Is(err, k.ErrNotEditable) {
						t.Fatalf("Save() error = %v; want %v", err, k.ErrNotEditable)
					}
					if err := s.Save(o.BuiltIn); !errors.Is(err, k.ErrNotEditable) {
						t.Fatalf("Save(built-in) error = %v; want %v", err, k.ErrNotEditable)
					}
					if err := s.Reset(); !errors.Is(err, k.ErrNotEditable) {
						t.Fatalf("Reset() error = %v; want %v", err, k.ErrNotEditable)
					}
					if got, err := s.Key(); err != nil || got != c.want {
						t.Fatalf("Key() = %q, %v; want %q to stay in use", got, err, c.want)
					}
					if after := s.Status(); after != before {
						t.Fatalf("Status() changed from %+v to %+v", before, after)
					}
					if dataFileExists {
						if got := readFile(t, o.DefaultFile); got != dataBody {
							t.Fatalf("%s changed to %q", k.FileName, got)
						}
					} else {
						mustNotExist(t, o.DefaultFile)
					}
				})
			}
		}
	})
}

func TestBothKindsClean(t *testing.T) {
	forEachKind(t, func(t *testing.T, kc kindCase) {
		k, noun := kc.kind, kc.wantNoun
		for _, c := range []struct {
			name, raw, want, errPart string
		}{
			{"plain", "synthetic-value", "synthetic-value", ""},
			{"surrounding space", " \t synthetic-value \r\n", "synthetic-value", ""},
			{"byte order mark", "\uFEFFsynthetic-value", "synthetic-value", ""},
			{"opaque characters", "synthetic+/=_.~:value", "synthetic+/=_.~:value", ""},
			{"maximum length", "synthetic-" + strings.Repeat("v", osaiovalue.MaxLength-10), "synthetic-" + strings.Repeat("v", osaiovalue.MaxLength-10), ""},
			{"empty", "", "", "the " + noun + " is empty"},
			{"only spaces", " \t\r\n", "", "the " + noun + " is empty"},
			{"space inside", "synthetic value", "", "the " + noun + " contains spaces or line breaks"},
			{"tab inside", "synthetic\tvalue", "", "the " + noun + " contains spaces or line breaks"},
			{"line break inside", "synthetic\nvalue", "", "the " + noun + " contains spaces or line breaks"},
			{"no-break space inside", "synthetic\u00a0value", "", "the " + noun + " contains spaces or line breaks"},
			{"control character", "synthetic\x00value", "", "the " + noun + " contains spaces or line breaks"},
			{"escape character", "synthetic\x1bvalue", "", "the " + noun + " contains spaces or line breaks"},
			{"too long", "synthetic-" + strings.Repeat("v", osaiovalue.MaxLength-9), "", "the " + noun + " is longer than 512 characters"},
		} {
			t.Run(c.name, func(t *testing.T) {
				got, err := k.Clean(c.raw)
				if c.errPart == "" {
					if err != nil || got != c.want {
						t.Fatalf("Clean(%q) = %q, %v; want %q", c.raw, got, err, c.want)
					}
					return
				}
				if got != "" || !errors.Is(err, k.ErrNone) || !strings.Contains(err.Error(), c.errPart) {
					t.Fatalf("Clean(%q) = %q, %v; want an error wrapping %v that says %q", c.raw, got, err, k.ErrNone, c.errPart)
				}
				if strings.Contains(err.Error(), "synthetic") {
					t.Fatalf("Clean error %q repeats the value", err)
				}
			})
		}
	})
}

// Every file source reads the same format: the value on a line of its own,
// with blank lines, # notes, a byte order mark and Windows line ends allowed.
func TestBothKindsFileFormat(t *testing.T) {
	forEachKind(t, func(t *testing.T, kc kindCase) {
		k, noun := kc.kind, kc.wantNoun
		bodies := []struct {
			name, body, want, errPart string
		}{
			{"value only", "synthetic-value", "synthetic-value", ""},
			{"notes, BOM and CRLF", "\uFEFF# a note\r\n\r\n  synthetic-value  \r\n# another note\r\n", "synthetic-value", ""},
			{"as the web page writes it", k.FileHeader + "synthetic-value\n", "synthetic-value", ""},
			{"BOM before the value", "\uFEFFsynthetic-value\n", "synthetic-value", ""},
			{"only notes", "# synthetic-value\n\n", "", "has no " + noun + " in it"},
			{"two value lines", "synthetic-one\r\nsynthetic-two\r\n", "", "has more than one " + noun + " line"},
			{"bad value", "synthetic value\n", "", "the " + noun + " contains spaces or line breaks"},
			{"too large", strings.Repeat("#", osaiovalue.MaxFileSize+1), "", "is larger than 16 KB"},
		}
		sources := []struct {
			name   string
			opts   func(path string) osaiovalue.Options
			source osaiovalue.Source
		}{
			{"command-line file", func(p string) osaiovalue.Options {
				return osaiovalue.Options{FlagFile: p, Getenv: env(nil)}
			}, osaiovalue.SourceFlag},
			{"file variable", func(p string) osaiovalue.Options {
				return osaiovalue.Options{Getenv: env(map[string]string{k.EnvFile: p})}
			}, osaiovalue.SourceEnvFile},
			{"data-folder file", func(p string) osaiovalue.Options {
				return osaiovalue.Options{DefaultFile: p, Getenv: env(nil)}
			}, osaiovalue.SourceFile},
		}
		for _, b := range bodies {
			for _, src := range sources {
				t.Run(b.name+"/"+src.name, func(t *testing.T) {
					path := writeFile(t, filepath.Join(t.TempDir(), k.FileName), b.body)
					s := osaiovalue.Load(k, src.opts(path))
					got, err := s.Key()
					if s.Status().Source != src.source {
						t.Fatalf("Status() = %+v; want source %q", s.Status(), src.source)
					}
					if b.errPart == "" {
						if err != nil || got != b.want {
							t.Fatalf("Key() = %q, %v; want %q", got, err, b.want)
						}
						return
					}
					if got != "" || !errors.Is(err, k.ErrNone) || !strings.Contains(err.Error(), b.errPart) {
						t.Fatalf("Key() = %q, %v; want an error wrapping %v that says %q", got, err, k.ErrNone, b.errPart)
					}
					checkNoLeak(t, s.Status())
				})
			}
		}
	})
}

// A value pasted into the file flag or variable by mistake is taken as a
// path that does not exist; the error must not repeat it, since it reaches
// the log and the web page.
func TestBothKindsFileErrorsDoNotEchoThePath(t *testing.T) {
	forEachKind(t, func(t *testing.T, kc kindCase) {
		k := kc.kind
		const pasted = "synthetic-pasted-value"
		for _, o := range []osaiovalue.Options{
			{FlagFile: pasted, Getenv: env(nil)},
			{Getenv: env(map[string]string{k.EnvFile: pasted})},
		} {
			s := osaiovalue.Load(k, o)
			_, err := s.Key()
			if err == nil || strings.Contains(err.Error(), pasted) || strings.Contains(s.Status().Problem, pasted) {
				t.Fatalf("Key() error = %v, Status = %+v; must not contain the path given", err, s.Status())
			}
		}
		if _, err := k.ReadFile(pasted); err == nil || strings.Contains(err.Error(), pasted) {
			t.Fatalf("ReadFile error = %v; must not contain the path given", err)
		}
	})
}
