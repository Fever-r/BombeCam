package prober

import (
	"context"
	"testing"
	"time"
)

func TestProber_RequiredValidation(t *testing.T) {
	applyDummy := func(ctx context.Context, block bool) error { return nil }

	// Empty config should fail
	_, err := New(Config{}, applyDummy)
	if err == nil {
		t.Fatalf("expected error for empty config, got nil")
	}

	// Missing CameraSubnet
	_, err = New(Config{
		CameraInterface: "eth0",
		CanaryIP:        "10.0.0.2",
	}, applyDummy)
	if err == nil {
		t.Fatalf("expected error for missing CameraSubnet, got nil")
	}

	// Missing CameraInterface
	_, err = New(Config{
		CameraSubnet: "10.0.0.0/24",
		CanaryIP:     "10.0.0.2",
	}, applyDummy)
	if err == nil {
		t.Fatalf("expected error for missing CameraInterface, got nil")
	}

	// Missing CanaryIP
	_, err = New(Config{
		CameraInterface: "eth0",
		CameraSubnet:    "10.0.0.0/24",
	}, applyDummy)
	if err == nil {
		t.Fatalf("expected error for missing CanaryIP, got nil")
	}

	// Valid config
	p, err := New(Config{
		CameraInterface: "eth0",
		CameraSubnet:    "10.0.0.0/24",
		CanaryIP:        "10.0.0.2",
	}, applyDummy)
	if err != nil {
		t.Fatalf("unexpected error for valid config: %v", err)
	}
	if p.cfg.CameraSubnet != "10.0.0.0/24" {
		t.Errorf("expected 10.0.0.0/24, got %s", p.cfg.CameraSubnet)
	}
}

func TestProberRefusesUnboundedOrMissingControlLease(t *testing.T) {
	calls := 0
	p, err := New(Config{CameraInterface: "fixture", CameraSubnet: "192.0.2.0/24", CanaryIP: "192.0.2.2"}, func(context.Context, bool) error { calls++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ApplyMode(context.Background(), false); err == nil {
		t.Fatal("unbounded off accepted")
	}
	if err := p.BeginControlLeg(context.Background(), 30*time.Second); err == nil {
		t.Fatal("missing lease accepted")
	}
	if calls != 0 {
		t.Fatal("unsafe request changed firewall")
	}
}
