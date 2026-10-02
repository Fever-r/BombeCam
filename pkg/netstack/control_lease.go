package netstack

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Fever-r/BombeCam/pkg/policy"
	"os/exec"
	"time"
)

func (m *RulesetManager) RenderTemporaryControl(mac, iface string, ttl time.Duration) (string, error) {
	doc, err := m.Document(true)
	if err != nil {
		return "", err
	}
	m.mu.RLock()
	resolved := m.resolvedLocked()
	m.mu.RUnlock()
	return m.renderer.CompileControlLease(doc, resolved, mac, iface, ttl)
}

func (m *RulesetManager) ApplyTemporaryControl(ctx context.Context, mac, iface string, ttl time.Duration) error {
	rules, err := m.RenderTemporaryControl(mac, iface, ttl)
	if err != nil {
		return err
	}
	if err := m.applyRendered(ctx, rules, true); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "nft", "-j", "list", "set", "inet", "bombecam", "verification_canary").CombinedOutput()
	if err != nil {
		return fmt.Errorf("cannot read back canary lease: %w (%s)", err, out)
	}
	return validateCanaryLease(out, policy.CanonicalMAC(mac), ttl)
}

func validateCanaryLease(raw []byte, mac string, ttl time.Duration) error {
	var doc struct {
		Nftables []struct {
			Set struct {
				Family, Table, Name, Type string
				Flags                     []string
				Elem                      []struct {
					Elem struct {
						Val              string
						Timeout, Expires int64
					}
				}
			} `json:"set"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("invalid canary lease readback: %w", err)
	}
	for _, item := range doc.Nftables {
		set := item.Set
		if set.Family != "inet" || set.Table != "bombecam" || set.Name != "verification_canary" || set.Type != "ether_addr" {
			continue
		}
		timed := false
		for _, flag := range set.Flags {
			if flag == "timeout" {
				timed = true
			}
		}
		if !timed || len(set.Elem) != 1 {
			break
		}
		elem := set.Elem[0].Elem
		if elem.Val == mac && elem.Timeout > 0 && elem.Timeout <= int64(ttl/time.Second) && elem.Expires > 0 && elem.Expires <= elem.Timeout {
			return nil
		}
	}
	return fmt.Errorf("kernel did not confirm a bounded, expiring canary-only lease")
}
