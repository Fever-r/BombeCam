package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	shadowshim "github.com/Fever-r/BombeCam/pkg/bridge/shadow_shim"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"strings"
	"sync"
	"time"
)

type MosquittoBridgeConfig struct {
	BrokerURL      string
	ClientID       string
	ConnectTimeout time.Duration
}

func DefaultMosquittoBridgeConfig() MosquittoBridgeConfig {
	return MosquittoBridgeConfig{BrokerURL: "tcp://127.0.0.1:1883", ClientID: "bombecam-gateway-shadow", ConnectTimeout: 5 * time.Second}
}

type cameraMQTTReport struct {
	At    time.Time
	Count uint64
}
type MosquittoBridge struct {
	mu                         sync.RWMutex
	cfg                        MosquittoBridgeConfig
	daemon                     *shadowshim.ShadowShimDaemon
	client                     mqtt.Client
	running, connected, closed bool
	subCancel                  func()
	stopCh                     chan struct{}
	reports                    map[string]cameraMQTTReport
	lastError                  string
}

func NewMosquittoBridge(cfg MosquittoBridgeConfig, daemon *shadowshim.ShadowShimDaemon) *MosquittoBridge {
	if cfg.BrokerURL == "" {
		cfg.BrokerURL = "tcp://127.0.0.1:1883"
	}
	if cfg.ClientID == "" {
		cfg.ClientID = fmt.Sprintf("bombecam-shadow-%d", time.Now().UnixNano())
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}
	return &MosquittoBridge{cfg: cfg, daemon: daemon, stopCh: make(chan struct{}), reports: make(map[string]cameraMQTTReport)}
}
func (b *MosquittoBridge) setError(err error) {
	b.mu.Lock()
	b.lastError = err.Error()
	b.mu.Unlock()
	fmt.Printf("[local-control] %v\n", err)
}
func (b *MosquittoBridge) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return fmt.Errorf("MQTT adapter is closed")
	}
	if b.running {
		b.mu.Unlock()
		return nil
	}
	opts := mqtt.NewClientOptions().AddBroker(b.cfg.BrokerURL).SetClientID(b.cfg.ClientID)
	opts.SetConnectTimeout(b.cfg.ConnectTimeout).SetAutoReconnect(true).SetConnectRetry(true)
	opts.SetConnectRetryInterval(time.Second).SetKeepAlive(15 * time.Second)
	// Never hold b.mu while waiting on MQTT: OnConnect also needs that lock.
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		token := c.SubscribeMultiple(map[string]byte{"$aws/things/+/shadow/update": 1, "$aws/things/+/shadow/get": 1}, func(_ mqtt.Client, msg mqtt.Message) {
			// Retained snapshots predate this connection and cannot confirm a command.
			if msg.Retained() {
				return
			}
			b.handleIncomingFromCamera(msg.Topic(), msg.Payload())
		})
		if !token.WaitTimeout(b.cfg.ConnectTimeout) {
			b.setError(fmt.Errorf("MQTT subscriptions timed out"))
			return
		}
		if err := token.Error(); err != nil {
			b.setError(err)
			return
		}
		b.mu.Lock()
		if !b.closed {
			b.connected = true
			b.lastError = ""
		}
		b.mu.Unlock()
		fmt.Println("[local-control] broker connected; waiting for camera reports")
	})
	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		b.mu.Lock()
		b.connected = false
		b.reports = make(map[string]cameraMQTTReport)
		b.mu.Unlock()
		b.setError(fmt.Errorf("broker disconnected: %w", err))
	})
	client := mqtt.NewClient(opts)
	b.client = client
	b.running = true
	out, unsub := b.daemon.Subscribe("$aws/things/+/shadow/#")
	b.subCancel = unsub
	b.mu.Unlock()
	go b.forwardOutgoingToMosquitto(out)
	token := client.Connect()
	if !token.WaitTimeout(b.cfg.ConnectTimeout) {
		b.setError(fmt.Errorf("broker unavailable; retrying connection"))
	} else if err := token.Error(); err != nil {
		b.setError(err)
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = b.Close()
		case <-b.stopCh:
		}
	}()
	return nil
}
func (b *MosquittoBridge) handleIncomingFromCamera(topic string, payload []byte) {
	parts := strings.Split(topic, "/")
	if len(parts) != 5 || parts[0] != "$aws" || parts[1] != "things" || parts[3] != "shadow" {
		return
	}
	if parts[4] == "update" {
		var req shadowshim.ShadowUpdateRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			b.setError(fmt.Errorf("invalid camera shadow payload: %w", err))
			return
		}
		// Count incoming reported fields only; never promote desired fields to telemetry.
		if len(req.State.Reported) == 0 {
			return
		}
		req.State.Desired = nil
		payload, _ = json.Marshal(req)
		if err := b.daemon.Publish(topic, payload); err != nil {
			b.setError(err)
			return
		}
		b.mu.Lock()
		r := b.reports[parts[2]]
		r.At = time.Now()
		r.Count++
		b.reports[parts[2]] = r
		b.mu.Unlock()
		fmt.Printf("[local-control] report received thing=%s fields=%d count=%d\n", parts[2], len(req.State.Reported), r.Count)
	} else if parts[4] == "get" {
		_ = b.daemon.Publish(topic, payload)
	}
}
func (b *MosquittoBridge) forwardOutgoingToMosquitto(ch <-chan shadowshim.TopicMessage) {
	for {
		select {
		case <-b.stopCh:
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if !strings.HasSuffix(msg.Topic, "/accepted") && !strings.HasSuffix(msg.Topic, "/delta") && !strings.HasSuffix(msg.Topic, "/rejected") {
				continue
			}
			b.mu.RLock()
			c, ready := b.client, b.connected && !b.closed
			b.mu.RUnlock()
			if c == nil || !ready || !c.IsConnected() {
				continue
			}
			token := c.Publish(msg.Topic, 1, false, msg.Payload)
			if !token.WaitTimeout(time.Second) {
				b.setError(fmt.Errorf("MQTT publish timed out"))
			} else if err := token.Error(); err != nil {
				b.setError(err)
			}
		}
	}
}
func (b *MosquittoBridge) IsConnected() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.connected && !b.closed && b.client != nil && b.client.IsConnected()
}
func (b *MosquittoBridge) Status(uuid string) LocalControlStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()
	s := LocalControlStatus{Route: "local_mqtt", State: "broker_unavailable", LastError: b.lastError}
	s.BrokerConnected = b.connected && !b.closed && b.client != nil && b.client.IsConnected()
	r := b.reports[uuid]
	s.LastReport = r.At
	s.ReportCount = r.Count
	if s.BrokerConnected {
		s.State = "waiting_for_camera"
		if !r.At.IsZero() {
			s.State = "camera_report_stale"
			if time.Since(r.At) <= LocalReportMaxAge {
				s.State = "camera_report_received"
			}
		}
	}
	return s
}
func (b *MosquittoBridge) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.running = false
	b.connected = false
	close(b.stopCh)
	c, unsub := b.client, b.subCancel
	b.mu.Unlock()
	if unsub != nil {
		unsub()
	}
	if c != nil {
		c.Disconnect(250)
	}
	return nil
}
