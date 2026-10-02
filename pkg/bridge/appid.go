package bridge

import (
	"errors"
	"sync/atomic"

	"github.com/Fever-r/BombeCam/internal/obscure"
	"github.com/Fever-r/BombeCam/pkg/osaiovalue"
)

// The Osaio app ID works exactly like the server key (pkg/serverkey): it is
// read, in order, from -app-id-file, BOMBECAM_APP_ID, BOMBECAM_APP_ID_FILE,
// app.id in the data folder (which Settings > App ID writes), and finally the
// app ID built into this binary. A change on the web page applies at once.
const (
	EnvAppID      = "BOMBECAM_APP_ID"
	EnvAppIDFile  = "BOMBECAM_APP_ID_FILE"
	AppIDFileName = "app.id"
)

// builtInAppID is the Osaio app ID built into this binary, obscured. It is not
// in the source code: builds set it from osaio-setup.txt or BOMBECAM_APP_ID
// (internal/buildkey), release builds from a GitHub Actions secret. A build
// without one leaves it empty.
var builtInAppID string

// LatestKnownAppID is the built-in app ID in plain form: the last source
// BombeCam tries, so a release download works without setup. It is empty in a
// build without one, and the web page then asks for it.
var LatestKnownAppID = obscure.Reveal("app_id", builtInAppID)

// ErrNoAppID means a request was not sent because there is no usable Osaio
// app ID. Errors that explain why (a missing file, a malformed value) wrap it.
var ErrNoAppID = errors.New("no usable Osaio app ID")

// ErrAppIDNotEditable is returned when the web page tries to change an app
// ID that is set on the command line or in the environment.
var ErrAppIDNotEditable = errors.New("the app ID is set on the command line or in the environment; change it there")

// AppIDKind describes the app ID to pkg/osaiovalue.
var AppIDKind = osaiovalue.Kind{
	Name:     "app ID",
	Noun:     "app ID",
	Flag:     "-app-id-file",
	EnvValue: EnvAppID,
	EnvFile:  EnvAppIDFile,
	FileName: AppIDFileName,
	FileHeader: "# BombeCam: the Osaio app ID. Keep this file private.\n" +
		"# If Osaio changes its app ID, replace the line below (or use Settings > App ID on the web page).\n",
	ErrNone:        ErrNoAppID,
	ErrNotEditable: ErrAppIDNotEditable,
}

var appIDs atomic.Pointer[osaiovalue.Store]

// UseAppIDs makes every request take its app ID from s. The gateway installs
// its store at start, so a change on the web page applies to the next request.
func UseAppIDs(s *osaiovalue.Store) { appIDs.Store(s) }

// AppID is the Osaio app ID requests carry and are signed with. In the
// gateway it comes from the store installed with UseAppIDs. Without one
// (tests, tools) it comes from BOMBECAM_APP_ID, BOMBECAM_APP_ID_FILE or the
// built-in app ID, read on every call. Errors wrap ErrNoAppID.
func AppID() (string, error) {
	if s := appIDs.Load(); s != nil {
		return s.Key()
	}
	return osaiovalue.Load(AppIDKind, osaiovalue.Options{BuiltIn: LatestKnownAppID}).Key()
}
