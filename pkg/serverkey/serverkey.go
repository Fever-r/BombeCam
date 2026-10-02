// Package serverkey supplies the Osaio server key that BombeCam signs its
// cloud requests with. BombeCam ships the latest known key (LatestKnown);
// users can replace it on the command line, in the environment, in a key
// file, or on the web page, where a change applies at once. The app ID works
// the same way (pkg/osaiovalue holds what both share). See
// docs/technical/CONFIGURATION.md.
package serverkey

import (
	"errors"

	"github.com/Fever-r/BombeCam/pkg/osaiovalue"
)

// Provider returns the key to sign the next cloud request with.
type Provider interface {
	Key() (string, error)
}

// ErrNoKey means there is no usable server key. Errors from this package that
// explain why (a missing file, a malformed key) wrap it.
var ErrNoKey = errors.New("no usable Osaio server key")

// ErrNotEditable is returned by Store.Save when the key is set on the command
// line or in the environment, which would override a saved key anyway.
var ErrNotEditable = errors.New("the server key is set on the command line or in the environment; change it there")

// Environment variables and the default key file name.
const (
	EnvKey     = "BOMBECAM_SERVER_KEY"
	EnvKeyFile = "BOMBECAM_SERVER_KEY_FILE"
	FileName   = "server.key"
)

// MaxLength bounds a key. Real keys are far shorter.
const MaxLength = osaiovalue.MaxLength

const maxFileSize = osaiovalue.MaxFileSize

// Kind describes the server key to pkg/osaiovalue.
var Kind = osaiovalue.Kind{
	Name:     "server key",
	Noun:     "key",
	Flag:     "-server-key-file",
	EnvValue: EnvKey,
	EnvFile:  EnvKeyFile,
	FileName: FileName,
	FileHeader: "# BombeCam server key. Keep this file private.\n" +
		"# If Osaio changes its key, replace the line below (or use Settings > Server key on the web page).\n",
	ErrNone:        ErrNoKey,
	ErrNotEditable: ErrNotEditable,
}

// Static is a fixed key. Tests use it with a made-up value.
type Static string

// Key returns the fixed key, or ErrNoKey if it is empty.
func (s Static) Key() (string, error) {
	if s == "" {
		return "", ErrNoKey
	}
	return string(s), nil
}

// Clean trims surrounding whitespace and rejects values that cannot be a key.
// The key is otherwise treated as opaque: no length or alphabet is assumed.
func Clean(raw string) (string, error) { return Kind.Clean(raw) }

// ReadFile reads a key file: the key on a line of its own. Blank lines and
// lines starting with # are ignored, so the file can carry a note.
func ReadFile(path string) (string, error) { return Kind.ReadFile(path) }

// WriteFile stores key at path, readable only by the current user.
func WriteFile(path, key string) error { return Kind.WriteFile(path, key) }

// Source says where the key in use came from.
type Source = osaiovalue.Source

const (
	SourceNone    = osaiovalue.SourceNone
	SourceFlag    = osaiovalue.SourceFlag    // -server-key-file
	SourceEnv     = osaiovalue.SourceEnv     // BOMBECAM_SERVER_KEY
	SourceEnvFile = osaiovalue.SourceEnvFile // BOMBECAM_SERVER_KEY_FILE
	SourceFile    = osaiovalue.SourceFile    // the default key file; the web page saves here
	SourceBuiltIn = osaiovalue.SourceBuiltIn // LatestKnown, shipped with this version
)

// Options locate the key sources: FlagFile is -server-key-file, DefaultFile
// <data folder>/server.key, BuiltIn LatestKnown.
type Options = osaiovalue.Options

// Store is the gateway's Provider. Load takes the first source that is set,
// in this order: -server-key-file, BOMBECAM_SERVER_KEY,
// BOMBECAM_SERVER_KEY_FILE, the default key file, the built-in key. A source
// that is set but broken is reported, not skipped. The web page can change the
// key (Save) or go back to the built-in one (Reset) unless it is set on the
// command line or in the environment, which are read once at start.
type Store = osaiovalue.Store

// Status describes the key without including it.
type Status = osaiovalue.Status

// Load resolves the key from the sources in o.
func Load(o Options) *Store { return osaiovalue.Load(Kind, o) }
