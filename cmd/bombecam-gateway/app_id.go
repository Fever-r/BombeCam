package main

import (
	"flag"
	"net/http"

	"github.com/Fever-r/BombeCam/pkg/bridge"
	"github.com/Fever-r/BombeCam/pkg/osaiovalue"
)

// The Osaio app ID goes with every cloud request and is part of its
// signature. It works exactly like the server key: it is read, in order, from
// -app-id-file, BOMBECAM_APP_ID, BOMBECAM_APP_ID_FILE, app.id in the data
// folder (which Settings > App ID writes), and finally the app ID built into
// this binary (bridge.LatestKnownAppID, set at build time; empty in a build
// without one). A change on the web page applies at once. See
// docs/technical/CONFIGURATION.md.

var appIDFileFlag = flag.String("app-id-file", "", "file holding the Osaio app ID (overrides BOMBECAM_APP_ID, BOMBECAM_APP_ID_FILE, the saved app ID and the built-in one)")

// appIDValue is the app ID. Requests read it through bridge.AppID, so loading
// it installs the store there.
var appIDValue = &osaioValue{
	kind:    bridge.AppIDKind,
	flag:    appIDFileFlag,
	builtIn: func() string { return bridge.LatestKnownAppID },
	install: bridge.UseAppIDs,
	log:     "app-id",
	field:   "app_id",
	code:    "app_id",
	missing: SessionStatusAppIDMissing,
	message: appIDMissingMessage,
}

// loadAppIDs resolves the app ID sources and installs the result.
func loadAppIDs(dataDir string) *osaiovalue.Store { return appIDValue.load(dataDir) }

func setAppIDs(s *osaiovalue.Store) { appIDValue.set(s) }

// appIDStatus is the app ID's status for the page; it never includes the app ID.
func appIDStatus() osaiovalue.Status { return appIDValue.status() }

// handleAppID serves /api/v1/app-id (see osaioValue.handle): GET the status,
// POST {"app_id": "..."} to use an app ID, DELETE to go back to the built-in
// one.
func handleAppID(w http.ResponseWriter, r *http.Request, sm *SessionManager, stm *StreamManager) {
	appIDValue.handle(w, r, sm, stm)
}
