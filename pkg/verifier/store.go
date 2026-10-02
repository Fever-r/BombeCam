package verifier

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultVerdictPath is where the appliance keeps its last verdict. It is read
// by the control plane's status endpoint, so a verdict that is never written
// leaves that endpoint reporting UNKNOWN.
const DefaultVerdictPath = "/var/lib/bombecam/verdict.json"

// SaveVerdict writes a verdict durably: temp file, fsync, rename, fsync dir.
// A verdict is always stored under its EFFECTIVE status, so a stale verdict can
// never be read back as proven.
func SaveVerdict(path string, v *Verdict) error {
	if v == nil {
		return fmt.Errorf("refusing to persist a nil verdict")
	}
	out := *v
	out.Status = v.EffectiveStatus()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(&out, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".verdict-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	// fsync the directory so the rename itself is durable across a power cut.
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// LoadVerdict reads the stored verdict and applies perishability on the way
// out. Anything missing, unreadable, unparseable or expired comes back as a
// non-protective verdict — never as an absence the caller might mistake for
// "fine".
func LoadVerdict(path string) *Verdict {
	now := time.Now().UTC()
	missing := func(reason string) *Verdict {
		return &Verdict{
			Status:     StatusUnknown,
			VerifiedAt: time.Time{},
			ExpiresAt:  now,
			Details:    VerdictDetails{FailureReason: reason},
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return missing("no verification has been recorded on this appliance")
	}
	var v Verdict
	if err := json.Unmarshal(b, &v); err != nil {
		return missing("the stored verdict is unreadable")
	}
	eff := v.EffectiveStatus()
	if eff != v.Status {
		v.Details.FailureReason = "the stored verdict has expired; enforcement has not been re-checked"
	}
	v.Status = eff
	return &v
}
