package shadowshim

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	reShadowUpdate = regexp.MustCompile(`^\$aws/things/([^/]+)/shadow/update$`)
	reShadowGet    = regexp.MustCompile(`^\$aws/things/([^/]+)/shadow/get$`)
)

// Config holds configuration options for the shadow shim daemon.
type Config struct {
	DefaultTimeout time.Duration
}

type subscription struct {
	pattern string
	ch      chan TopicMessage
}

// ShadowShimDaemon operates the shadow message router, state machine, and topic event bus.
type ShadowShimDaemon struct {
	mu           sync.RWMutex
	cfg          Config
	sm           *StateMachine
	subs         []*subscription
	ctx          context.Context
	cancel       context.CancelFunc
	running      bool
	msgHistory   []TopicMessage
	historyLimit int
}

// NewDaemon creates a new ShadowShimDaemon instance.
func NewDaemon(cfg Config) *ShadowShimDaemon {
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 2000 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &ShadowShimDaemon{
		cfg:          cfg,
		sm:           NewStateMachine(),
		subs:         make([]*subscription, 0),
		ctx:          ctx,
		cancel:       cancel,
		historyLimit: 100,
	}
}

// Start begins daemon background operations.
func (d *ShadowShimDaemon) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running = true
	return nil
}

// Close shuts down daemon and closes subscription channels.
func (d *ShadowShimDaemon) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.running {
		return nil
	}
	d.running = false
	d.cancel()

	for _, sub := range d.subs {
		close(sub.ch)
	}
	d.subs = nil
	return nil
}

// StateMachine returns the underlying StateMachine.
func (d *ShadowShimDaemon) StateMachine() *StateMachine {
	return d.sm
}

// Subscribe registers a listener channel matching topic patterns (+ and # supported).
func (d *ShadowShimDaemon) Subscribe(pattern string) (<-chan TopicMessage, func()) {
	d.mu.Lock()
	defer d.mu.Unlock()

	ch := make(chan TopicMessage, 50)
	sub := &subscription{pattern: pattern, ch: ch}
	d.subs = append(d.subs, sub)

	unsubscribe := func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		var active []*subscription
		for _, s := range d.subs {
			if s != sub {
				active = append(active, s)
			}
		}
		d.subs = active
	}

	return ch, unsubscribe
}

// Publish routes a message to matching subscribers and applies shadow updates.
func (d *ShadowShimDaemon) Publish(topic string, payload []byte) error {
	now := time.Now()
	msg := TopicMessage{
		Topic:     topic,
		Payload:   payload,
		Timestamp: now,
	}

	// 1. Deliver to subscribers
	d.mu.RLock()
	for _, sub := range d.subs {
		if topicMatches(sub.pattern, topic) {
			select {
			case sub.ch <- msg:
			default:
			}
		}
	}
	d.mu.RUnlock()

	// 2. Track history
	d.mu.Lock()
	d.msgHistory = append(d.msgHistory, msg)
	if len(d.msgHistory) > d.historyLimit {
		d.msgHistory = d.msgHistory[len(d.msgHistory)-d.historyLimit:]
	}
	d.mu.Unlock()

	// 3. Process Shadow Logic
	if m := reShadowUpdate.FindStringSubmatch(topic); len(m) == 2 {
		thingName := m[1]
		var req ShadowUpdateRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			// Publish to rejected topic
			rejTopic := fmt.Sprintf(TopicShadowUpdateRejected, thingName)
			rejPayload, _ := json.Marshal(map[string]any{
				"code":    400,
				"message": fmt.Sprintf("invalid payload: %v", err),
			})
			_ = d.Publish(rejTopic, rejPayload)
			return err
		}

		doc, delta, err := d.sm.ApplyUpdate(thingName, &req)
		if err != nil {
			rejTopic := fmt.Sprintf(TopicShadowUpdateRejected, thingName)
			rejPayload, _ := json.Marshal(map[string]any{
				"code":    500,
				"message": err.Error(),
			})
			_ = d.Publish(rejTopic, rejPayload)
			return err
		}

		// Publish accepted
		acceptedTopic := fmt.Sprintf(TopicShadowUpdateAccepted, thingName)
		acceptedPayload, _ := json.Marshal(doc)
		_ = d.Publish(acceptedTopic, acceptedPayload)

		// Publish delta if present
		if delta != nil {
			deltaTopic := fmt.Sprintf(TopicShadowUpdateDelta, thingName)
			deltaPayload, _ := json.Marshal(delta)
			_ = d.Publish(deltaTopic, deltaPayload)
		}
	} else if m := reShadowGet.FindStringSubmatch(topic); len(m) == 2 {
		thingName := m[1]
		doc, err := d.sm.GetShadow(thingName)
		if err != nil {
			rejTopic := fmt.Sprintf(TopicShadowGetRejected, thingName)
			rejPayload, _ := json.Marshal(map[string]any{
				"code":    404,
				"message": err.Error(),
			})
			_ = d.Publish(rejTopic, rejPayload)
		} else {
			accTopic := fmt.Sprintf(TopicShadowGetAccepted, thingName)
			accPayload, _ := json.Marshal(doc)
			_ = d.Publish(accTopic, accPayload)
		}
	}

	return nil
}

// DispatchDesired dispatches a desired state update to $aws/things/{thingName}/shadow/update.
func (d *ShadowShimDaemon) DispatchDesired(ctx context.Context, thingName string, desired map[string]any) error {
	token := fmt.Sprintf("bc-%d", time.Now().UnixNano())
	req := ShadowUpdateRequest{
		State: ShadowState{
			Desired: desired,
		},
		ClientToken: token,
	}

	b, err := json.Marshal(req)
	if err != nil {
		return err
	}

	topic := fmt.Sprintf(TopicShadowUpdate, thingName)
	return d.Publish(topic, b)
}

// DispatchAndConfirm implements Invariant I4: records monotonic T_dispatch, dispatches desired
// state, and awaits confirmed reported readback with timestamp >= T_dispatch within timeout.
func (d *ShadowShimDaemon) DispatchAndConfirm(ctx context.Context, thingName string, desired map[string]any, expected map[string]any, timeout time.Duration) (*ConfirmationResult, error) {
	if timeout <= 0 {
		timeout = d.cfg.DefaultTimeout
	}

	dispatchTime := time.Now()

	// 1. Synchronously register waiter on caller goroutine BEFORE dispatching
	w, cleanup, err := d.sm.RegisterWaiter(thingName, expected, dispatchTime)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// 2. Dispatch desired update
	if err := d.DispatchDesired(ctx, thingName, desired); err != nil {
		return nil, fmt.Errorf("failed to dispatch desired update: %w", err)
	}

	// 3. Await confirmation result
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case res := <-w.ch:
		return res, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		lastReported := d.sm.getLastReportedState(thingName)
		return nil, &ErrReadbackTimeout{
			ThingName:    thingName,
			DispatchTime: dispatchTime,
			Timeout:      timeout,
			Expected:     expected,
			LastReported: lastReported,
		}
	}
}

// IngestReported simulates or records camera reported state into the shadow.
func (d *ShadowShimDaemon) IngestReported(thingName string, reported map[string]any) error {
	req := ShadowUpdateRequest{
		State: ShadowState{
			Reported: reported,
		},
	}

	b, err := json.Marshal(req)
	if err != nil {
		return err
	}

	topic := fmt.Sprintf(TopicShadowUpdate, thingName)
	return d.Publish(topic, b)
}

// GetLatestShadow retrieves current shadow document for thingName.
func (d *ShadowShimDaemon) GetLatestShadow(thingName string) (*ShadowDocument, error) {
	return d.sm.GetShadow(thingName)
}

// topicMatches checks if an MQTT topic matches an MQTT subscription pattern (+ and # wildcards).
func topicMatches(pattern, topic string) bool {
	if pattern == topic || pattern == "#" {
		return true
	}

	pParts := strings.Split(pattern, "/")
	tParts := strings.Split(topic, "/")

	for i := 0; i < len(pParts); i++ {
		if pParts[i] == "#" {
			return true
		}
		if i >= len(tParts) {
			return false
		}
		if pParts[i] != "+" && pParts[i] != tParts[i] {
			return false
		}
	}

	return len(pParts) == len(tParts)
}
