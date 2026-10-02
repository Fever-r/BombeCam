package shadowshim

import (
	"context"
	"testing"
	"time"
)

func TestStateMachineUpdateAndGet(t *testing.T) {
	sm := NewStateMachine()

	req := &ShadowUpdateRequest{
		State: ShadowState{
			Desired: map[string]any{
				"streaming": true,
			},
		},
		ClientToken: "test_token_1",
	}

	doc, delta, err := sm.ApplyUpdate("cam1", req)
	if err != nil {
		t.Fatalf("unexpected error applying update: %v", err)
	}

	if doc == nil || doc.Version != 1 {
		t.Fatalf("expected version 1, got %v", doc)
	}
	if delta == nil || delta.State["streaming"] != true {
		t.Fatalf("expected delta streaming=true, got %v", delta)
	}

	// Confirm shadow retrieval
	getDoc, err := sm.GetShadow("cam1")
	if err != nil {
		t.Fatalf("unexpected error getting shadow: %v", err)
	}
	if getDoc.State.Desired["streaming"] != true {
		t.Fatalf("expected desired streaming=true in shadow, got %v", getDoc.State.Desired)
	}
}

func TestStateMachineReadbackConfirmation(t *testing.T) {
	sm := NewStateMachine()

	// Apply desired state
	_, _, err := sm.ApplyUpdate("cam2", &ShadowUpdateRequest{
		State: ShadowState{
			Desired: map[string]any{
				"night_mode": 1,
			},
		},
	})
	if err != nil {
		t.Fatalf("apply update failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	dispatchTime := time.Now()
	type resWrapper struct {
		res *ConfirmationResult
		err error
	}
	waitCh := make(chan resWrapper, 1)
	go func() {
		res, rerr := sm.ConfirmReadback(ctx, "cam2", map[string]any{"night_mode": 1}, dispatchTime, 2*time.Second)
		waitCh <- resWrapper{res: res, err: rerr}
	}()

	time.Sleep(50 * time.Millisecond)

	// Device reports matching state
	_, _, err = sm.ApplyUpdate("cam2", &ShadowUpdateRequest{
		State: ShadowState{
			Reported: map[string]any{
				"night_mode": 1,
			},
		},
	})
	if err != nil {
		t.Fatalf("apply reported state failed: %v", err)
	}

	select {
	case rw := <-waitCh:
		if rw.err != nil {
			t.Fatalf("expected confirmation success, got error: %v", rw.err)
		}
		if rw.res == nil || rw.res.ConfirmedState["night_mode"] != 1 {
			t.Fatalf("unexpected confirmation result: %v", rw.res)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for confirmation")
	}
}
