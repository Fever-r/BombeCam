package bridge

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestVideoPacketDiagnosticsSummarizePacketMetadata(t *testing.T) {
	var d videoPacketDiagnostics
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d.recordRTPPayload([]byte{49 << 1, 0x88, 0x80 | 50, 1, 2}, start)
	d.recordRTPPayload([]byte{49 << 1, 0x88, 50, 3, 4}, start.Add(time.Second))
	d.recordRTPPayload([]byte{49 << 1, 0x88, 0x40 | 50, 5, 6}, start.Add(2*time.Second))
	d.recordNALType(7)
	d.recordNALType(8)
	d.idrAccessUnits = 2
	d.recordSPSParseError(errors.New("sps: truncated"))

	if !d.windowReady(start.Add(videoDiagWindow)) {
		t.Fatal("expected diagnostic window to be ready")
	}
	got := d.summary()
	for _, want := range []string{
		"packets=3",
		"RTP-NAL={49:3}",
		"FU-start={50:1}",
		"FU-middle={50:1}",
		"FU-end={50:1}",
		"rebuilt-NAL={7:1,8:1}",
		"SPS-parse-errors=1",
		`last-SPS-error="sps: truncated"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q does not contain %q", got, want)
		}
	}

	d.resetWindow(start.Add(videoDiagWindow))
	if d.windowReady(start.Add(videoDiagWindow + time.Second)) {
		t.Fatal("empty diagnostic window should not be ready")
	}
	if d.windows != 1 || d.packets != 0 || d.spsErrors != 0 {
		t.Fatalf("reset did not clear the window: %+v", d)
	}
}
