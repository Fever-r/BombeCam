package bridge

import (
	"slices"
	"testing"
)

func TestBuildHEVCTranscoderArgsKeepsExistingOutputCodecs(t *testing.T) {
	const rtspURL = "rtsp://127.0.0.1:8554/camera"
	const audioURL = "tcp://127.0.0.1:45678"

	withAudio := buildHEVCTranscoderArgs(true, audioURL, rtspURL)
	for _, want := range []string{"-f", "hevc", "-i", "pipe:0", "-f", "aac", "-i", audioURL, "libx264", "-rtsp_transport", "tcp", rtspURL} {
		if !slices.Contains(withAudio, want) {
			t.Errorf("audio HEVC args missing %q: %v", want, withAudio)
		}
	}
	if slices.Contains(withAudio, "-an") {
		t.Fatalf("audio HEVC args unexpectedly disable audio: %v", withAudio)
	}

	videoOnly := buildHEVCTranscoderArgs(false, "", rtspURL)
	if !slices.Contains(videoOnly, "-an") || slices.Contains(videoOnly, "tcp://") {
		t.Fatalf("video-only HEVC args have wrong audio settings: %v", videoOnly)
	}
}
