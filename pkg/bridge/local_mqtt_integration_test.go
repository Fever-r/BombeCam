//go:build mqttintegration

package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	shadowshim "github.com/Fever-r/BombeCam/pkg/bridge/shadow_shim"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// These are software integration tests against a real broker and a simulated
// device client. They deliberately make no physical-camera or firewall claim.
func testLocalBroker(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("BOMBECAM_TEST_MOSQUITTO")
	if binary == "" {
		binary = "mosquitto"
	}
	binary, err := exec.LookPath(binary)
	if err != nil {
		t.Skipf("needs a Mosquitto broker on PATH or in BOMBECAM_TEST_MOSQUITTO: %v", err)
	}
	binary, _ = filepath.Abs(binary)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().(*net.TCPAddr)
	port := addr.Port
	_ = l.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mosquitto.conf")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("listener %d 127.0.0.1\nallow_anonymous true\npersistence false\nlog_dest stdout\n", port)), 0600); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(dir, "broker.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-c", cfg)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = logFile.Close() })
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return fmt.Sprintf("tcp://127.0.0.1:%d", port)
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, _ := os.ReadFile(logFile.Name())
	t.Fatalf("broker failed to start: %s", data)
	return ""
}

func mqttWait(t *testing.T, token mqtt.Token) {
	t.Helper()
	if !token.WaitTimeout(2 * time.Second) {
		t.Fatal("MQTT operation timeout")
	}
	if err := token.Error(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalMQTTRealBrokerRoundTrip(t *testing.T) {
	url := testLocalBroker(t)
	ch := NewMQTTControlChannel(nil, 200*time.Millisecond)
	defer ch.Close()
	b := NewMosquittoBridge(MosquittoBridgeConfig{BrokerURL: url, ConnectTimeout: time.Second}, ch.Daemon())
	ch.AttachBroker(b)
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	deadline := time.Now().Add(2 * time.Second)
	for !b.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !b.IsConnected() {
		t.Fatalf("adapter never ready: %+v", b.Status("fixture-camera"))
	}
	if b.Status("fixture-camera").State != "waiting_for_camera" {
		t.Fatal("broker connection mistaken for camera connection")
	}
	if err := ch.SetLED(context.Background(), "fixture-camera", true); err == nil {
		t.Fatal("command succeeded without device")
	}

	client := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(url).SetClientID("explicit-simulated-camera"))
	mqttWait(t, client.Connect())
	defer client.Disconnect(50)
	// A retained report must not confirm that a camera is connected.
	report, _ := json.Marshal(shadowshim.ShadowUpdateRequest{State: shadowshim.ShadowState{Reported: map[string]any{"LedOnOff": 1}}})
	mqttWait(t, client.Publish("$aws/things/retained-fixture/shadow/update", 1, true, report))
	// A newly connected adapter receives this as a retained snapshot, not as
	// evidence of a live camera. Use a separate daemon to avoid shared state.
	ch2 := NewMQTTControlChannel(nil, 100*time.Millisecond)
	defer ch2.Close()
	b2 := NewMosquittoBridge(MosquittoBridgeConfig{BrokerURL: url, ClientID: "retained-check-adapter", ConnectTimeout: time.Second}, ch2.Daemon())
	ch2.AttachBroker(b2)
	if err := b2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	deadline = time.Now().Add(time.Second)
	for !b2.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !b2.IsConnected() {
		t.Fatal("second adapter unavailable")
	}
	if err := ch2.SetLED(context.Background(), "retained-fixture", true); err == nil {
		t.Fatal("retained report confirmed a new command")
	}
	if s := b2.Status("retained-fixture"); s.ReportCount != 0 {
		t.Fatalf("retained snapshot counted as camera traffic: %+v", s)
	}
	_ = b2.Close()
	// Install a responder only for this simulated test device, over actual MQTT.
	mqttWait(t, client.Subscribe("$aws/things/fixture-camera/shadow/update/delta", 1, func(c mqtt.Client, msg mqtt.Message) {
		var delta shadowshim.ShadowDeltaMessage
		if json.Unmarshal(msg.Payload(), &delta) != nil {
			return
		}
		payload, _ := json.Marshal(shadowshim.ShadowUpdateRequest{State: shadowshim.ShadowState{Reported: delta.State}})
		c.Publish("$aws/things/fixture-camera/shadow/update", 1, false, payload)
	}))
	commands := []func() error{
		func() error { return ch.SetIR(context.Background(), "fixture-camera", IRModeAuto) },
		func() error { return ch.SetLED(context.Background(), "fixture-camera", false) },
		func() error { return ch.SetLight(context.Background(), "fixture-camera", true) },
		func() error { return ch.SetAttribute(context.Background(), "fixture-camera", "MotionDetectSW", 1) },
		func() error { return ch.SetAttribute(context.Background(), "fixture-camera", "SoundDetectSW", 1) },
		func() error { return ch.ArmTalk(context.Background(), "fixture-camera", true, "real-session-fixture") },
		func() error { return ch.MovePTZ(context.Background(), "fixture-camera", 1, 40) },
	}
	for i, command := range commands {
		if err := command(); err != nil {
			t.Fatalf("command %d: %v", i, err)
		}
	}
	if status := b.Status("fixture-camera"); status.ReportCount == 0 || status.State != "camera_report_received" {
		t.Fatalf("missing broker report evidence: %+v", status)
	}
	mqttWait(t, client.Unsubscribe("$aws/things/fixture-camera/shadow/update/delta"))
	if err := ch.SetLED(context.Background(), "fixture-camera", false); err == nil {
		t.Fatal("prior matching report confirmed a new command")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ch.SetLED(context.Background(), "fixture-camera", true); err == nil {
		t.Fatal("closed broker accepted command")
	}
}
