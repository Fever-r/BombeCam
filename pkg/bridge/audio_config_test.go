package bridge

import "testing"

func TestAudioOverrideUsesExactConfiguredIdentity(t *testing.T) {
	t.Setenv("BOMBECAM_VIDEO_ONLY_DEVICES", "")
	if !downlinkAudioEnabled("camera-a") {
		t.Fatal("empty configuration must not disable a particular camera")
	}
	t.Setenv("BOMBECAM_VIDEO_ONLY_DEVICES", " CAMERA-A, camera-b ")
	if downlinkAudioEnabled("camera-a") || downlinkAudioEnabled("camera-b") {
		t.Fatal("explicit video-only devices must disable downlink audio")
	}
	if !downlinkAudioEnabled("camera") || !downlinkAudioEnabled("camera-c") {
		t.Fatal("a partial or unrelated identity must not match")
	}
}
