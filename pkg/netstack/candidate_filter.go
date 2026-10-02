package netstack

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
)

// RFC 1918 Private IPv4 Subnets
var (
	rfc1918Subnets = []*net.IPNet{
		mustParseCIDR("10.0.0.0/8"),
		mustParseCIDR("172.16.0.0/12"),
		mustParseCIDR("192.168.0.0/16"),
	}
)

func mustParseCIDR(cidr string) *net.IPNet {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return n
}

// ParsedCandidate represents the components parsed from an RFC 5245 SDP candidate string.
type ParsedCandidate struct {
	Foundation string
	Component  int
	Transport  string
	Priority   uint32
	Address    string
	Port       int
	Type       string // "host", "srflx", "prflx", "relay"
	Raw        string
}

// ICECandidateFilter validates and filters WebRTC ICE candidate addresses.
type ICECandidateFilter struct {
	mu              sync.RWMutex
	allowedSubnets  []*net.IPNet
	discovery       NetworkDiscovery
	allowAutoDetect bool
}

// NewICECandidateFilter initializes the filter with optional explicit subnets or dynamic discovery.
func NewICECandidateFilter(explicitSubnets []string, discovery NetworkDiscovery) (*ICECandidateFilter, error) {
	var subnets []*net.IPNet
	if len(explicitSubnets) > 0 {
		var err error
		subnets, err = ParseCIDRs(explicitSubnets)
		if err != nil {
			return nil, err
		}
	}

	if discovery == nil {
		discovery = GetDefaultDiscovery()
	}

	return &ICECandidateFilter{
		allowedSubnets:  subnets,
		discovery:       discovery,
		allowAutoDetect: len(subnets) == 0,
	}, nil
}

// SetAllowedSubnets updates the allowed subnets for candidate matching.
func (f *ICECandidateFilter) SetAllowedSubnets(subnets []string) error {
	parsed, err := ParseCIDRs(subnets)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allowedSubnets = parsed
	f.allowAutoDetect = len(parsed) == 0
	return nil
}

// AllowedSubnets returns the currently configured allowed subnets.
func (f *ICECandidateFilter) AllowedSubnets() []*net.IPNet {
	f.mu.RLock()
	defer f.mu.RUnlock()
	res := make([]*net.IPNet, len(f.allowedSubnets))
	copy(res, f.allowedSubnets)
	return res
}

// ParseCandidate parses an RFC 5245 SDP candidate attribute string.
// Formats supported:
// - "candidate:842163049 1 udp 1677729535 192.168.1.150 54321 typ host ..."
// - "a=candidate:842163049 1 udp 1677729535 192.168.1.150 54321 typ host ..."
// - Direct IP string: "192.168.1.150"
func ParseCandidate(sdpLine string) (*ParsedCandidate, error) {
	sdpLine = strings.TrimSpace(sdpLine)
	if sdpLine == "" {
		return nil, fmt.Errorf("empty candidate line")
	}

	// Direct IP fallback
	if directIP := net.ParseIP(sdpLine); directIP != nil {
		return &ParsedCandidate{
			Address:   directIP.String(),
			Transport: "udp",
			Type:      "host",
			Raw:       sdpLine,
		}, nil
	}

	line := sdpLine
	if strings.HasPrefix(line, "a=") {
		line = line[2:]
	}
	if strings.HasPrefix(line, "candidate:") {
		line = line[len("candidate:"):]
	} else if strings.HasPrefix(line, "candidate ") {
		line = line[len("candidate "):]
	}

	fields := strings.Fields(line)
	// RFC 5245 requires at least 8 tokens: foundation, component, transport, priority, address, port, "typ", type
	if len(fields) < 8 {
		return nil, fmt.Errorf("malformed candidate line, expected at least 8 tokens, got %d: %q", len(fields), sdpLine)
	}

	// RFC 5245 §15.1: component-id = 1*5DIGIT ; 1 to 256 inclusive
	comp, err := strconv.Atoi(fields[1])
	if err != nil || comp < 1 || comp > 256 {
		return nil, fmt.Errorf("invalid candidate component %q: must be between 1 and 256", fields[1])
	}

	transport := strings.ToLower(fields[2])
	if transport != "udp" && transport != "tcp" {
		return nil, fmt.Errorf("invalid candidate transport %q: only udp and tcp are supported", fields[2])
	}

	// RFC 5245 §15.1: priority = 1*10DIGIT ; 1 to 2**31 - 1 (2147483647) inclusive
	pri, err := strconv.ParseUint(fields[3], 10, 32)
	if err != nil || pri < 1 || pri > 2147483647 {
		return nil, fmt.Errorf("invalid candidate priority %q: must be between 1 and 2147483647", fields[3])
	}

	// RFC 5245 §15.1 & RFC 4566: port = 1*5DIGIT ; 1 to 65535 inclusive
	port, err := strconv.Atoi(fields[5])
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid candidate port %q: must be between 1 and 65535", fields[5])
	}

	// Look for "typ <cand-type>"
	candType := ""
	for i := 6; i < len(fields)-1; i++ {
		if fields[i] == "typ" {
			candType = fields[i+1]
			break
		}
	}
	if candType == "" {
		return nil, fmt.Errorf("missing candidate type in %q", sdpLine)
	}

	return &ParsedCandidate{
		Foundation: fields[0],
		Component:  comp,
		Transport:  fields[2],
		Priority:   uint32(pri),
		Address:    fields[4],
		Port:       port,
		Type:       candType,
		Raw:        sdpLine,
	}, nil
}

// IsInvalidAddress checks whether an IP is loopback, link-local, multicast, unspecified, or broadcast.
func IsInvalidAddress(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	v4 := ip.To4()
	if v4 == nil {
		return true
	}
	// Check IPv4 broadcast
	if v4[0] == 255 && v4[1] == 255 && v4[2] == 255 && v4[3] == 255 {
		return true
	}
	return false
}

// IsRFC1918PrivateIP checks if an IPv4 address is in RFC1918 space (10/8, 172.16/12, 192.168/16).
func IsRFC1918PrivateIP(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	for _, subnet := range rfc1918Subnets {
		if subnet.Contains(v4) {
			return true
		}
	}
	return false
}

// ValidateCandidate parses an SDP candidate string and returns the valid LAN host IP if permitted.
func (f *ICECandidateFilter) ValidateCandidate(candidateSDP string) (net.IP, bool) {
	cand, err := ParseCandidate(candidateSDP)
	if err != nil {
		return nil, false
	}

	// Only host candidates are permitted for direct LAN streaming
	if strings.ToLower(cand.Type) != "host" {
		return nil, false
	}

	// Direct LAN streaming requires UDP transport
	if strings.ToLower(cand.Transport) != "udp" {
		return nil, false
	}

	ip := net.ParseIP(cand.Address)
	if ip == nil {
		return nil, false
	}

	if !f.IsAllowedIP(ip) {
		return nil, false
	}

	return ip.To4(), true
}

// ValidateIP validates whether a bare IP address is a permissible camera LAN IP.
func (f *ICECandidateFilter) ValidateIP(ip net.IP) bool {
	return f.IsAllowedIP(ip)
}

// IsAllowedIP checks whether a given IP matches the explicit or auto-detected camera subnets.
func (f *ICECandidateFilter) IsAllowedIP(ip net.IP) bool {
	if ip == nil || IsInvalidAddress(ip) {
		return false
	}

	v4 := ip.To4()
	if v4 == nil {
		return false
	}

	f.mu.RLock()
	explicit := f.allowedSubnets
	autoDetect := f.allowAutoDetect
	disc := f.discovery
	f.mu.RUnlock()

	// 1. If explicit subnets are configured, match against them
	if len(explicit) > 0 {
		for _, subnet := range explicit {
			if subnet.Contains(v4) {
				return true
			}
		}
		return false
	}

	// 2. If auto-detection is allowed, check host subnets via discovery
	if autoDetect && disc != nil {
		hostSubnets, err := disc.DetectHostSubnets()
		if err == nil && len(hostSubnets) > 0 {
			for _, subnet := range hostSubnets {
				if subnet.Contains(v4) {
					return true
				}
			}
		}
	}

	// 3. Fallback: If no host subnets are detected or discovery is not available,
	// accept any valid RFC 1918 private IPv4 address (and reject public WAN IPs).
	return IsRFC1918PrivateIP(v4)
}
