// Package nftables compiles a policy.Document into an nftables ruleset for a
// Linux router (including OpenWrt fw4) that the cameras connect through.
//
// The output is one self-contained table, "inet bombecam", loaded in a single
// atomic transaction. It is idempotent: loading it replaces any previous
// BombeCam table and never touches other tables (fw4 flushes only its own
// table, so the two coexist).
//
// Shape of the ruleset when cloud video is blocked:
//
//	forward (priority filter - 5): packets from a camera MAC jump to camera_out
//	camera_out: local network -> return (untouched)
//	            allowlisted destination/port -> goto capped
//	            everything else, IPv4 and IPv6 -> drop
//	capped:     over the camera's byte-rate cap -> drop, otherwise accept
//
// Only accept, drop and rate-limit decisions are used. Nothing is redirected,
// rewritten or answered on the camera's behalf.
package nftables

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Fever-r/BombeCam/pkg/policy"
)

var ifaceRe = regexp.MustCompile(`^[A-Za-z0-9_.@:-]{1,15}$`)
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,31}$`)

var nftKeywords = map[string]bool{
	"counter": true, "counters": true, "policy": true, "device": true, "log": true,
	"meta": true, "ct": true, "accept": true, "drop": true, "reject": true,
	"limit": true, "quota": true, "snat": true, "dnat": true, "masquerade": true,
	"elements": true, "timeout": true, "flags": true, "type": true, "hook": true,
}

// NATTableName is the optional return-path NAT table for a Linux box that
// routes the camera network itself. OpenWrt routers already masquerade.
const NATTableName = "bombecam_nat"

// Config specifies target system parameters.
type Config struct {
	TableName string // default "bombecam"
	// LogBlocked adds a rate-limited log line for blocked attempts so they
	// can be inspected (logread / journalctl). Logging changes nothing.
	LogBlocked bool
	// Optional return-path NAT for a plain Linux router.
	EnableMasq   bool
	WANInterface string
	CameraSubnet string
}

// DefaultConfig returns the defaults.
func DefaultConfig() Config {
	return Config{TableName: "bombecam", LogBlocked: true}
}

// Renderer compiles policy documents.
type Renderer struct {
	cfg          Config
	warnings     []string
	controlLease *canaryLease
}

type canaryLease struct {
	mac, iface string
	seconds    int
}

// CompileControlLease keeps the blocking policy and gives only the enrolled
// canary a timed exception. Expiration is enforced by the kernel, not a timer
// in the verifier process. The caller must distinguish the canary from cameras.
func (r *Renderer) CompileControlLease(doc *policy.Document, resolved map[string][]net.IP, mac, iface string, ttl time.Duration) (string, error) {
	if doc == nil || !doc.BlockCloudVideo {
		return "", fmt.Errorf("control lease requires blocking policy")
	}
	if !ifaceRe.MatchString(iface) || ttl < time.Second || ttl > 30*time.Second || ttl%time.Second != 0 {
		return "", fmt.Errorf("invalid canary interface or lease duration (1..30 whole seconds)")
	}
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 || hw[0]&1 != 0 {
		return "", fmt.Errorf("invalid canary MAC")
	}
	mac = strings.ToLower(hw.String())
	enrolled := false
	for _, sub := range doc.Subjects {
		if policy.CanonicalMAC(sub.MAC) == mac {
			enrolled = true
		}
	}
	if !enrolled {
		return "", fmt.Errorf("canary must also be an enrolled test-leg subject")
	}
	copy := *r
	copy.controlLease = &canaryLease{mac: mac, iface: iface, seconds: int(ttl / time.Second)}
	return copy.Compile(doc, resolved)
}

// NewRenderer validates the config.
func NewRenderer(cfg Config) (*Renderer, error) {
	if cfg.TableName == "" {
		cfg.TableName = "bombecam"
	}
	if nftKeywords[cfg.TableName] || !identRe.MatchString(cfg.TableName) {
		return nil, fmt.Errorf("invalid table name %q: reserved or malformed", cfg.TableName)
	}
	if cfg.EnableMasq {
		if !ifaceRe.MatchString(cfg.WANInterface) {
			return nil, fmt.Errorf("NAT enabled but WAN interface %q is invalid", cfg.WANInterface)
		}
		ip, _, err := net.ParseCIDR(cfg.CameraSubnet)
		if err != nil || ip.To4() == nil {
			return nil, fmt.Errorf("NAT enabled but camera subnet %q is not an IPv4 CIDR", cfg.CameraSubnet)
		}
	}
	return &Renderer{cfg: cfg}, nil
}

// Warnings returns non-fatal problems found by the last Compile, such as an
// allowlisted host with no known address (its rule then matches nothing, so
// the camera fails closed for that destination).
func (r *Renderer) Warnings() []string { return r.warnings }

// SetName returns the nft set name used for an allowlisted host's addresses.
func SetName(ruleID string) string { return ruleID + "_v4" }

// Compile renders the ruleset. resolved maps each allowlisted host name to its
// current addresses; only IPv4 addresses are used because camera IPv6 is
// always blocked.
func (r *Renderer) Compile(doc *policy.Document, resolved map[string][]net.IP) (string, error) {
	if err := doc.Validate(); err != nil {
		return "", fmt.Errorf("policy document is invalid, refusing to render: %w", err)
	}
	r.warnings = nil
	tbl := r.cfg.TableName

	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }

	p("#!/usr/sbin/nft -f")
	p("# BombeCam camera firewall. Generated from policy %s; do not hand-edit.", doc.Version)
	if doc.BlockCloudVideo {
		p("# Block cloud video: YES. %s", policy.Headline)
	} else {
		p("# Block cloud video: NO. BombeCam's rules are removed; the camera works normally.")
	}
	p("")
	// Declare-then-delete makes the file a replacement inside one transaction.
	p("table inet %s", tbl)
	p("delete table inet %s", tbl)
	if r.cfg.EnableMasq {
		p("table ip %s", NATTableName)
		p("delete table ip %s", NATTableName)
	}

	if doc.BlockCloudVideo {
		p("")
		r.writeTable(&b, doc, resolved)
	}

	if r.cfg.EnableMasq {
		p("")
		p("# Return path for a Linux box that routes the camera subnet itself.")
		p("table ip %s {", NATTableName)
		p("\tchain postrouting {")
		p("\t\ttype nat hook postrouting priority srcnat; policy accept;")
		p("\t\toifname \"%s\" ip saddr %s counter masquerade", r.cfg.WANInterface, r.cfg.CameraSubnet)
		p("\t}")
		p("}")
	}
	return b.String(), nil
}

func (r *Renderer) writeTable(b *strings.Builder, doc *policy.Document, resolved map[string][]net.IP) {
	p := func(f string, a ...any) { fmt.Fprintf(b, f+"\n", a...) }
	tbl := r.cfg.TableName

	macs := make([]string, 0, len(doc.Subjects))
	for _, s := range doc.Subjects {
		macs = append(macs, policy.CanonicalMAC(s.MAC))
	}
	sort.Strings(macs)

	p("table inet %s {", tbl)
	if lease := r.controlLease; lease != nil {
		p("\tset verification_canary {")
		p("\t\ttype ether_addr")
		p("\t\tflags timeout")
		p("\t\telements = { %s timeout %ds }", lease.mac, lease.seconds)
		p("\t}")
	}
	p("\tset cameras {")
	p("\t\ttype ether_addr")
	p("\t\telements = { %s }", strings.Join(macs, ", "))
	p("\t}")
	for _, rule := range doc.Allow {
		if rule.Host == "" {
			continue
		}
		ips := v4Strings(resolved[rule.Host])
		p("\t# %s", rule.Host)
		p("\tset %s {", SetName(rule.ID))
		p("\t\ttype ipv4_addr")
		if len(ips) > 0 {
			p("\t\telements = { %s }", strings.Join(ips, ", "))
		} else {
			r.warnings = append(r.warnings, fmt.Sprintf(
				"%s has no known IPv4 address yet: %s traffic stays blocked until it resolves", rule.Host, rule.ID))
		}
		p("\t}")
	}
	p("")
	p("\t# Runs before fw4's forward chain (priority filter), so it sees every")
	p("\t# packet of a camera connection, not just the first one.")
	p("\tchain forward {")
	p("\t\ttype filter hook forward priority filter - 5; policy accept;")
	if lease := r.controlLease; lease != nil {
		p("\t\tiifname \"%s\" ether saddr @verification_canary accept comment \"temporary canary control leg\"", lease.iface)
	}
	p("\t\tether saddr @cameras jump camera_out")
	p("\t}")
	p("")
	p("\tchain camera_out {")
	p("\t\tip daddr { %s } return comment \"your network: untouched\"", strings.Join(doc.LocalCIDRs, ", "))
	for _, rule := range doc.Allow {
		for _, proto := range rule.Protocols {
			if rule.Host != "" {
				p("\t\tip daddr @%s %s dport %d counter goto capped comment \"%s\"", SetName(rule.ID), proto, rule.Port, rule.ID)
			} else {
				p("\t\tmeta nfproto ipv4 %s dport %d counter goto capped comment \"%s\"", proto, rule.Port, rule.ID)
			}
		}
	}
	if r.cfg.LogBlocked {
		p("\t\tlimit rate 10/minute burst 20 packets log prefix \"bombecam-blocked: \" level info")
	}
	p("\t\tcounter drop comment \"everything else, IPv4 and IPv6\"")
	p("\t}")
	p("")
	p("\t# One byte-rate bucket per camera, shared by every allowed destination.")
	p("\tchain capped {")
	for _, s := range doc.Subjects {
		p("\t\tether saddr %s limit rate over %d bytes/second burst %d bytes counter drop comment \"cap: %s\"",
			policy.CanonicalMAC(s.MAC), doc.CapBytesPerSecond, doc.CapBurstBytes, policy.SanitizeName(displayName(s)))
	}
	p("\t\tcounter accept comment \"allowed, under cap\"")
	p("\t}")
	p("}")
}

func displayName(s policy.Subject) string {
	if s.Name != "" {
		return s.Name
	}
	return s.ID
}

func v4Strings(ips []net.IP) []string {
	seen := map[string]bool{}
	var out []string
	for _, ip := range ips {
		v4 := ip.To4()
		if v4 == nil || v4.IsUnspecified() || v4.IsLoopback() || v4.IsPrivate() || v4.IsMulticast() {
			continue
		}
		s := v4.String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
