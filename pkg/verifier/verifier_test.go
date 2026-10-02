package verifier

import (
	"context"
	"testing"
	"time"

	"github.com/Fever-r/BombeCam/pkg/policy"
)

type mockProber struct {
	checkPrecondFunc func(ctx context.Context, cameraIP, cameraMAC string) (bool, error)
	execProbeFunc    func(ctx context.Context, probe NetworkProbe, fromCanary bool) bool
	counterIncFunc   func(ctx context.Context) (bool, error)
	attrL2Func       func(ctx context.Context, cameraMAC string, learnedTTL int) (bool, error)
	applyModeFunc    func(ctx context.Context, block bool) error
}

func (m *mockProber) CheckPreconditions(ctx context.Context, cameraIP, cameraMAC string) (bool, error) {
	if m.checkPrecondFunc != nil {
		return m.checkPrecondFunc(ctx, cameraIP, cameraMAC)
	}
	return true, nil
}

func (m *mockProber) ExecuteProbe(ctx context.Context, probe NetworkProbe, fromCanary bool) bool {
	if m.execProbeFunc != nil {
		return m.execProbeFunc(ctx, probe, fromCanary)
	}
	return true
}

func (m *mockProber) CheckCounterIncrement(ctx context.Context) (bool, error) {
	if m.counterIncFunc != nil {
		return m.counterIncFunc(ctx)
	}
	return true, nil
}

func (m *mockProber) AttributeL2(ctx context.Context, cameraMAC string, learnedTTL int) (bool, error) {
	if m.attrL2Func != nil {
		return m.attrL2Func(ctx, cameraMAC, learnedTTL)
	}
	return true, nil
}

func (m *mockProber) ApplyMode(ctx context.Context, block bool) error {
	if m.applyModeFunc != nil {
		return m.applyModeFunc(ctx, block)
	}
	return nil
}

// This fixture explicitly models a kernel-backed, bounded canary lease.
func (m *mockProber) BeginControlLeg(ctx context.Context, _ time.Duration) error {
	return m.ApplyMode(ctx, false)
}

func TestVerifier_NeutralConfigAndMatrix(t *testing.T) {
	d := DefaultConfig()
	if d.CameraIP != "" || d.GatewayIP != "" || d.CameraMAC != "" {
		t.Errorf("DefaultConfig must not contain hardcoded IPs or MAC, got IP=%q, GW=%q, MAC=%q", d.CameraIP, d.GatewayIP, d.CameraMAC)
	}

	cfg := Config{
		CameraID:         "test_cam",
		CameraMAC:        "11:22:33:44:55:66",
		CameraIP:         "10.10.5.20",
		GatewayIP:        "10.10.5.1",
		EnforcementPoint: "nftables",
	}

	prober := &mockProber{}
	v := NewVerifier(cfg, prober, nil)

	if v.cfg.CameraIP != "10.10.5.20" {
		t.Errorf("expected 10.10.5.20, got %s", v.cfg.CameraIP)
	}
	if v.cfg.GatewayIP != "10.10.5.1" {
		t.Errorf("expected 10.10.5.1, got %s", v.cfg.GatewayIP)
	}

	// Test SetProbeMatrix with custom targets
	customMatrix := []NetworkProbe{
		{
			Name:                 "probe_local_dns",
			TargetHost:           "10.10.5.1",
			Port:                 53,
			Protocol:             "udp",
			ExpectedWhenBlocking: true,
		},
	}
	err := v.SetProbeMatrix(customMatrix)
	if err != nil {
		t.Fatalf("SetProbeMatrix failed: %v", err)
	}

	m := v.ProbeMatrix()
	if len(m) != 1 || m[0].TargetHost != "10.10.5.1" {
		t.Errorf("unexpected probe matrix: %v", m)
	}
}

func TestMatrixFromPolicy(t *testing.T) {
	doc, err := policy.Compile([]policy.Subject{{ID: "c", MAC: "aa:bb:cc:dd:ee:01"}},
		policy.Setting{BlockCloudVideo: true}, policy.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := MatrixFromPolicy(doc, ProbeTargets{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"control-tcp-8883": true, "stream_setup-tcp-443": true, "dns-udp-53": true, "dns-tcp-53": true,
		"other-https-tcp-443": false, "relay-udp-3478": false, "relay-udp-19302": false, "arbitrary-tcp-9999": false,
	}
	if len(m) != len(want) {
		t.Fatalf("matrix = %+v", m)
	}
	for _, p := range m {
		exp, ok := want[p.Name]
		if !ok || exp != p.ExpectedWhenBlocking {
			t.Errorf("probe %s expected=%v", p.Name, p.ExpectedWhenBlocking)
		}
	}
	off, _ := policy.Compile([]policy.Subject{{ID: "c", MAC: "aa:bb:cc:dd:ee:01"}}, policy.Setting{}, policy.Options{})
	if _, err := MatrixFromPolicy(off, ProbeTargets{}); err == nil {
		t.Fatal("a No policy has nothing to verify")
	}
}

func TestRunVerificationDifferential(t *testing.T) {
	var modes []bool
	blocking := false
	p := &mockProber{
		applyModeFunc: func(_ context.Context, b bool) error { modes = append(modes, b); blocking = b; return nil },
		execProbeFunc: func(_ context.Context, pr NetworkProbe, _ bool) bool { return !blocking || pr.ExpectedWhenBlocking },
	}
	v := NewVerifier(Config{CameraID: "c", CameraMAC: "aa:bb:cc:dd:ee:01"}, p, NewMetrics())
	verdict, err := v.RunVerification(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Status != StatusEnforcedProven || !verdict.BlockCloudVideo {
		t.Fatalf("verdict = %+v", verdict)
	}
	if len(modes) != 2 || modes[0] || !modes[1] {
		t.Fatalf("expected off then on, got %v", modes)
	}

	// A leak in the test leg is NOT_ENFORCED.
	p.execProbeFunc = func(_ context.Context, pr NetworkProbe, _ bool) bool { return true }
	verdict, _ = v.RunVerification(context.Background())
	if verdict.Status != StatusNotEnforced {
		t.Fatalf("leak not detected: %s", verdict.Status)
	}

	// A broken network is PRECONDITION_FAILED and blocking is put back on.
	modes = nil
	p.execProbeFunc = func(_ context.Context, pr NetworkProbe, _ bool) bool { return false }
	verdict, _ = v.RunVerification(context.Background())
	if verdict.Status != StatusPreconditionFailed || modes[len(modes)-1] != true {
		t.Fatalf("broken network: status=%s modes=%v", verdict.Status, modes)
	}
}
