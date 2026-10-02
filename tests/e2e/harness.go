package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Fever-r/BombeCam/internal/buildkey"
	"github.com/Fever-r/BombeCam/pkg/bridge"
	"github.com/Fever-r/BombeCam/pkg/profile"
	"github.com/eclipse/paho.mqtt.golang/packets"
	"golang.org/x/crypto/ssh"
)

// ----------------------------------------------------------------------------
// Mock Router (OpenWrt over SSH)
// ----------------------------------------------------------------------------

// MockRouterPassword is the SSH password the mock router accepts.
const MockRouterPassword = "router-admin-pass"

// MockRouter is an in-process SSH server standing in for an OpenWrt/GL.iNet
// router. Each command the gateway sends is executed for real with sh, with
// the uploaded BombeCam router script running in dry-run mode against a
// private fake root (Root), so tests can inspect exactly what the router
// would have been configured with.
type MockRouter struct {
	Addr     string
	Root     string
	HostKey  string
	listener net.Listener
	mu       sync.Mutex
	Commands []string
	Outputs  []string
	// KeyLogins counts sessions authenticated with BombeCam's router key.
	KeyLogins int
}

// routerShell reports whether the mock router can run the router script,
// which needs a POSIX sh as on a real router. Windows has none.
func routerShell() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	_, err := exec.LookPath("sh")
	return err == nil
}

// requireRouterShell skips a test that applies rules on the mock router
// where it can't run the router script.
func requireRouterShell(t *testing.T) {
	t.Helper()
	if !routerShell() {
		t.Skip("the mock router runs the router script with a POSIX sh")
	}
}

// AuthorizedKeysPath is where the router script installs BombeCam's key.
func (m *MockRouter) AuthorizedKeysPath() string {
	return filepath.Join(m.Root, "etc/dropbear/authorized_keys")
}

// NewMockRouter starts a mock router. fw is "fw4" or "fw3".
func NewMockRouter(t *testing.T, fw string) *MockRouter {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("mock router host key: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock router listen: %v", err)
	}
	m := &MockRouter{Addr: ln.Addr().String(), Root: t.TempDir(), HostKey: ssh.FingerprintSHA256(signer.PublicKey()), listener: ln}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == MockRouterPassword {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
		// Like dropbear: a key listed in authorized_keys logs in, and its
		// command="..." option replaces whatever the client asked to run.
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			data, _ := os.ReadFile(m.AuthorizedKeysPath())
			for len(data) > 0 {
				pub, _, opts, rest, err := ssh.ParseAuthorizedKey(data)
				if err != nil {
					break
				}
				data = rest
				if !bytes.Equal(pub.Marshal(), key.Marshal()) {
					continue
				}
				perms := &ssh.Permissions{Extensions: map[string]string{}}
				for _, o := range opts {
					if v, ok := strings.CutPrefix(o, "command="); ok {
						perms.Extensions["force-command"] = strings.Trim(v, `"`)
					}
				}
				m.mu.Lock()
				m.KeyLogins++
				m.mu.Unlock()
				return perms, nil
			}
			return nil, fmt.Errorf("unknown key")
		},
	}
	cfg.AddHostKey(signer)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go m.serve(c, cfg, fw)
		}
	}()
	return m
}

func (m *MockRouter) serve(c net.Conn, cfg *ssh.ServerConfig, fw string) {
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	forced := ""
	if conn.Permissions != nil {
		forced = conn.Permissions.Extensions["force-command"]
	}
	env := append(os.Environ(), "BC_ROOT="+m.Root, "BC_DRYRUN=1", "BC_FW="+fw, "BC_AUTH_KEYS="+m.AuthorizedKeysPath())
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range creqs {
				if req.Type != "exec" || len(req.Payload) < 4 {
					_ = req.Reply(false, nil)
					continue
				}
				n := int(req.Payload[0])<<24 | int(req.Payload[1])<<16 | int(req.Payload[2])<<8 | int(req.Payload[3])
				command := string(req.Payload[4 : 4+n])
				_ = req.Reply(true, nil)
				local := strings.ReplaceAll(command, "/tmp/bombecam-router.sh", filepath.Join(m.Root, "uploaded.sh"))
				cmd := exec.Command("sh", "-c", local)
				cmd.Env = env
				if forced != "" {
					cmd = exec.Command("sh", "-c", forced)
					cmd.Env = append(env, "SSH_ORIGINAL_COMMAND="+command)
				}
				out := &lockedBuffer{}
				cmd.Stdin = ch
				cmd.Stdout = io.MultiWriter(ch, out)
				cmd.Stderr = io.MultiWriter(ch.Stderr(), out)
				status := 0
				if err := cmd.Run(); err != nil {
					status = 1
					if ee, ok := err.(*exec.ExitError); ok {
						status = ee.ExitCode()
					}
				}
				m.mu.Lock()
				m.Commands = append(m.Commands, command)
				m.Outputs = append(m.Outputs, out.String())
				m.mu.Unlock()
				_, _ = ch.SendRequest("exit-status", false, []byte{byte(status >> 24), byte(status >> 16), byte(status >> 8), byte(status)})
				ch.Close()
			}
		}()
	}
}

// lockedBuffer is a bytes.Buffer safe for the concurrent stdout/stderr copiers.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// CameraList returns the router's saved camera list ("mac\tname" lines), or "".
func (m *MockRouter) CameraList() string {
	b, _ := os.ReadFile(filepath.Join(m.Root, "etc/bombecam/cameras"))
	return string(b)
}

// Installed reports whether BombeCam's rules are configured on the router.
func (m *MockRouter) Installed() bool {
	_, err := os.Stat(filepath.Join(m.Root, "etc/bombecam/cameras"))
	return err == nil
}

// Connected reports whether BombeCam's key is installed on the router.
func (m *MockRouter) Connected() bool {
	b, _ := os.ReadFile(m.AuthorizedKeysPath())
	return strings.Contains(string(b), "bombecam-router gate")
}

// KeyLoginCount returns how many sessions used BombeCam's router key.
func (m *MockRouter) KeyLoginCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.KeyLogins
}

// CommandCount returns how many commands the router has run.
func (m *MockRouter) CommandCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Commands)
}

// LastOutput returns the output of the most recent command.
func (m *MockRouter) LastOutput() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Outputs) == 0 {
		return ""
	}
	return m.Outputs[len(m.Outputs)-1]
}

// Close stops the mock router.
func (m *MockRouter) Close() {
	if m.listener != nil {
		_ = m.listener.Close()
	}
}

// ----------------------------------------------------------------------------
// Mock Cloud Backend (Osaio)
// ----------------------------------------------------------------------------

// MockCloudBackend simulates the vendor cloud backend for discovery, authentication, and inventory.
type MockCloudBackend struct {
	Server          *httptest.Server
	URL             string
	mu              sync.Mutex
	Devices         []map[string]any
	ValidEmail      string
	ValidPassword   string
	AuthUID         string
	AuthToken       string
	RejectLogin     bool
	SimulateTimeout bool
	LoginCalls      int
	DeviceListCalls int
	// SignKey is the server key requests must be signed with. Requests with
	// a wrong signature are refused and counted in BadSignatures.
	SignKey string
	// AppID is the app ID requests must carry (E2EAppID until SetAppID).
	AppID         string
	Requests      int
	BadSignatures int
}

// E2EServerKey is the made-up server key the harness gives the gateway and
// expects in request signatures. No real key is used in tests.
const E2EServerKey = "synthetic-e2e-server-key"

// E2EBuiltInKey and E2EAppID are the made-up server key and app ID the
// harness builds into the gateway (obscured), the way release builds include
// the real ones (internal/buildkey). The mock cloud refuses any other app ID.
const (
	E2EBuiltInKey = "synthetic-e2e-built-in-key"
	E2EAppID      = "synthetic-e2e-app-id"
)

// validSignature recomputes the request signature from the headers.
func validSignature(r *http.Request, key string) bool {
	msg := r.Header.Get("appid") + r.Header.Get("timestamp")
	switch r.Header.Get("ApiSignType") {
	case "1":
	case "2":
		msg += r.Header.Get("uid") + r.Header.Get("api-token")
	default:
		return false
	}
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(msg))
	want := base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(m.Sum(nil))))
	return hmac.Equal([]byte(want), []byte(r.Header.Get("sign")))
}

// checkSignature counts requests and refuses those not signed with SignKey.
func (m *MockCloudBackend) checkSignature(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.Requests++
		key := m.SignKey
		appID := m.AppID
		if appID == "" {
			appID = E2EAppID
		}
		bad := key != "" && (!validSignature(r, key) || r.Header.Get("appid") != appID)
		if bad {
			m.BadSignatures++
		}
		m.mu.Unlock()
		if bad {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 2001, "msg": "bad signature"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SetAppID changes the app ID requests must carry, as when the vendor
// changes its app ID.
func (m *MockCloudBackend) SetAppID(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.AppID = id
}

// SetSignKey changes the key requests must be signed with, as when the
// vendor rotates its key.
func (m *MockCloudBackend) SetSignKey(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SignKey = key
}

// Counts returns how many requests arrived and how many were badly signed.
func (m *MockCloudBackend) Counts() (requests, badSignatures int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Requests, m.BadSignatures
}

// ExpectedLANHost is the address the gateway advertises to NVRs on this
// machine: its default-route IPv4 address (the gateway runs here too), or
// 127.0.0.1 when there is no network.
func ExpectedLANHost() string {
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		defer c.Close()
		if ip := c.LocalAddr().(*net.UDPAddr).IP; ip != nil && !ip.IsLoopback() && ip.To4() != nil {
			return ip.String()
		}
	}
	return "127.0.0.1"
}

// DefaultMockDevices returns a standard 4-camera test inventory.
func DefaultMockDevices() []map[string]any {
	return []map[string]any{
		{
			"uuid":       "cam-001",
			"name":       "Front Porch",
			"type":       "WS03",
			"model_type": 1,
			"online":     1,
		},
		{
			"uuid":       "cam-002",
			"name":       "Back Garden",
			"type":       "WS04",
			"model_type": 1,
			"online":     1,
		},
		{
			"uuid":       "cam-003",
			"name":       "Driveway",
			"type":       "WS03",
			"model_type": 1,
			"online":     1,
		},
		{
			"uuid":       "cam-004",
			"name":       "Garage",
			"type":       "WS03",
			"model_type": 1,
			"online":     0, // Offline camera
		},
	}
}

// NewMockCloudBackend creates and starts a new MockCloudBackend on an ephemeral port.
func NewMockCloudBackend(t *testing.T, initialDevices []map[string]any) *MockCloudBackend {
	if initialDevices == nil {
		initialDevices = DefaultMockDevices()
	}

	m := &MockCloudBackend{
		Devices:       initialDevices,
		ValidEmail:    "",
		ValidPassword: "",
		AuthUID:       "mock-uid-test-100",
		AuthToken:     "mock-auth-token-test-abc",
		SignKey:       E2EServerKey,
	}

	mux := http.NewServeMux()

	// GET /v2/account/get-baseurl
	mux.HandleFunc("/v2/account/get-baseurl", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 1000,
			"data": map[string]any{
				"region": "US",
				"web":    m.URL,
				"ws":     "wss://mock.osaio.net/ws",
			},
		})
	})

	// POST /v2/login/login & /app/v1/user/login
	loginHandler := func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.LoginCalls++
		simTimeout := m.SimulateTimeout
		reject := m.RejectLogin
		validEmail := m.ValidEmail
		validPass := m.ValidPassword
		uid := m.AuthUID
		token := m.AuthToken
		m.mu.Unlock()

		if simTimeout {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 5003, "msg": "cloud service timeout"})
			return
		}

		if reject {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 2001, "msg": "invalid credentials"})
			return
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)

		// Check if credentials match if specified
		if acc, ok := body["account"].(string); ok && acc != "" && validEmail != "" && acc != validEmail {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 2001, "msg": "account not found"})
			return
		}
		if pwd, ok := body["password"].(string); ok && pwd != "" && validPass != "" && pwd != validPass {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 2001, "msg": "wrong password"})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 1000,
			"data": map[string]any{
				"uid":       uid,
				"api_token": token,
			},
		})
	}
	mux.HandleFunc("/v2/login/login", loginHandler)
	mux.HandleFunc("/app/v1/user/login", loginHandler)

	// GET /v2/device/list & /app/v1/device/list
	deviceHandler := func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.DeviceListCalls++
		devs := make([]map[string]any, len(m.Devices))
		copy(devs, m.Devices)
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 1000,
			"data": map[string]any{
				"data": devs,
			},
		})
	}
	mux.HandleFunc("/v2/device/list", deviceHandler)
	mux.HandleFunc("/app/v1/device/list", deviceHandler)

	// POST /v2/webrtcsession/user/videocall
	mux.HandleFunc("/v2/webrtcsession/user/videocall", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 1000,
			"data": map[string]any{
				"session_id": "mock-videocall-sess-1",
				"token":      "mock-videocall-tok-1",
			},
		})
	})

	m.Server = httptest.NewServer(m.checkSignature(mux))
	m.URL = m.Server.URL
	return m
}

func (m *MockCloudBackend) SetDevices(devices []map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Devices = devices
}

func (m *MockCloudBackend) SetRejectLogin(reject bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RejectLogin = reject
}

func (m *MockCloudBackend) SetSimulateTimeout(timeout bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SimulateTimeout = timeout
}

func (m *MockCloudBackend) GetLoginCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.LoginCalls
}

func (m *MockCloudBackend) GetDeviceListCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.DeviceListCalls
}

func (m *MockCloudBackend) Close() {
	if m.Server != nil {
		m.Server.Close()
	}
}

// ----------------------------------------------------------------------------
// Mock Mosquitto MQTT Broker & Simulated Device
// ----------------------------------------------------------------------------

// MockMQTTBroker simulates an external Mosquitto broker and physical camera responding over MQTT.
type MockMQTTBroker struct {
	listener  net.Listener
	URL       string
	stopCh    chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	conns     []net.Conn
}

func NewMockMQTTBroker(t *testing.T) *MockMQTTBroker {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock MQTT broker: %v", err)
	}

	b := &MockMQTTBroker{
		listener: ln,
		URL:      fmt.Sprintf("tcp://%s", ln.Addr().String()),
		stopCh:   make(chan struct{}),
	}

	go b.acceptLoop()
	return b
}

func (b *MockMQTTBroker) acceptLoop() {
	for {
		conn, err := b.listener.Accept()
		if err != nil {
			select {
			case <-b.stopCh:
				return
			default:
				return
			}
		}

		b.mu.Lock()
		b.conns = append(b.conns, conn)
		b.mu.Unlock()

		go b.handleConn(conn)
	}
}

func (b *MockMQTTBroker) handleConn(conn net.Conn) {
	defer conn.Close()

	var writeMu sync.Mutex
	writePacket := func(cp packets.ControlPacket) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return cp.Write(conn)
	}

	for {
		packet, err := packets.ReadPacket(conn)
		if err != nil {
			return
		}

		switch p := packet.(type) {
		case *packets.ConnectPacket:
			connack := packets.NewControlPacket(packets.Connack).(*packets.ConnackPacket)
			connack.ReturnCode = packets.Accepted
			_ = writePacket(connack)

		case *packets.PingreqPacket:
			pingresp := packets.NewControlPacket(packets.Pingresp).(*packets.PingrespPacket)
			_ = writePacket(pingresp)

		case *packets.SubscribePacket:
			suback := packets.NewControlPacket(packets.Suback).(*packets.SubackPacket)
			suback.MessageID = p.MessageID
			suback.ReturnCodes = make([]byte, len(p.Topics))
			for i := range p.Topics {
				suback.ReturnCodes[i] = p.Qoss[i]
			}
			_ = writePacket(suback)

		case *packets.PubackPacket:
			// No-op

		case *packets.PublishPacket:
			if p.Qos == 1 {
				puback := packets.NewControlPacket(packets.Puback).(*packets.PubackPacket)
				puback.MessageID = p.MessageID
				_ = writePacket(puback)
			}

			// When a camera delta command is received, the simulated camera independently executes
			// and publishes its updated reported state back to Mosquitto on $aws/things/{thing}/shadow/update
			if strings.Contains(p.TopicName, "/shadow/update/delta") {
				parts := strings.Split(p.TopicName, "/")
				if len(parts) >= 3 {
					thingName := parts[2]
					var deltaDoc struct {
						State map[string]any `json:"state"`
					}
					if err := json.Unmarshal(p.Payload, &deltaDoc); err == nil && len(deltaDoc.State) > 0 {
						reportedDoc := map[string]any{
							"state": map[string]any{
								"reported": deltaDoc.State,
							},
						}
						repPayload, _ := json.Marshal(reportedDoc)
						respPub := packets.NewControlPacket(packets.Publish).(*packets.PublishPacket)
						respPub.TopicName = fmt.Sprintf("$aws/things/%s/shadow/update", thingName)
						respPub.Payload = repPayload
						respPub.Qos = 0
						go func() {
							time.Sleep(20 * time.Millisecond)
							_ = writePacket(respPub)
						}()
					}
				}
			}

		case *packets.DisconnectPacket:
			return
		}
	}
}

func (b *MockMQTTBroker) Close() {
	b.closeOnce.Do(func() {
		close(b.stopCh)
		if b.listener != nil {
			_ = b.listener.Close()
		}
		b.mu.Lock()
		for _, c := range b.conns {
			_ = c.Close()
		}
		b.mu.Unlock()
	})
}

// ----------------------------------------------------------------------------
// Mock Control Channel (bridge.ControlChannel)
// ----------------------------------------------------------------------------

// MockControlChannel is a thread-safe in-memory bridge.ControlChannel mock.
type MockControlChannel struct {
	mu          sync.Mutex
	PTZCalls    []map[string]any
	IRModes     map[string]int
	LEDStates   map[string]bool
	LightStates map[string]bool
	FailNext    bool
}

var _ bridge.ControlChannel = (*MockControlChannel)(nil)

// NewMockControlChannel creates a new MockControlChannel.
func NewMockControlChannel() *MockControlChannel {
	return &MockControlChannel{
		IRModes:     make(map[string]int),
		LEDStates:   make(map[string]bool),
		LightStates: make(map[string]bool),
	}
}

func (m *MockControlChannel) MovePTZ(ctx context.Context, cameraUUID string, direction int, durationMs int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailNext {
		m.FailNext = false
		return fmt.Errorf("control channel hardware failure")
	}
	m.PTZCalls = append(m.PTZCalls, map[string]any{
		"camera_uuid": cameraUUID,
		"direction":   direction,
		"duration_ms": durationMs,
	})
	return nil
}

func (m *MockControlChannel) SetIR(ctx context.Context, cameraUUID string, mode int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.IRModes[cameraUUID] = mode
	return nil
}

func (m *MockControlChannel) GetIR(ctx context.Context, cameraUUID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.IRModes[cameraUUID], nil
}

func (m *MockControlChannel) SetLED(ctx context.Context, cameraUUID string, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.LEDStates[cameraUUID] = on
	return nil
}

func (m *MockControlChannel) GetLED(ctx context.Context, cameraUUID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.LEDStates[cameraUUID], nil
}

func (m *MockControlChannel) SetLight(ctx context.Context, cameraUUID string, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.LightStates[cameraUUID] = on
	return nil
}

func (m *MockControlChannel) ArmTalk(ctx context.Context, cameraUUID string, enable bool, sessionID string) error {
	return nil
}

func (m *MockControlChannel) SetAttribute(ctx context.Context, cameraUUID, key string, value any) error {
	return nil
}

func (m *MockControlChannel) Close() error {
	return nil
}

// ----------------------------------------------------------------------------
// Gateway Subprocess & E2E Test Harness
// ----------------------------------------------------------------------------

var (
	binaryBuildMu sync.Mutex
	// cachedBinaries holds one gateway build per built-in key ("" = none).
	cachedBinaries = map[string]string{}
)

func findProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}

func getOrBuildGatewayBinary() (string, error) {
	return buildGatewayBinary(E2EBuiltInKey, E2EAppID)
}

// buildGatewayBinary builds the gateway with the server key and app ID linked
// in, as a release is; "" leaves one out, as a build from source without
// osaio-setup.txt does.
func buildGatewayBinary(builtInKey, builtInAppID string) (string, error) {
	binaryBuildMu.Lock()
	defer binaryBuildMu.Unlock()

	cacheKey := builtInKey + "\x00" + builtInAppID
	if bin := cachedBinaries[cacheKey]; bin != "" {
		if _, err := os.Stat(bin); err == nil {
			return bin, nil
		}
	}

	root := findProjectRoot()
	tmpDir, err := os.MkdirTemp("", "bombecam-bin-*")
	if err != nil {
		return "", err
	}

	binName := "bombecam-gateway"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(tmpDir, binName)

	flags := buildkey.LDFlags(buildkey.Found{Value: map[string]string{buildkey.ServerKey.Name: builtInKey, buildkey.AppID.Name: builtInAppID}})
	cmd := exec.Command("go", "build", "-ldflags", flags, "-o", binPath, "./cmd/bombecam-gateway")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go build failed: %w\nOutput: %s", err, string(out))
	}

	cachedBinaries[cacheKey] = binPath
	return binPath, nil
}

func getFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// GatewayHarness manages an automated end-to-end black-box gateway subprocess.
type GatewayHarness struct {
	T           *testing.T
	Cmd         *exec.Cmd
	cmdExited   chan struct{}
	Port        int
	BaseURL     string
	TempDir     string
	ProfilePath string
	KeyPath     string
	MasterKey   []byte
	MockRouter  *MockRouter
	MockCloud   *MockCloudBackend
	MockMQTT    *MockMQTTBroker
	StdoutBuf   *syncBuffer
	StderrBuf   *syncBuffer
	Client      *http.Client
	BinaryPath  string
	ExtraArgs   []string
	APIToken    string
	CSRFToken   string
	// ServerKey is passed to the gateway as BOMBECAM_SERVER_KEY; empty leaves
	// it to the saved key file or the built-in key, as on a fresh download.
	ServerKey string
	// AppID is passed to the gateway as BOMBECAM_APP_ID; empty (the default)
	// leaves it to the saved app.id or the built-in app ID.
	AppID string
}

// WithAppIDInEnvironment passes id to the gateway as BOMBECAM_APP_ID.
func WithAppIDInEnvironment(id string) HarnessOption {
	return func(h *GatewayHarness) {
		h.AppID = id
	}
}

// WithoutServerKey starts the gateway with no key in its environment, so it
// uses a saved server.key or the key built into this version.
func WithoutServerKey() HarnessOption {
	return func(h *GatewayHarness) {
		h.ServerKey = ""
	}
}

// WithoutBuiltInKey runs a gateway built with the app ID but no server key,
// as a build from source whose setup has only the app ID. Combine it with
// WithoutServerKey.
func WithoutBuiltInKey() HarnessOption {
	return withBuild("", E2EAppID)
}

// WithoutBuiltInValues runs a gateway built with neither value, as a build
// from source without osaio-setup.txt.
func WithoutBuiltInValues() HarnessOption {
	return withBuild("", "")
}

func withBuild(key, appID string) HarnessOption {
	return func(h *GatewayHarness) {
		bin, err := buildGatewayBinary(key, appID)
		if err != nil {
			h.T.Fatalf("building the gateway: %v", err)
		}
		h.BinaryPath = bin
	}
}

// HarnessOption allows configuring GatewayHarness.
type HarnessOption func(h *GatewayHarness)

// WithInitialProfile configures an initial profile before gateway startup.
func WithInitialProfile(p *profile.Profile) HarnessOption {
	return func(h *GatewayHarness) {
		pm := profile.NewManager(h.ProfilePath, h.MasterKey)
		if err := pm.Save(context.Background(), p); err != nil {
			h.T.Fatalf("failed to pre-seed profile: %v", err)
		}
	}
}

// WithSeededCameras pre-seeds a signed-in profile with two cameras whose MAC
// addresses are already known (as BombeCam learns them from a LAN stream):
// cam-001 "Test Camera" aa:bb:cc:dd:ee:01 and cam-002 "Porch" aa:bb:cc:dd:ee:02.
func WithSeededCameras() HarnessOption {
	return func(h *GatewayHarness) {
		now := time.Now().UTC()
		WithInitialProfile(&profile.Profile{
			Version:   profile.CurrentSchemaVersion,
			CreatedAt: now,
			Credentials: profile.CloudCredentials{
				AccountEmail: "test-user@example.com", Password: "TestPassword123!", Country: "1",
				AuthToken: "mock-token-seeded", TokenExpiresAt: now.Add(24 * time.Hour),
				Region: h.MockCloud.URL, VendorUID: "uid-seeded",
			},
			Cameras: map[string]profile.CameraProfile{
				"cam-001": {UUID: "cam-001", Name: "Test Camera", Model: "WS03", MACAddress: "AA:BB:CC:DD:EE:01", EnrolledAt: now, Online: true},
				"cam-002": {UUID: "cam-002", Name: "Porch", Model: "WS03", MACAddress: "aa:bb:cc:dd:ee:02", EnrolledAt: now, Online: true},
			},
		})(h)
	}
}

// WithCorruptedProfile writes a corrupted envelope file.
func WithCorruptedProfile() HarnessOption {
	return func(h *GatewayHarness) {
		_ = os.WriteFile(h.ProfilePath, []byte(`{"magic":"BOMBE_ENC_V1","ciphertext":"bad-base64!"}`), 0600)
	}
}

// WithMockDevices overrides the default mock devices.
func WithMockDevices(devices []map[string]any) HarnessOption {
	return func(h *GatewayHarness) {
		h.MockCloud.SetDevices(devices)
	}
}

// WithExtraArgs appends CLI args to the gateway command.
func WithExtraArgs(args ...string) HarnessOption {
	return func(h *GatewayHarness) {
		h.ExtraArgs = append(h.ExtraArgs, args...)
	}
}

// NewGatewayHarness boots up an ephemeral gateway subprocess with mock servers.
func NewGatewayHarness(t *testing.T, opts ...HarnessOption) *GatewayHarness {
	t.Helper()

	binPath, err := getOrBuildGatewayBinary()
	if err != nil {
		t.Fatalf("failed to build gateway binary: %v", err)
	}

	tmpDir := t.TempDir()
	profPath := filepath.Join(tmpDir, "profile.enc")
	keyPath := filepath.Join(tmpDir, "profile.key")

	masterKey := make([]byte, 32)
	for i := range masterKey {
		masterKey[i] = byte(i + 1)
	}
	if err := os.WriteFile(keyPath, masterKey, 0600); err != nil {
		t.Fatalf("failed to write key file: %v", err)
	}

	mockRouter := NewMockRouter(t, "fw4")
	mockCloud := NewMockCloudBackend(t, nil)
	mockMQTT := NewMockMQTTBroker(t)

	apiToken := "bc_tok_e2etest0123456789abcdef0123456789abcdef0123456789abcdef01234567"
	tokensData, _ := json.Marshal(map[string]time.Time{
		apiToken: time.Now().UTC().Add(365 * 24 * time.Hour),
	})
	_ = os.WriteFile(filepath.Join(tmpDir, "api_tokens.json"), tokensData, 0600)

	jar, _ := cookiejar.New(nil)
	h := &GatewayHarness{
		T:           t,
		TempDir:     tmpDir,
		ProfilePath: profPath,
		KeyPath:     keyPath,
		MasterKey:   masterKey,
		MockRouter:  mockRouter,
		MockCloud:   mockCloud,
		MockMQTT:    mockMQTT,
		StdoutBuf:   &syncBuffer{},
		StderrBuf:   &syncBuffer{},
		Client:      &http.Client{Timeout: 5 * time.Second, Jar: jar},
		BinaryPath:  binPath,
		APIToken:    apiToken,
		ServerKey:   E2EServerKey,
	}

	for _, opt := range opts {
		opt(h)
	}

	h.startSubprocess()

	t.Cleanup(func() {
		h.Teardown()
	})

	return h
}

func (h *GatewayHarness) startSubprocess() {
	port, err := getFreePort()
	if err != nil {
		h.T.Fatalf("failed to allocate free port: %v", err)
	}
	h.Port = port
	h.BaseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	args := []string{
		"-headless",
		"-no-mediamtx-supervisor",
		fmt.Sprintf("-http=127.0.0.1:%d", port),
		fmt.Sprintf("-profile-path=%s", h.ProfilePath),
		fmt.Sprintf("-profile-key-file=%s", h.KeyPath),
		"-control-transport=mqtt",
		fmt.Sprintf("-mosquitto-url=%s", h.MockMQTT.URL),
		"-rtsp-base=rtsp://127.0.0.1:8554",
		"-hls-base=http://127.0.0.1:8888",
	}
	args = append(args, h.ExtraArgs...)

	h.Cmd = exec.Command(h.BinaryPath, args...)
	h.Cmd.Dir = h.TempDir
	h.Cmd.Stdout = h.StdoutBuf
	h.Cmd.Stderr = h.StderrBuf

	h.Cmd.Env = append(os.Environ(),
		fmt.Sprintf("OSAIO_GLOBAL_BASE=%s", h.MockCloud.URL),
		fmt.Sprintf("OSAIO_DEFAULT_WEB=%s", h.MockCloud.URL),
		fmt.Sprintf("BOMBECAM_PROFILE_PATH=%s", h.ProfilePath),
		fmt.Sprintf("BOMBECAM_PROFILE_KEY_FILE=%s", h.KeyPath),
		"BOMBECAM_HEADLESS=1",
		"BOMBECAM_NO_MEDIAMTX_SUPERVISOR=1",
		// Later entries win, so a key in the developer's own environment
		// never reaches the test gateway.
		"BOMBECAM_SERVER_KEY="+h.ServerKey,
		"BOMBECAM_SERVER_KEY_FILE=",
		"BOMBECAM_APP_ID="+h.AppID,
		"BOMBECAM_APP_ID_FILE=",
	)

	if err := h.Cmd.Start(); err != nil {
		h.T.Fatalf("failed to start gateway subprocess: %v", err)
	}
	cmd := h.Cmd
	h.cmdExited = make(chan struct{})
	exited := h.cmdExited
	go func() { _ = cmd.Wait(); close(exited) }()

	// Poll until ready
	ready := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, h.BaseURL+"/api/v1/onboarding/status", nil)
		if err == nil {
			resp, _, err := h.DoRequest(req)
			if err == nil && resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	if !ready {
		h.KillGateway()
		h.T.Fatalf("gateway failed to become ready on %s within 10s.\nStdout:\n%s\nStderr:\n%s",
			h.BaseURL, h.StdoutBuf.String(), h.StderrBuf.String())
	}
}

// KillGateway forcefully terminates the running gateway subprocess.
func (h *GatewayHarness) KillGateway() {
	if h.Cmd == nil || h.Cmd.Process == nil {
		return
	}
	select {
	case <-h.cmdExited:
		h.Cmd = nil
		return
	default:
	}
	var err error
	if runtime.GOOS == "windows" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err = exec.CommandContext(ctx, "taskkill", "/F", "/T", "/PID", fmt.Sprint(h.Cmd.Process.Pid)).Run()
		cancel()
	}
	if runtime.GOOS != "windows" || err != nil {
		err = h.Cmd.Process.Kill()
	}
	select {
	case <-h.cmdExited:
		h.Cmd = nil
	case <-time.After(5 * time.Second):
		h.T.Errorf("gateway process %d exit not confirmed after bounded cleanup: %v", h.Cmd.Process.Pid, err)
	}
}

// RestartGateway kills the active process and cold-restarts from disk.
func (h *GatewayHarness) RestartGateway() {
	h.KillGateway()
	jar, _ := cookiejar.New(nil)
	h.Client.Jar = jar
	h.CSRFToken = ""
	h.startSubprocess()
}

// Teardown cleans up the gateway process and mock servers.
func (h *GatewayHarness) Teardown() {
	h.KillGateway()
	if h.MockRouter != nil {
		h.MockRouter.Close()
	}
	if h.MockCloud != nil {
		h.MockCloud.Close()
	}
	if h.MockMQTT != nil {
		h.MockMQTT.Close()
	}
}

// syncBuffer is a bytes.Buffer that the child-process copier can write to
// while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// GetCapturedLogs returns combined captured stdout and stderr.
func (h *GatewayHarness) GetCapturedLogs() string {
	return h.StdoutBuf.String() + "\n" + h.StderrBuf.String()
}

// AssertZeroSecrets asserts that no canary secret is present in logs, stderr, or on-disk files.
func (h *GatewayHarness) AssertZeroSecrets(secrets []string) {
	h.T.Helper()
	logs := h.GetCapturedLogs()
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		if strings.Contains(logs, sec) {
			h.T.Fatalf("SECURITY VIOLATION: Secret %q leaked in gateway stdout/stderr logs!", sec)
		}
	}

	if raw, err := os.ReadFile(h.ProfilePath); err == nil {
		rawStr := string(raw)
		for _, sec := range secrets {
			if sec != "" && strings.Contains(rawStr, sec) {
				h.T.Fatalf("SECURITY VIOLATION: Secret %q leaked in on-disk profile file!", sec)
			}
		}
	}
}

// ----------------------------------------------------------------------------
// Fluent HTTP API Helpers
// ----------------------------------------------------------------------------

func (h *GatewayHarness) DoRequest(req *http.Request) (*http.Response, string, error) {
	if h.APIToken != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+h.APIToken)
	}
	if h.CSRFToken != "" && req.Header.Get("X-CSRF-Token") == "" {
		if req.Method == http.MethodPost || req.Method == http.MethodPut || req.Method == http.MethodDelete || req.Method == http.MethodPatch {
			req.Header.Set("X-CSRF-Token", h.CSRFToken)
		}
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, "", err
	}
	return resp, string(bodyBytes), nil
}

func (h *GatewayHarness) Get(path string) (*http.Response, string, error) {
	req, err := http.NewRequest(http.MethodGet, h.BaseURL+path, nil)
	if err != nil {
		return nil, "", err
	}
	return h.DoRequest(req)
}

func (h *GatewayHarness) PostJSON(path string, body any) (*http.Response, string, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, "", err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(http.MethodPost, h.BaseURL+path, reader)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	return h.DoRequest(req)
}

func (h *GatewayHarness) Delete(path string) (*http.Response, string, error) {
	req, err := http.NewRequest(http.MethodDelete, h.BaseURL+path, nil)
	if err != nil {
		return nil, "", err
	}
	return h.DoRequest(req)
}

func (h *GatewayHarness) GetOnboardingStatus() (int, map[string]any) {
	h.T.Helper()
	resp, body, err := h.Get("/api/v1/onboarding/status")
	if err != nil {
		h.T.Fatalf("GetOnboardingStatus failed: %v", err)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(body), &res)
	return resp.StatusCode, res
}

func (h *GatewayHarness) Setup(payload any) (int, map[string]any) {
	h.T.Helper()
	resp, body, err := h.PostJSON("/api/v1/onboarding/setup", payload)
	if err != nil {
		h.T.Fatalf("Setup failed: %v", err)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(body), &res)
	if tok, ok := res["csrf_token"].(string); ok {
		h.CSRFToken = tok
	}
	return resp.StatusCode, res
}

func (h *GatewayHarness) DiscoverCameras(refresh bool) (int, map[string]any) {
	h.T.Helper()
	path := "/api/v1/onboarding/cameras/discover"
	if refresh {
		path += "?refresh=true"
	}
	resp, body, err := h.Get(path)
	if err != nil {
		h.T.Fatalf("DiscoverCameras failed: %v", err)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(body), &res)
	return resp.StatusCode, res
}

func (h *GatewayHarness) EnrollCameras(ids []string) (int, map[string]any) {
	h.T.Helper()
	resp, body, err := h.PostJSON("/api/v1/onboarding/cameras/enroll", map[string]any{
		"camera_ids": ids,
	})
	if err != nil {
		h.T.Fatalf("EnrollCameras failed: %v", err)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(body), &res)
	return resp.StatusCode, res
}

// GetPrivacy returns GET /api/v1/privacy.
func (h *GatewayHarness) GetPrivacy() (int, map[string]any) {
	h.T.Helper()
	resp, body, err := h.Get("/api/v1/privacy")
	if err != nil {
		h.T.Fatalf("GetPrivacy failed: %v", err)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(body), &res)
	return resp.StatusCode, res
}

// ApplyPrivacy applies "Block cloud video" on the mock router.
func (h *GatewayHarness) ApplyPrivacy(block bool) (int, map[string]any) {
	h.T.Helper()
	return h.ApplyPrivacyWith(map[string]any{
		"block_cloud_video": block,
		"router_address":    h.MockRouter.Addr,
		"router_password":   MockRouterPassword,
	})
}

// ApplyPrivacyWith posts an arbitrary body to POST /api/v1/privacy/apply.
func (h *GatewayHarness) ApplyPrivacyWith(body map[string]any) (int, map[string]any) {
	h.T.Helper()
	resp, raw, err := h.PostJSON("/api/v1/privacy/apply", body)
	if err != nil {
		h.T.Fatalf("ApplyPrivacy failed: %v", err)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(raw), &res)
	return resp.StatusCode, res
}

func (h *GatewayHarness) postPrivacy(path string, body map[string]any) (int, map[string]any) {
	h.T.Helper()
	resp, raw, err := h.PostJSON(path, body)
	if err != nil {
		h.T.Fatalf("POST %s failed: %v", path, err)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(raw), &res)
	return resp.StatusCode, res
}

// ConnectRouter connects the mock router with its password (installs
// BombeCam's key and applies the current choices).
func (h *GatewayHarness) ConnectRouter() (int, map[string]any) {
	h.T.Helper()
	return h.postPrivacy("/api/v1/privacy/router/connect", map[string]any{
		"router_address": h.MockRouter.Addr, "router_password": MockRouterPassword,
	})
}

// SetCameraBlocked turns blocking on or off for one camera.
func (h *GatewayHarness) SetCameraBlocked(camID string, blocked bool) (int, map[string]any) {
	h.T.Helper()
	return h.postPrivacy("/api/v1/privacy/camera", map[string]any{"camera_id": camID, "blocked": blocked})
}

// BlockAll sets every camera (and cameras added later) to blocked or not.
func (h *GatewayHarness) BlockAll(blocked bool) (int, map[string]any) {
	h.T.Helper()
	return h.postPrivacy("/api/v1/privacy/block-all", map[string]any{"blocked": blocked})
}

// DisconnectRouter removes BombeCam's rules and key from the router.
func (h *GatewayHarness) DisconnectRouter() (int, map[string]any) {
	h.T.Helper()
	return h.postPrivacy("/api/v1/privacy/router/disconnect", map[string]any{})
}

func (h *GatewayHarness) GetStream(camID string) (int, map[string]any) {
	h.T.Helper()
	resp, body, err := h.Get("/api/v1/cameras/" + camID + "/stream")
	if err != nil {
		h.T.Fatalf("GetStream failed: %v", err)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(body), &res)
	return resp.StatusCode, res
}

func (h *GatewayHarness) PTZ(camID string, direction any, durationMs int) (int, map[string]any) {
	h.T.Helper()
	payload := map[string]any{
		"direction": direction,
	}
	if durationMs > 0 {
		payload["duration_ms"] = durationMs
	}
	resp, body, err := h.PostJSON("/api/v1/cameras/"+camID+"/ptz", payload)
	if err != nil {
		h.T.Fatalf("PTZ failed: %v", err)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(body), &res)
	return resp.StatusCode, res
}

func (h *GatewayHarness) Control(camID string, action string, value any) (int, map[string]any) {
	h.T.Helper()
	payload := map[string]any{
		"action": action,
	}
	if value != nil {
		payload["value"] = value
	}
	resp, body, err := h.PostJSON("/api/v1/cameras/"+camID+"/control", payload)
	if err != nil {
		h.T.Fatalf("Control failed: %v", err)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(body), &res)
	return resp.StatusCode, res
}

func (h *GatewayHarness) Unlock(camID string) (int, map[string]any) {
	h.T.Helper()
	resp, body, err := h.PostJSON("/api/v1/cameras/"+camID+"/unlock", nil)
	if err != nil {
		h.T.Fatalf("Unlock failed: %v", err)
	}
	var res map[string]any
	_ = json.Unmarshal([]byte(body), &res)
	return resp.StatusCode, res
}

func (h *GatewayHarness) GetFrigate(queryParams ...string) (int, string, map[string]any) {
	h.T.Helper()
	path := "/api/v1/integrations/frigate"
	if len(queryParams) > 0 {
		path += "?" + strings.Join(queryParams, "&")
	}
	resp, body, err := h.Get(path)
	if err != nil {
		h.T.Fatalf("GetFrigate failed: %v", err)
	}
	var jsonRes map[string]any
	_ = json.Unmarshal([]byte(body), &jsonRes)
	return resp.StatusCode, body, jsonRes
}

func (h *GatewayHarness) GetHomeAssistant(queryParams ...string) (int, string, map[string]any) {
	h.T.Helper()
	path := "/api/v1/integrations/homeassistant"
	if len(queryParams) > 0 {
		path += "?" + strings.Join(queryParams, "&")
	}
	resp, body, err := h.Get(path)
	if err != nil {
		h.T.Fatalf("GetHomeAssistant failed: %v", err)
	}
	var jsonRes map[string]any
	_ = json.Unmarshal([]byte(body), &jsonRes)
	return resp.StatusCode, body, jsonRes
}

func (h *GatewayHarness) GetStreams(activeOnly bool) (int, map[string]any) {
	h.T.Helper()
	path := "/api/v1/integrations/streams"
	if activeOnly {
		path += "?active_only=true"
	}
	resp, body, err := h.Get(path)
	if err != nil {
		h.T.Fatalf("GetStreams failed: %v", err)
	}
	var jsonRes map[string]any
	_ = json.Unmarshal([]byte(body), &jsonRes)
	return resp.StatusCode, jsonRes
}

func (h *GatewayHarness) Reset() (int, map[string]any) {
	h.T.Helper()
	resp, body, err := h.PostJSON("/api/v1/onboarding/reset", nil)
	if err != nil {
		h.T.Fatalf("Reset failed: %v", err)
	}
	var jsonRes map[string]any
	_ = json.Unmarshal([]byte(body), &jsonRes)
	return resp.StatusCode, jsonRes
}
