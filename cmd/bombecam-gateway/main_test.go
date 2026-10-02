package main

import (
	"testing"

	"github.com/Fever-r/BombeCam/pkg/bridge"
)

func TestApplyTimezoneConfig_ExplicitUTC(t *testing.T) {
	c := bridge.NewCloud("US", "1234567890", testServerKey)
	// Set baseline non-zero offset
	c.ZoneOffset = -5.0
	c.TimezoneName = "America/New_York"

	// 1. Explicit UTC offset "0"
	applyTimezoneConfig(c, "UTC", "0")
	if c.TimezoneName != "UTC" || c.ZoneOffset != 0.0 {
		t.Fatalf("expected UTC and 0.0 offset, got %s / %f", c.TimezoneName, c.ZoneOffset)
	}

	// 2. Explicit UTC offset "0.0"
	c.ZoneOffset = 2.0
	applyTimezoneConfig(c, "UTC", "0.0")
	if c.ZoneOffset != 0.0 {
		t.Fatalf("expected 0.0 offset, got %f", c.ZoneOffset)
	}

	// 3. Unconfigured (empty strings) must preserve existing values
	c.ZoneOffset = -7.0
	c.TimezoneName = "America/Denver"
	applyTimezoneConfig(c, "", "")
	if c.ZoneOffset != -7.0 || c.TimezoneName != "America/Denver" {
		t.Fatalf("expected unchanged -7.0 offset and America/Denver, got %f / %s", c.ZoneOffset, c.TimezoneName)
	}

	// 4. Fractional offset (India UTC+5:30)
	applyTimezoneConfig(c, "Asia/Kolkata", "5.5")
	if c.ZoneOffset != 5.5 || c.TimezoneName != "Asia/Kolkata" {
		t.Fatalf("expected 5.5 offset and Asia/Kolkata, got %f / %s", c.ZoneOffset, c.TimezoneName)
	}

	// 5. Negative offset (Pacific UTC-8)
	applyTimezoneConfig(c, "America/Los_Angeles", "-8")
	if c.ZoneOffset != -8.0 || c.TimezoneName != "America/Los_Angeles" {
		t.Fatalf("expected -8.0 offset and America/Los_Angeles, got %f / %s", c.ZoneOffset, c.TimezoneName)
	}

	// 6. Out-of-range offset fallback (valid terrestrial range is [-12, +14])
	c.ZoneOffset = 1.0
	applyTimezoneConfig(c, "", "45.0")
	if c.ZoneOffset != 1.0 {
		t.Fatalf("expected out-of-range 45.0 to be rejected, preserving 1.0, got %f", c.ZoneOffset)
	}

	applyTimezoneConfig(c, "", "-15.0")
	if c.ZoneOffset != 1.0 {
		t.Fatalf("expected out-of-range -15.0 to be rejected, preserving 1.0, got %f", c.ZoneOffset)
	}

	// 7. Malformed string fallback
	c.ZoneOffset = 1.0
	applyTimezoneConfig(c, "", "invalid-offset")
	if c.ZoneOffset != 1.0 {
		t.Fatalf("expected malformed string to be rejected, preserving 1.0, got %f", c.ZoneOffset)
	}

	// 8. Nil cloud safe
	applyTimezoneConfig(nil, "UTC", "0")
}
