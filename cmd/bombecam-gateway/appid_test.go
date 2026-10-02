package main

import (
	"os"

	"github.com/Fever-r/BombeCam/pkg/bridge"
)

// Tests here get a made-up Osaio app ID through BOMBECAM_APP_ID; no real one
// is ever used.
func init() {
	os.Setenv(bridge.EnvAppID, "synthetic-app-id") // even if one is set in the environment
}
