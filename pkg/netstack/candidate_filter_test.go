package netstack

import (
	"net"
	"testing"
)

func TestParseCandidate(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantType  string
		wantAddr  string
		wantPort  int
		shouldErr bool
	}{
		{
			name:      "Standard RFC 5245 host candidate",
			input:     "candidate:842163049 1 udp 1677729535 192.168.1.150 54321 typ host generation 0",
			wantType:  "host",
			wantAddr:  "192.168.1.150",
			wantPort:  54321,
			shouldErr: false,
		},
		{
			name:      "a= prefixed SDP line",
			input:     "a=candidate:42349582 1 udp 2130706431 10.10.5.20 50000 typ host",
			wantType:  "host",
			wantAddr:  "10.10.5.20",
			wantPort:  50000,
			shouldErr: false,
		},
		{
			name:      "Server reflexive candidate",
			input:     "candidate:11223344 1 udp 16777215 203.0.113.1 54321 typ srflx raddr 192.168.1.150 rport 54321",
			wantType:  "srflx",
			wantAddr:  "203.0.113.1",
			wantPort:  54321,
			shouldErr: false,
		},
		{
			name:      "Relay candidate",
			input:     "candidate:99887766 1 udp 1000 198.51.100.5 3478 typ relay",
			wantType:  "relay",
			wantAddr:  "198.51.100.5",
			wantPort:  3478,
			shouldErr: false,
		},
		{
			name:      "Direct bare IPv4 address",
			input:     "192.168.1.150",
			wantType:  "host",
			wantAddr:  "192.168.1.150",
			wantPort:  0,
			shouldErr: false,
		},
		{
			name:      "Malformed line",
			input:     "candidate:invalid only three tokens",
			shouldErr: true,
		},
		{
			name:      "Empty line",
			input:     "",
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cand, err := ParseCandidate(tt.input)
			if tt.shouldErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cand.Type != tt.wantType {
				t.Errorf("expected type %s, got %s", tt.wantType, cand.Type)
			}
			if cand.Address != tt.wantAddr {
				t.Errorf("expected address %s, got %s", tt.wantAddr, cand.Address)
			}
			if tt.wantPort != 0 && cand.Port != tt.wantPort {
				t.Errorf("expected port %d, got %d", tt.wantPort, cand.Port)
			}
		})
	}
}

func TestICECandidateFilter_ExplicitSubnets(t *testing.T) {
	filter, err := NewICECandidateFilter([]string{"192.168.1.0/24", "10.0.0.0/16"}, nil)
	if err != nil {
		t.Fatalf("NewICECandidateFilter failed: %v", err)
	}

	// Permitted: 192.168.1.150
	cand1 := "candidate:1 1 udp 1000 192.168.1.150 5000 typ host"
	ip, ok := filter.ValidateCandidate(cand1)
	if !ok || ip == nil || ip.String() != "192.168.1.150" {
		t.Errorf("expected 192.168.1.150 to be valid, got ip=%v, ok=%v", ip, ok)
	}

	// Permitted: 10.0.50.2
	cand2 := "candidate:2 1 udp 1000 10.0.50.2 5000 typ host"
	ip, ok = filter.ValidateCandidate(cand2)
	if !ok || ip == nil || ip.String() != "10.0.50.2" {
		t.Errorf("expected 10.0.50.2 to be valid, got ip=%v, ok=%v", ip, ok)
	}

	// Rejected: 172.16.0.10 (RFC 1918 but not in explicit subnets)
	cand3 := "candidate:3 1 udp 1000 172.16.0.10 5000 typ host"
	_, ok = filter.ValidateCandidate(cand3)
	if ok {
		t.Errorf("expected 172.16.0.10 to be rejected because not in explicit subnets")
	}

	// Rejected: Srflx candidate even if in subnet
	cand4 := "candidate:4 1 udp 1000 192.168.1.150 5000 typ srflx"
	_, ok = filter.ValidateCandidate(cand4)
	if ok {
		t.Errorf("expected srflx candidate to be rejected")
	}

	// Rejected: Loopback
	candLoopback := "candidate:5 1 udp 1000 127.0.0.1 5000 typ host"
	_, ok = filter.ValidateCandidate(candLoopback)
	if ok {
		t.Errorf("expected loopback candidate to be rejected")
	}

	// Rejected: Link-local (169.254.1.1)
	candLinkLocal := "candidate:6 1 udp 1000 169.254.1.1 5000 typ host"
	_, ok = filter.ValidateCandidate(candLinkLocal)
	if ok {
		t.Errorf("expected link-local candidate to be rejected")
	}

	// Rejected: Public WAN IP (8.8.8.8)
	candPublic := "candidate:7 1 udp 1000 8.8.8.8 5000 typ host"
	_, ok = filter.ValidateCandidate(candPublic)
	if ok {
		t.Errorf("expected public candidate to be rejected")
	}
}

func TestICECandidateFilter_DynamicDiscovery(t *testing.T) {
	_, sub1, _ := net.ParseCIDR("172.20.10.0/24")
	mock := NewMockDiscovery([]NetworkInterface{
		{
			Name:       "eth1",
			Subnets:    []*net.IPNet{sub1},
			IsUp:       true,
			IsLoopback: false,
		},
	})

	filter, err := NewICECandidateFilter(nil, mock)
	if err != nil {
		t.Fatalf("failed to create filter: %v", err)
	}

	// Candidate in auto-detected subnet
	cand1 := "candidate:1 1 udp 1000 172.20.10.55 5000 typ host"
	ip, ok := filter.ValidateCandidate(cand1)
	if !ok || ip.String() != "172.20.10.55" {
		t.Errorf("expected 172.20.10.55 to be valid via discovery, got ip=%v, ok=%v", ip, ok)
	}

	// Update allowed subnets dynamically
	err = filter.SetAllowedSubnets([]string{"10.200.0.0/16"})
	if err != nil {
		t.Fatalf("SetAllowedSubnets failed: %v", err)
	}

	// Now 172.20.10.55 should be rejected, and 10.200.1.2 accepted
	_, ok = filter.ValidateCandidate(cand1)
	if ok {
		t.Errorf("expected 172.20.10.55 to be rejected after explicit subnet change")
	}

	cand2 := "candidate:2 1 udp 1000 10.200.1.2 5000 typ host"
	ip, ok = filter.ValidateCandidate(cand2)
	if !ok || ip.String() != "10.200.1.2" {
		t.Errorf("expected 10.200.1.2 to be accepted, got ip=%v, ok=%v", ip, ok)
	}
}
