// Package buildkey finds the Osaio values that BombeCam builds include (the
// server key and the app ID) and turns them into linker flags. Neither value
// is in the source code: they come from osaio-setup.txt at the top of the
// source tree (go run ./tools/setup writes it, git ignores it) or from the
// BOMBECAM_SERVER_KEY and BOMBECAM_APP_ID environment variables. In the file
// and in the binary they are obscured (internal/obscure), not plain text.
package buildkey

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Fever-r/BombeCam/internal/obscure"
)

// File is the setup file at the top of the source tree.
const File = "osaio-setup.txt"

// Value is one setting a build can include.
type Value struct {
	Name   string // its line in File: name=value
	Label  string // for people
	Env    string // overrides File, as at run time; release builds set it from a secret
	Symbol string // the variable it is linked into, obscured
}

var (
	ServerKey = Value{"server_key", "Osaio server key", "BOMBECAM_SERVER_KEY", "github.com/Fever-r/BombeCam/pkg/serverkey.builtIn"}
	AppID     = Value{"app_id", "Osaio app ID", "BOMBECAM_APP_ID", "github.com/Fever-r/BombeCam/pkg/bridge.builtInAppID"}
	// Values lists them in the order setup asks for them.
	Values = []Value{ServerKey, AppID}
)

// ErrUnusable means a value is set but can't be built in.
var ErrUnusable = errors.New("unusable setting")

// SourceError says which source (an environment variable or the file's path)
// can't be used and, when the problem is in one setting, which one.
type SourceError struct {
	Value Value // zero for a problem with the file as a whole
	From  string
	Err   error
}

func (e *SourceError) Error() string {
	if e.Value.Name == "" {
		return fmt.Sprintf("%s: %v", sourceName(e.From), e.Err)
	}
	return fmt.Sprintf("%s (%s): %v", e.Value.Label, sourceName(e.From), e.Err)
}

func (e *SourceError) Unwrap() error { return e.Err }

// Found is what a build will include: per value name, the value ("" when not
// set up) and where it came from (an environment variable or the file path).
type Found struct {
	Value map[string]string
	From  map[string]string
}

// Missing lists the values that are not set up.
func (f Found) Missing() []Value {
	var out []Value
	for _, v := range Values {
		if f.Value[v.Name] == "" {
			out = append(out, v)
		}
	}
	return out
}

// Find returns what a build in root includes. A value not set up at all is
// not an error; one that is set but unusable is a *SourceError, so a typo
// never silently builds a binary without it. A problem in the file is
// ignored only when the environment supplies what it would have.
func Find(root string, getenv func(string) string) (Found, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	f := Found{Value: map[string]string{}, From: map[string]string{}}
	path := filepath.Join(root, File)
	file, ferr := ReadFile(path)
	if errors.Is(ferr, os.ErrNotExist) {
		ferr = nil
	}
	var se *SourceError
	if ferr != nil && !errors.As(ferr, &se) {
		se = &SourceError{From: path, Err: ferr} // e.g. the file can't be opened
	}
	fromEnv := 0
	for _, v := range Values {
		if raw := getenv(v.Env); strings.TrimSpace(raw) != "" {
			val, err := Check(v, raw)
			if err != nil {
				return f, &SourceError{v, v.Env, err}
			}
			f.Value[v.Name], f.From[v.Name] = val, v.Env
			fromEnv++
			continue
		}
		if val := file[v.Name]; val != "" {
			f.Value[v.Name], f.From[v.Name] = val, path
		}
	}
	if se != nil {
		covered := (se.Value.Name == "" && fromEnv == len(Values)) || (se.Value.Name != "" && f.From[se.Value.Name] == se.Value.Env)
		if !covered {
			return f, se
		}
	}
	return f, nil
}

// ReadFile reads a setup file: name=value lines, the values obscured as Write
// leaves them or plain if typed by hand; blank lines and lines starting with
// # are ignored. It returns every value that reads fine, and the first
// problem as a *SourceError that names the setting when the problem is in
// one setting's line. A missing file gives an error that wraps
// os.ErrNotExist.
func ReadFile(path string) (map[string]string, error) {
	out := map[string]string{}
	fh, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer fh.Close()
	data, err := io.ReadAll(io.LimitReader(fh, 16<<10+1))
	if err != nil {
		return out, &SourceError{From: path, Err: err}
	}
	if len(data) > 16<<10 {
		return out, &SourceError{From: path, Err: fmt.Errorf("%w: %s is larger than 16 KB", ErrUnusable, File)}
	}
	var first error
	fail := func(v Value, err error) {
		if first == nil {
			first = &SourceError{Value: v, From: path, Err: err}
		}
	}
	seen := map[string]bool{}
	for n, line := range strings.Split(strings.TrimPrefix(string(data), "\uFEFF"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, raw, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		var v *Value
		for i := range Values {
			if Values[i].Name == name {
				v = &Values[i]
			}
		}
		switch {
		case !ok || v == nil:
			fail(Value{}, fmt.Errorf("%w: line %d of %s is not server_key=... or app_id=...", ErrUnusable, n+1, File))
			continue
		case seen[name]:
			fail(*v, fmt.Errorf("%w: %s sets %s twice", ErrUnusable, File, name))
			continue
		}
		seen[name] = true
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		plain := obscure.Reveal(v.Name, raw)
		if plain == "" {
			fail(*v, fmt.Errorf("%w: the %s in %s can't be read; set it again with go run ./tools/setup", ErrUnusable, v.Label, File))
			continue
		}
		val, err := Check(*v, plain)
		if err != nil {
			fail(*v, err)
			continue
		}
		out[name] = val
	}
	return out, first
}

// Write saves values (by name) to path, readable only by the current user.
func Write(path string, values map[string]string) error {
	var b strings.Builder
	b.WriteString("# Osaio values that BombeCam builds include (build.cmd, build.ps1, make,\n" +
		"# Docker and tools/release). They are obscured here and in the binary.\n" +
		"# Change them with go run ./tools/setup. Git ignores this file; keep it\n" +
		"# out of commits. Delete it to build without them.\n")
	for _, v := range Values {
		val := values[v.Name]
		if val != "" {
			var err error
			if val, err = Check(v, val); err != nil {
				return err
			}
			val = obscure.Hide(v.Name, val)
		}
		b.WriteString(v.Name + "=" + val + "\n")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".osaio-setup-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// LDFlags is the -ldflags argument that builds f's values in, obscured, or
// "" when there are none.
func LDFlags(f Found) string {
	var parts []string
	for _, v := range Values {
		if val := f.Value[v.Name]; val != "" {
			parts = append(parts, "-X "+v.Symbol+"="+obscure.Hide(v.Name, val))
		}
	}
	return strings.Join(parts, " ")
}

// Check trims raw and accepts it if a build can carry it: no spaces or
// control characters, at most 512 characters, and only letters, digits and
// + / = . _ - (the value passes through make, sh, PowerShell and the
// linker's flag parsing).
func Check(v Value, raw string) (string, error) {
	val := strings.TrimSpace(strings.TrimPrefix(raw, "\uFEFF"))
	switch {
	case val == "":
		return "", fmt.Errorf("%w: the %s is empty", ErrUnusable, v.Label)
	case len(val) > 512:
		return "", fmt.Errorf("%w: the %s is longer than 512 characters", ErrUnusable, v.Label)
	}
	for _, r := range val {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", fmt.Errorf("%w: the %s contains spaces or line breaks", ErrUnusable, v.Label)
		}
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("+/=._-", r)) {
			return "", fmt.Errorf("%w: the %s has the character %q, which can't be built in", ErrUnusable, v.Label, r)
		}
	}
	return val, nil
}

func sourceName(from string) string {
	if strings.HasPrefix(from, "BOMBECAM_") {
		return from
	}
	return filepath.Base(from)
}
