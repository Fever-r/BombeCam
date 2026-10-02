package verifier

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/Fever-r/BombeCam/pkg/policy"
)

// ProbeTargets are the addresses the canary aims at. Allowlisted hosts are
// probed by name (so the probe goes where the camera would go); the blocked
// probes need concrete stand-ins that are NOT on the allowlist.
type ProbeTargets struct {
	OtherHTTPS string // a TLS server that is not allowlisted (stands in for cloud storage)
	Relay      string // a STUN/TURN-style UDP target (stands in for relay servers)
	Arbitrary  string // any public address, probed on an unusual port
	PublicDNS  string // a public DNS server
}

// DefaultProbeTargets are public stand-ins.
func DefaultProbeTargets() ProbeTargets {
	return ProbeTargets{
		OtherHTTPS: "1.1.1.1",
		Relay:      "142.250.180.127",
		Arbitrary:  "1.1.1.1",
		PublicDNS:  "8.8.8.8",
	}
}

// MatrixFromPolicy generates the probe matrix from the compiled policy, so the
// predictions the differential checks cannot drift from the rules in force.
// Every allow rule becomes a probe expected to succeed; a fixed set of probes
// that no allow rule covers is expected to be blocked.
func MatrixFromPolicy(doc *policy.Document, targets ProbeTargets) ([]NetworkProbe, error) {
	if doc == nil {
		return nil, fmt.Errorf("nil policy document")
	}
	if !doc.BlockCloudVideo {
		return nil, fmt.Errorf("policy does not block cloud video: there is nothing to verify")
	}
	d := DefaultProbeTargets()
	if targets.OtherHTTPS == "" {
		targets.OtherHTTPS = d.OtherHTTPS
	}
	if targets.Relay == "" {
		targets.Relay = d.Relay
	}
	if targets.Arbitrary == "" {
		targets.Arbitrary = d.Arbitrary
	}
	if targets.PublicDNS == "" {
		targets.PublicDNS = d.PublicDNS
	}
	var probes []NetworkProbe
	for _, r := range doc.Allow {
		for _, proto := range r.Protocols {
			host := r.Host
			if host == "" {
				if r.Port != 53 {
					continue // NTP needs a real NTP server to answer; not probed
				}
				host = targets.PublicDNS
			}
			probes = append(probes, NetworkProbe{
				Name: fmt.Sprintf("%s-%s-%d", r.ID, proto, r.Port), TargetHost: host,
				Port: r.Port, Protocol: string(proto), ExpectedWhenBlocking: true,
			})
		}
	}
	blocked := []NetworkProbe{
		{Name: "other-https-tcp-443", TargetHost: targets.OtherHTTPS, Port: 443, Protocol: "tcp"},
		{Name: "relay-udp-3478", TargetHost: targets.Relay, Port: 3478, Protocol: "udp"},
		{Name: "relay-udp-19302", TargetHost: targets.Relay, Port: 19302, Protocol: "udp"},
		{Name: "arbitrary-tcp-9999", TargetHost: targets.Arbitrary, Port: 9999, Protocol: "tcp"},
	}
	for _, b := range blocked {
		if Permits(doc, "", b.Protocol, b.Port) {
			return nil, fmt.Errorf("probe %s was meant to be blocked but the policy allows %s/%d to any destination",
				b.Name, b.Protocol, b.Port)
		}
		probes = append(probes, b)
	}
	sort.Slice(probes, func(i, j int) bool { return probes[i].Name < probes[j].Name })
	return probes, nil
}

// Permits reports whether the policy lets a camera send proto/port traffic to
// host ("" = a destination that is not one of the allowlisted names).
func Permits(doc *policy.Document, host, proto string, port int) bool {
	if !doc.BlockCloudVideo {
		return true
	}
	for _, r := range doc.Allow {
		if r.Port != port {
			continue
		}
		okProto := false
		for _, p := range r.Protocols {
			if string(p) == proto {
				okProto = true
			}
		}
		if !okProto {
			continue
		}
		if r.Host == "" || r.Host == host {
			return true
		}
	}
	return false
}

// LoadProbeMatrix reads a matrix from JSON, for test topologies whose
// destinations are not the public internet.
func LoadProbeMatrix(path string) ([]NetworkProbe, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m []NetworkProbe
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("probe matrix %s is empty", path)
	}
	return m, nil
}
