// Package osaiovalue supplies a value from the Osaio app that BombeCam needs
// for its cloud requests: the server key that signs them (pkg/serverkey) and
// the app ID they carry (pkg/bridge). Both work the same way. BombeCam ships
// the latest known value; users can replace it on the command line, in the
// environment, in a file, or on the web page, where a change applies at
// once. See docs/technical/CONFIGURATION.md.
package osaiovalue

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

// Kind describes one value: its names, its sources and its messages.
type Kind struct {
	Name       string // in messages: "server key", "app ID"
	Noun       string // the short form for file contents: "key", "app ID"
	Flag       string // the command-line flag naming a file: "-server-key-file"
	EnvValue   string // the environment variable holding the value
	EnvFile    string // the environment variable holding a file path
	FileName   string // the file in the data folder that the web page writes
	FileHeader string // comment lines at the top of that file
	// ErrNone means there is no usable value. Every error from this package
	// that explains why (a missing file, a malformed value) wraps it.
	ErrNone error
	// ErrNotEditable is returned by Store.Save and Store.Reset when the value
	// is set on the command line or in the environment.
	ErrNotEditable error
}

// MaxLength bounds a value. Real ones are far shorter.
const MaxLength = 512

// MaxFileSize bounds a file holding a value.
const MaxFileSize = 16 << 10

// Clean trims surrounding whitespace and rejects what cannot be the value.
// The value is otherwise treated as opaque: no length or alphabet is assumed.
func (k Kind) Clean(raw string) (string, error) {
	v := strings.TrimSpace(strings.TrimPrefix(raw, "\uFEFF"))
	if v == "" {
		return "", fmt.Errorf("%w: the %s is empty", k.ErrNone, k.Noun)
	}
	if len(v) > MaxLength {
		return "", fmt.Errorf("%w: the %s is longer than %d characters", k.ErrNone, k.Noun, MaxLength)
	}
	for _, r := range v {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", fmt.Errorf("%w: the %s contains spaces or line breaks", k.ErrNone, k.Noun)
		}
	}
	return v, nil
}

// ReadFile reads a file holding the value on a line of its own. Blank lines
// and lines starting with # are ignored, so the file can carry a note. Its
// errors name the file only if it is the data folder's FileName: a path
// typed into a flag or variable might be the value itself, pasted in the
// wrong place.
func (k Kind) ReadFile(path string) (string, error) {
	name := "the " + k.Noun + " file"
	if filepath.Base(path) == k.FileName {
		name = k.FileName
	}
	return k.readFile(path, name)
}

// readFile is ReadFile, with name standing for the file in its errors.
func (k Kind) readFile(path, name string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: %s does not exist", k.ErrNone, name)
		}
		return "", fmt.Errorf("%w: %s cannot be read", k.ErrNone, name)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return "", fmt.Errorf("%w: %s cannot be read", k.ErrNone, name)
	}
	if len(data) > MaxFileSize {
		return "", fmt.Errorf("%w: %s is larger than 16 KB", k.ErrNone, name)
	}
	value := ""
	for _, line := range strings.Split(strings.TrimPrefix(string(data), "\uFEFF"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if value != "" {
			return "", fmt.Errorf("%w: %s has more than one %s line", k.ErrNone, name, k.Noun)
		}
		value = line
	}
	if value == "" {
		return "", fmt.Errorf("%w: %s has no %s in it", k.ErrNone, name, k.Noun)
	}
	return k.Clean(value)
}

// WriteFile stores value at path, readable only by the current user. It
// writes a temporary file first so a crash cannot leave half a value behind.
func (k Kind) WriteFile(path, value string) error {
	value, err := k.Clean(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+strings.ReplaceAll(k.FileName, ".", "-")+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(k.FileHeader + value + "\n"); err != nil {
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

// Source says where the value in use came from.
type Source string

const (
	SourceNone    Source = ""
	SourceFlag    Source = "command_line" // the Flag
	SourceEnv     Source = "environment"  // EnvValue
	SourceEnvFile Source = "env_file"     // EnvFile
	SourceFile    Source = "key_file"     // FileName in the data folder; the web page saves here
	SourceBuiltIn Source = "built_in"     // shipped with this version
)

// Options locate the sources.
type Options struct {
	FlagFile    string              // the file named on the command line
	DefaultFile string              // <data folder>/<Kind.FileName>
	BuiltIn     string              // used when nothing else is set
	Getenv      func(string) string // os.Getenv when nil
}

// Store holds the value in use. Load takes the first source that is set, in
// this order: the command-line file, the environment variable, the file named
// in the environment, the file in the data folder, the built-in value. A
// source that is set but broken is reported, not skipped. The web page can
// change the value (Save) or go back to the built-in one (Reset) unless it is
// set on the command line or in the environment, which are read once at
// start.
type Store struct {
	kind        Kind
	mu          sync.RWMutex
	value       string
	source      Source
	problem     error
	defaultFile string
	builtIn     string
}

// Load resolves the value of kind k from the sources in o.
func Load(k Kind, o Options) *Store {
	getenv := o.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	s := &Store{kind: k, defaultFile: o.DefaultFile}
	if v, err := k.Clean(o.BuiltIn); err == nil {
		s.builtIn = v
	}
	defer s.useBuiltInIfUnset()
	switch {
	case strings.TrimSpace(o.FlagFile) != "":
		s.source = SourceFlag
		s.value, s.problem = k.readFile(strings.TrimSpace(o.FlagFile), "the file named by "+k.Flag)
	case strings.TrimSpace(getenv(k.EnvValue)) != "":
		s.source = SourceEnv
		s.value, s.problem = k.Clean(getenv(k.EnvValue))
	case strings.TrimSpace(getenv(k.EnvFile)) != "":
		s.source = SourceEnvFile
		s.value, s.problem = k.readFile(strings.TrimSpace(getenv(k.EnvFile)), "the file named in "+k.EnvFile)
	case o.DefaultFile != "":
		if _, err := os.Stat(o.DefaultFile); err == nil {
			s.source = SourceFile
			s.value, s.problem = k.readFile(o.DefaultFile, k.FileName)
		}
	}
	return s
}

// useBuiltInIfUnset falls back to the built-in value when no source is set.
// Caller holds s.mu or has not shared s yet.
func (s *Store) useBuiltInIfUnset() {
	if s.source == SourceNone && s.builtIn != "" {
		s.value, s.source, s.problem = s.builtIn, SourceBuiltIn, nil
	}
}

// Kind is what the store holds.
func (s *Store) Kind() Kind { return s.kind }

// Key returns the value in use. (The name matches serverkey.Provider, so the
// same store serves the server key and the app ID.)
func (s *Store) Key() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.value != "" {
		return s.value, nil
	}
	if s.problem != nil {
		return "", s.problem
	}
	return "", s.kind.ErrNone
}

// Status describes the value without including it.
type Status struct {
	Configured bool   `json:"configured"`
	Source     Source `json:"source"`
	Editable   bool   `json:"editable"`
	HasBuiltIn bool   `json:"has_built_in"`
	Problem    string `json:"problem,omitempty"`
}

// Status reports whether a value is in use, where it came from, and whether
// the web page may change it.
func (s *Store) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := Status{
		Configured: s.value != "",
		Source:     s.source,
		Editable:   s.whyNotEditableLocked() == nil,
		HasBuiltIn: s.builtIn != "",
	}
	if s.problem != nil {
		st.Problem = s.problem.Error()
	}
	return st
}

// whyNotEditableLocked explains why the web page may not change the value.
func (s *Store) whyNotEditableLocked() error {
	switch {
	case s.source == SourceFlag || s.source == SourceEnv || s.source == SourceEnvFile:
		return s.kind.ErrNotEditable
	case s.defaultFile == "":
		return fmt.Errorf("no %s file location is configured", s.kind.Noun)
	}
	return nil
}

// Save checks value and uses it for every cloud request from now on. A value
// other than the built-in one is written to the data folder's file; saving
// the built-in value removes that file, the same as Reset.
func (s *Store) Save(value string) error {
	value, err := s.kind.Clean(value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.whyNotEditableLocked(); err != nil {
		return err
	}
	if value == s.builtIn {
		return s.resetLocked()
	}
	if err := s.kind.WriteFile(s.defaultFile, value); err != nil {
		return fmt.Errorf("saving the %s: %w", s.kind.Name, err)
	}
	s.value, s.source, s.problem = value, SourceFile, nil
	return nil
}

// Reset removes a value saved from the web page and goes back to the
// built-in one (or to none, if this build has none).
func (s *Store) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.whyNotEditableLocked(); err != nil {
		return err
	}
	return s.resetLocked()
}

func (s *Store) resetLocked() error {
	if err := os.Remove(s.defaultFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing the saved %s: %w", s.kind.Name, err)
	}
	s.value, s.source, s.problem = "", SourceNone, nil
	s.useBuiltInIfUnset()
	return nil
}
