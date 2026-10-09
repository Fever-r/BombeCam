package bridge

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPTZStopLogsEveryFailureAndExhaustion(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "ptz-stop-log")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	stdout := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = stdout }()
	attempts := 0
	retryPTZStop("test-control", "fixture-camera", func() bool { return true }, func() error {
		attempts++
		return errors.New("stop unavailable")
	}, [2]time.Duration{})
	os.Stdout = stdout
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if attempts != 3 || len(lines) != 4 {
		t.Fatalf("attempts=%d log=%q, want 3 failures followed by exhaustion", attempts, data)
	}
	for i, want := range []string{"attempt=1/3", "attempt=2/3", "attempt=3/3", "camera may still be moving"} {
		if !strings.Contains(lines[i], want) || !strings.Contains(lines[i], "camera=fixture-camera") ||
			!strings.Contains(lines[i], "stop unavailable") || !strings.Contains(lines[i], "[test-control]") {
			t.Errorf("log line %d = %q, missing failure context or %q", i+1, lines[i], want)
		}
	}
}

func TestPTZStopRetriesTwiceThenStops(t *testing.T) {
	attempts, moving := 0, true
	retryPTZStop("test-control", "fixture-camera", func() bool { return true }, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("dropped stop")
		}
		moving = false
		return nil
	}, [2]time.Duration{time.Millisecond, time.Millisecond})
	if attempts != 3 || moving {
		t.Fatalf("attempts=%d moving=%v, want 3 and false", attempts, moving)
	}
}

func TestPTZSignalingExplicitStopRetries(t *testing.T) {
	previousDelays := ptzStopRetryDelays
	ptzStopRetryDelays = [2]time.Duration{time.Millisecond, time.Millisecond}
	defer func() { ptzStopRetryDelays = previousDelays }()
	for _, alwaysFail := range []bool{false, true} {
		name := "succeeds-third-attempt"
		if alwaysFail {
			name = "exhausted"
		}
		t.Run(name, func(t *testing.T) {
			ch := NewSignalingControlChannel(&Signaling{})
			defer ch.Close()
			attempts := 0
			stopErr := errors.New("stop unavailable")
			ch.ptzSend = func(direction int) error {
				if direction != 0 {
					t.Fatalf("direction=%d, want stop", direction)
				}
				attempts++
				if alwaysFail || attempts < 3 {
					return stopErr
				}
				return nil
			}
			err := ch.MovePTZ(context.Background(), "fixture-camera", 0, 0)
			if attempts != 3 {
				t.Fatalf("stop sends=%d, want exactly 3", attempts)
			}
			if alwaysFail {
				if !errors.Is(err, stopErr) {
					t.Fatalf("error=%v, want stop failure", err)
				}
			} else if err != nil {
				t.Fatalf("successful stop returned error: %v", err)
			}
		})
	}
}

func TestPTZStopAlwaysFailsThreeAttempts(t *testing.T) {
	done := make(chan int, 1)
	go func() {
		attempts := 0
		retryPTZStop("test-control", "fixture-camera", func() bool { return true }, func() error {
			attempts++
			return errors.New("stop unavailable")
		}, [2]time.Duration{time.Millisecond, time.Millisecond})
		done <- attempts
	}()
	select {
	case attempts := <-done:
		if attempts != 3 {
			t.Fatalf("attempts=%d, want 3", attempts)
		}
	case <-time.After(time.Second):
		t.Fatal("stop retries hung")
	}
}

func TestPTZStopNewerMoveCancelsRetries(t *testing.T) {
	previousDelays := ptzStopRetryDelays
	ptzStopRetryDelays = [2]time.Duration{time.Millisecond, time.Millisecond}
	defer func() { ptzStopRetryDelays = previousDelays }()
	for _, channel := range []string{"signaling", "mqtt"} {
		for _, command := range []struct {
			name      string
			direction int
			duration  int
			stops     int32
		}{
			{"timed", 2, 20, 4}, // one old attempt, three attempts owned by the new move
			{"continuous", 2, 0, 1},
			{"explicit-stop", 0, 0, 2}, // one old attempt and the explicit stop itself
		} {
			if channel == "mqtt" && command.name == "continuous" {
				continue
			}
			t.Run(channel+"/"+command.name, func(t *testing.T) {
				var stops atomic.Int32
				var missingDeadline atomic.Bool
				firstStop, releaseStop := make(chan struct{}), make(chan struct{})
				defer close(releaseStop)
				// Keep the old stop in flight until the second MovePTZ registers ownership.
				send := func(direction int) error {
					if direction != 0 {
						return nil
					}
					n := stops.Add(1)
					if n == 1 {
						close(firstStop)
						<-releaseStop
					}
					if command.name == "explicit-stop" && n == 2 {
						return nil
					}
					return errors.New("dropped stop")
				}
				var ch ControlChannel
				if channel == "signaling" {
					s := NewSignalingControlChannel(&Signaling{})
					s.ptzSend = send
					ch = s
				} else {
					m := NewMQTTControlChannel(nil, time.Second, WithRequireReadback(false))
					m.ptzDispatch = func(ctx context.Context, camera string, desired map[string]any) error {
						if desired["direction"] == 0 {
							if _, ok := ctx.Deadline(); !ok {
								missingDeadline.Store(true)
							}
						}
						return send(desired["direction"].(int))
					}
					ch = m
				}
				defer ch.Close()
				if err := ch.MovePTZ(context.Background(), "fixture-camera", 1, 1); err != nil {
					t.Fatal(err)
				}
				select {
				case <-firstStop:
				case <-time.After(time.Second):
					t.Fatal("old move did not attempt a stop")
				}
				// Give explicit MQTT stops a deadline too; automatic retries supply their own.
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := ch.MovePTZ(ctx, "fixture-camera", command.direction, command.duration); err != nil {
					t.Fatal(err)
				}
				releaseStop <- struct{}{}
				if command.name == "timed" {
					waitFor(t, time.Second, "new move stop attempts", func() bool { return stops.Load() >= command.stops })
				}
				time.Sleep(20 * time.Millisecond) // allow any erroneous stale retries to run
				if n := stops.Load(); n != command.stops {
					t.Fatalf("stop sends=%d, want exactly %d", n, command.stops)
				}
				if missingDeadline.Load() {
					t.Error("stop dispatch missing bounded context")
				}
			})
		}
	}
}

func TestPTZStopClosedChannelCancelsRetries(t *testing.T) {
	previousDelays := ptzStopRetryDelays
	ptzStopRetryDelays = [2]time.Duration{time.Millisecond, time.Millisecond}
	defer func() { ptzStopRetryDelays = previousDelays }()
	ch := NewMQTTControlChannel(nil, time.Second, WithRequireReadback(false))
	defer ch.Close()
	var stops atomic.Int32
	ch.ptzDispatch = func(ctx context.Context, camera string, desired map[string]any) error {
		if desired["direction"] != 0 {
			return nil
		}
		stops.Add(1)
		ch.Close()
		return errors.New("dropped stop")
	}
	if err := ch.MovePTZ(context.Background(), "fixture-camera", 1, 1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, "stop that closes the channel", func() bool { return stops.Load() >= 1 })
	time.Sleep(20 * time.Millisecond) // allow any erroneous retries after Close to run
	if n := stops.Load(); n != 1 {
		t.Fatalf("stop sends=%d, want exactly 1", n)
	}
}
