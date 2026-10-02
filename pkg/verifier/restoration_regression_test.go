package verifier

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRestorationUsesIndependentContextAndReportsFailure(t *testing.T) {
	for _, failRestore := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled-request", true: "restore-failure"}[failRestore], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			restored := false
			p := &mockProber{
				execProbeFunc: func(context.Context, NetworkProbe, bool) bool { cancel(); return false },
				applyModeFunc: func(cleanup context.Context, block bool) error {
					if !block {
						return nil
					}
					if cleanup.Err() != nil {
						t.Fatal("cancelled request reused for restoration")
					}
					deadline, ok := cleanup.Deadline()
					if !ok || time.Until(deadline) > restorationBudget {
						t.Fatal("restoration deadline absent")
					}
					restored = true
					if failRestore {
						return fmt.Errorf("nft refused restore")
					}
					return nil
				},
			}
			v := NewVerifier(Config{CameraID: "fixture"}, p, NewMetrics())
			verdict, err := v.RunVerification(ctx)
			if !restored {
				t.Fatal("restore not attempted")
			}
			if failRestore {
				if err == nil || verdict.Status != StatusUnknown || verdict.BlockCloudVideo || !strings.Contains(verdict.Details.FailureReason, "nft refused restore") {
					t.Fatalf("false restoration: %+v %v", verdict, err)
				}
				if current := v.GetActiveVerdict(); current.Status != StatusUnknown || current.BlockCloudVideo {
					t.Fatalf("stale protective verdict: %+v", current)
				}
			} else if err != nil || verdict.Status != StatusPreconditionFailed || !verdict.BlockCloudVideo {
				t.Fatalf("successful restoration: %+v %v", verdict, err)
			}
		})
	}
}

type unsafeExecutor struct{ ProbeExecutor }

func TestVerifierRefusesUnboundedControlExecutor(t *testing.T) {
	calls := 0
	base := &mockProber{applyModeFunc: func(context.Context, bool) error { calls++; return nil }}
	v := NewVerifier(Config{CameraID: "fixture"}, unsafeExecutor{base}, NewMetrics())
	verdict, err := v.RunVerification(context.Background())
	if err == nil || calls != 0 || verdict.Status != StatusUnknown || verdict.BlockCloudVideo {
		t.Fatalf("unsafe control leg: %+v %v calls=%d", verdict, err, calls)
	}
}

func TestFailedControlLeaseAlsoAttemptsIndependentRestoration(t *testing.T) {
	var modes []bool
	p := &mockProber{applyModeFunc: func(_ context.Context, b bool) error {
		modes = append(modes, b)
		if !b {
			return fmt.Errorf("lease readback failed")
		}
		return nil
	}}
	v := NewVerifier(Config{CameraID: "fixture"}, p, NewMetrics())
	verdict, err := v.RunVerification(context.Background())
	if err == nil || len(modes) != 2 || !modes[1] || verdict.Status != StatusUnknown || !verdict.BlockCloudVideo {
		t.Fatalf("partial lease recovery: %+v %v %v", verdict, err, modes)
	}
}
