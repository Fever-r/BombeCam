package main

import "testing"

func TestUnobservedCameraStatusIsUnknown(t *testing.T) {
	cam := &ManagedCamera{UUID: "synthetic-camera", Session: NewCameraSession("synthetic-camera", "Synthetic", false)}
	status := cam.GetStatus(nil)
	if status.ObservedIR != "unknown" || status.ObservedLED != "unknown" || status.Transport != "unknown" {
		t.Fatalf("status invents observations: %+v", status)
	}
	if status.Streaming || status.FreshnessTS != "" {
		t.Fatalf("status invents media or observation time: %+v", status)
	}
}
