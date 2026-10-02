package verifier

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Metrics records Block cloud video telemetry for Prometheus export.
type Metrics struct {
	mu                       sync.RWMutex
	EnforcementPointVerified map[string]float64 // camera -> timestamp
	BlockCloudVideo          float64
	PolicyBundleVersion      string
	PolicyBundleAgeSeconds   float64
	BlockedPacketsTotal      map[string]map[string]uint64 // camera -> destination_class -> count
	AllowedBytesTotal        map[string]map[string]uint64 // camera -> destination_class -> bytes
	CameraReachable          map[string]float64           // camera -> 0 or 1
	CameraStreaming          map[string]float64           // camera -> 0 or 1
	LastVerifiedTimestamp    time.Time
}

// NewMetrics initializes the metrics registry
func NewMetrics() *Metrics {
	return &Metrics{
		EnforcementPointVerified: make(map[string]float64),
		BlockedPacketsTotal:      make(map[string]map[string]uint64),
		AllowedBytesTotal:        make(map[string]map[string]uint64),
		CameraReachable:          make(map[string]float64),
		CameraStreaming:          make(map[string]float64),
		PolicyBundleVersion:      "5.0.0",
	}
}

// RecordVerification updates verified metrics
func (m *Metrics) RecordVerification(cameraID string, status VerdictStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := float64(time.Now().Unix())
	if status == StatusEnforcedProven || status == StatusEnforcedObserved {
		m.EnforcementPointVerified[cameraID] = now
		m.LastVerifiedTimestamp = time.Now()
	} else {
		m.EnforcementPointVerified[cameraID] = 0
	}
}

// SetBlockCloudVideo records whether "Block cloud video" is on.
func (m *Metrics) SetBlockCloudVideo(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.BlockCloudVideo = 0
	if on {
		m.BlockCloudVideo = 1
	}
}

// RecordBlockedPacket increments blocked packet counter for a destination class
func (m *Metrics) RecordBlockedPacket(cameraID, destClass string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.BlockedPacketsTotal[cameraID]; !ok {
		m.BlockedPacketsTotal[cameraID] = make(map[string]uint64)
	}
	m.BlockedPacketsTotal[cameraID][destClass]++
}

// RecordAllowedBytes records allowed bytes for a destination class
func (m *Metrics) RecordAllowedBytes(cameraID, destClass string, bytes uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.AllowedBytesTotal[cameraID]; !ok {
		m.AllowedBytesTotal[cameraID] = make(map[string]uint64)
	}
	m.AllowedBytesTotal[cameraID][destClass] += bytes
}

// SetCameraStatus sets reachability and streaming state
func (m *Metrics) SetCameraStatus(cameraID string, reachable, streaming bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if reachable {
		m.CameraReachable[cameraID] = 1.0
	} else {
		m.CameraReachable[cameraID] = 0.0
	}

	if streaming {
		m.CameraStreaming[cameraID] = 1.0
	} else {
		m.CameraStreaming[cameraID] = 0.0
	}
}

// Global metrics singleton
var DefaultMetrics = NewMetrics()

// -----------------------------------------------------------------------------
// Prometheus rendering. The state itself is
// a metric, the timestamp is zero unless a protective verdict is live, and the
// destination-class counters — including the `unknown` class whose growth is
// the alarm — are present.
// -----------------------------------------------------------------------------

// DestinationClasses are the buckets egress is attributed to. `unknown` climbing
// is the signal that the camera is reaching somewhere nobody has classified.
var DestinationClasses = []string{
	"control",
	"stream_setup",
	"dns",
	"time",
	"other",
}

// SetCameraHealth records whether the camera is reachable and streaming, so a
// setting change that breaks a camera is attributable rather than mysterious.
func (m *Metrics) SetCameraHealth(cameraID string, reachable, streaming bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := func(v bool) float64 {
		if v {
			return 1
		}
		return 0
	}
	m.CameraReachable[cameraID] = b(reachable)
	m.CameraStreaming[cameraID] = b(streaming)
}

// SetPolicyBundle records the policy version in force and how old it is.
func (m *Metrics) SetPolicyBundle(version string, builtAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PolicyBundleVersion = version
	if !builtAt.IsZero() {
		m.PolicyBundleAgeSeconds = time.Since(builtAt).Seconds()
	}
}

// Render emits the full metric set in Prometheus text format. `current` is the
// live verdict; passing nil renders the non-protective defaults, which is the
// correct output for an appliance that has never verified.
func (m *Metrics) Render(current *Verdict) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var b strings.Builder
	w := func(f string, a ...interface{}) { b.WriteString(fmt.Sprintf(f, a...) + "\n") }

	cameraID, point := "unknown", "unknown"
	state := StatusUnknown
	var verifiedAt int64
	if current != nil {
		if current.CameraID != "" {
			cameraID = current.CameraID
		}
		if current.EnforcementPoint != "" {
			point = current.EnforcementPoint
		}
		state = current.EffectiveStatus()
		if state == StatusEnforcedProven || state == StatusEnforcedObserved {
			verifiedAt = current.VerifiedAt.Unix()
		}
	}

	w("# HELP bombecam_enforcement_point_verified Unix time of the last verdict that established protection; 0 when unprotected or unverified.")
	w("# TYPE bombecam_enforcement_point_verified gauge")
	w("bombecam_enforcement_point_verified{camera=%q,point=%q} %d", cameraID, point, verifiedAt)

	w("# HELP bombecam_enforcement_point_state Current enforcement state, one-hot.")
	w("# TYPE bombecam_enforcement_point_state gauge")
	for _, s := range []VerdictStatus{
		StatusEnforcedProven, StatusEnforcedObserved,
		StatusNotEnforced, StatusPreconditionFailed, StatusUnknown,
	} {
		v := 0
		if s == state {
			v = 1
		}
		w("bombecam_enforcement_point_state{camera=%q,state=%q} %d", cameraID, s, v)
	}

	w("# HELP bombecam_block_cloud_video 1 when \"Block cloud video\" is on, 0 when off.")
	w("# TYPE bombecam_block_cloud_video gauge")
	on := m.BlockCloudVideo
	if current != nil {
		on = 0
		if current.BlockCloudVideo {
			on = 1
		}
	}
	w("bombecam_block_cloud_video{camera=%q} %g", cameraID, on)

	w("# HELP bombecam_policy_bundle_age_seconds Age of the policy in force.")
	w("# TYPE bombecam_policy_bundle_age_seconds gauge")
	w("bombecam_policy_bundle_age_seconds %g", m.PolicyBundleAgeSeconds)
	w("# HELP bombecam_policy_bundle_info Version of the policy in force.")
	w("# TYPE bombecam_policy_bundle_info gauge")
	w("bombecam_policy_bundle_info{version=%q} 1", m.PolicyBundleVersion)

	w("# HELP bombecam_blocked_packets_total Packets dropped, by destination class.")
	w("# TYPE bombecam_blocked_packets_total counter")
	w("# HELP bombecam_allowed_bytes_total Bytes permitted, by destination class. With blocking on, \"other\" must stay at 0.")
	w("# TYPE bombecam_allowed_bytes_total counter")
	cams := map[string]bool{cameraID: true}
	for c := range m.BlockedPacketsTotal {
		cams[c] = true
	}
	for c := range m.AllowedBytesTotal {
		cams[c] = true
	}
	names := make([]string, 0, len(cams))
	for c := range cams {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		for _, class := range DestinationClasses {
			w("bombecam_blocked_packets_total{camera=%q,destination_class=%q} %d",
				c, class, m.BlockedPacketsTotal[c][class])
		}
	}
	for _, c := range names {
		for _, class := range DestinationClasses {
			w("bombecam_allowed_bytes_total{camera=%q,destination_class=%q} %d",
				c, class, m.AllowedBytesTotal[c][class])
		}
	}

	w("# HELP camera_reachable Whether the camera answers on the camera segment.")
	w("# TYPE camera_reachable gauge")
	w("# HELP camera_streaming Whether the camera's local stream is flowing.")
	w("# TYPE camera_streaming gauge")
	for _, c := range names {
		w("camera_reachable{camera=%q} %g", c, m.CameraReachable[c])
		w("camera_streaming{camera=%q} %g", c, m.CameraStreaming[c])
	}
	return b.String()
}
