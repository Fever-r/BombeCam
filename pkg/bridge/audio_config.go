package bridge

import (
	"os"
	"strings"
)

// Keep device-specific workarounds in local configuration, not compiled identities.
func downlinkAudioEnabled(uuid string) bool {
	for _, id := range strings.Split(os.Getenv("BOMBECAM_VIDEO_ONLY_DEVICES"), ",") {
		if value := strings.TrimSpace(id); value != "" && strings.EqualFold(value, uuid) {
			return false
		}
	}
	return true
}
