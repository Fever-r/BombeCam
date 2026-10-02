package verifier

import (
	"encoding/json"
	"fmt"
	"time"
)

// ControlPathType indicates how camera commands travel (informational).
type ControlPathType string

const (
	ControlPathLocal       ControlPathType = "local"
	ControlPathLocalDirect ControlPathType = "local_direct"
	ControlPathVendorCloud ControlPathType = "vendor_cloud"
	ControlPathNone        ControlPathType = "none"
)

// VerdictStatus represents the runtime enforcement verdict.
type VerdictStatus string

const (
	StatusEnforcedProven   VerdictStatus = "ENFORCED_PROVEN"   // Differential canary passed with L2 attribution & counter increment
	StatusEnforcedObserved VerdictStatus = "ENFORCED_OBSERVED" // Provoke-and-observe or counter attestation without canary
	StatusNotEnforced      VerdictStatus = "NOT_ENFORCED"      // Traffic reaches WAN without traversing hook or test leg failed
	StatusUnknown          VerdictStatus = "UNKNOWN"           // Expired, unverified, reboot, or network change
	// StatusPreconditionFailed means verification could not be PERFORMED —
	// the camera was unreachable, no NAT return path existed, or the
	// blocking-off control leg failed. It is a network fault, not a statement about the
	// firewall, and must never be reported as one. It is rendered to
	// users in exactly the same words as NOT_ENFORCED.
	StatusPreconditionFailed VerdictStatus = "PRECONDITION_FAILED"
)

// HumanReadable returns the user-facing explanation.
// Non-protective statuses (NOT_ENFORCED, UNKNOWN, PRECONDITION_FAILED)
// render identically to prevent laundering unknown states into benign diagnostics,
// while avoiding false leak accusations when verification has not been performed.
func (s VerdictStatus) HumanReadable() string {
	switch s {
	case StatusEnforcedProven:
		return "Verified: this camera's traffic is limited to its allowlist on this router."
	case StatusEnforcedObserved:
		return "Blocking was observed for test traffic; this camera's own path was not confirmed."
	default:
		return "Blocking is not in place or could not be verified."
	}
}

// VerdictDetails provides fine-grained audit breakdown of the verification check
type VerdictDetails struct {
	PreconditionPassed bool   `json:"precondition_passed"` // NAT exists, camera adjacent, canary holds lease
	ControlLegPassed   bool   `json:"control_leg_passed"`  // blocking off: every probe succeeds
	TestLegPassed      bool   `json:"test_leg_passed"`     // blocking on: every probe matches the policy
	L2Attributed       bool   `json:"l2_attributed"`       // Camera source MAC matches and TTL matches learned TTL
	CounterIncremented bool   `json:"counter_incremented"` // Deny rule packet/byte counter increased
	LearnedTTL         int    `json:"learned_ttl"`
	FailureReason      string `json:"failure_reason,omitempty"`
}

// Verdict is the strongly-typed perishable verdict schema exported via /api/v1/firewall/status
type Verdict struct {
	Status           VerdictStatus   `json:"status"`
	ControlPath      ControlPathType `json:"control_path"`
	BlockCloudVideo  bool            `json:"block_cloud_video"`
	VerifiedAt       time.Time       `json:"verified_at"`
	ExpiresAt        time.Time       `json:"expires_at"`
	CameraID         string          `json:"camera_id"`
	CameraMAC        string          `json:"camera_mac"`
	EnforcementPoint string          `json:"enforcement_point"` // "nftables", "openwrt", "consumer_router"
	Details          VerdictDetails  `json:"details"`
}

// EffectiveControlPath returns the control path, defaulting uninitialized to "none"
func (v *Verdict) EffectiveControlPath() ControlPathType {
	if v == nil || v.ControlPath == "" {
		return ControlPathNone
	}
	return v.ControlPath
}

// IsExpired checks whether this verdict has decayed
func (v *Verdict) IsExpired() bool {
	if v == nil {
		return true
	}
	return time.Now().UTC().After(v.ExpiresAt)
}

// EffectiveStatus returns the decaying verdict status (decays to UNKNOWN when
// expired). Vendor-cloud controls are expected with "Block cloud video" (the
// control connection is on the allowlist), so the control path does not
// change the status.
func (v *Verdict) EffectiveStatus() VerdictStatus {
	if v == nil || v.IsExpired() {
		return StatusUnknown
	}
	return v.Status
}

// Validate checks the verdict is well formed.
func (v *Verdict) Validate() error {
	if v == nil {
		return fmt.Errorf("nil verdict")
	}
	if v.Status == "" {
		return fmt.Errorf("verdict has no status")
	}
	return nil
}

// MarshalJSON serialises a verdict under its EFFECTIVE status and always
// carries the user-facing sentence and control_path with it.
func (v Verdict) MarshalJSON() ([]byte, error) {
	type alias Verdict // avoid recursing into this method
	eff := (&v).EffectiveStatus()
	cp := (&v).EffectiveControlPath()
	out := struct {
		alias
		Status        VerdictStatus   `json:"status"`
		ControlPath   ControlPathType `json:"control_path"`
		HumanReadable string          `json:"human_readable"`
		Protected     bool            `json:"protected"`
	}{
		alias:         alias(v),
		Status:        eff,
		ControlPath:   cp,
		HumanReadable: eff.HumanReadable(),
		Protected:     eff == StatusEnforcedProven || eff == StatusEnforcedObserved,
	}
	return json.Marshal(out)
}

// UnmarshalJSON deserializes a verdict, defaulting an empty control_path to "none".
func (v *Verdict) UnmarshalJSON(data []byte) error {
	type alias Verdict
	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*v = Verdict(raw)
	if v.ControlPath == "" {
		v.ControlPath = ControlPathNone
	}
	return nil
}
