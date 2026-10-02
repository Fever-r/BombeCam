package netstack

import (
	"net"
	"testing"
)

func TestHostDiscovery_ListInterfaces(t *testing.T) {
	d := NewHostDiscovery()
	ifaces, err := d.ListInterfaces()
	if err != nil {
		t.Fatalf("ListInterfaces failed: %v", err)
	}
	// We expect at least loopback or some interface on any machine
	t.Logf("Found %d interfaces", len(ifaces))
	for _, ifc := range ifaces {
		t.Logf("  Interface: %s, Up: %v, Loopback: %v, IPs: %v", ifc.Name, ifc.IsUp, ifc.IsLoopback, ifc.IPs)
	}
}

func TestMockDiscovery_SubnetsAndLookup(t *testing.T) {
	_, sub1, _ := net.ParseCIDR("10.20.30.0/24")
	_, sub2, _ := net.ParseCIDR("192.168.1.0/24")

	mockIfaces := []NetworkInterface{
		{
			Name:         "eth0",
			HardwareAddr: "00:11:22:33:44:55",
			IPs:          []net.IP{net.ParseIP("10.20.30.1")},
			Subnets:      []*net.IPNet{sub1},
			IsUp:         true,
			IsLoopback:   false,
		},
		{
			Name:         "wlan0",
			HardwareAddr: "00:11:22:33:44:66",
			IPs:          []net.IP{net.ParseIP("192.168.1.50")},
			Subnets:      []*net.IPNet{sub2},
			IsUp:         true,
			IsLoopback:   false,
		},
		{
			Name:         "lo",
			HardwareAddr: "",
			IPs:          []net.IP{net.ParseIP("127.0.0.1")},
			Subnets:      nil,
			IsUp:         true,
			IsLoopback:   true,
		},
		{
			Name:         "down0",
			HardwareAddr: "aa:bb:cc:dd:ee:ff",
			IPs:          nil,
			Subnets:      nil,
			IsUp:         false,
			IsLoopback:   false,
		},
	}

	mock := NewMockDiscovery(mockIfaces)

	subnets, err := mock.DetectHostSubnets()
	if err != nil {
		t.Fatalf("DetectHostSubnets error: %v", err)
	}
	if len(subnets) != 2 {
		t.Fatalf("expected 2 active subnets, got %d", len(subnets))
	}

	// Test IsIPInHostSubnets
	if !mock.IsIPInHostSubnets(net.ParseIP("10.20.30.15")) {
		t.Errorf("expected 10.20.30.15 to be in host subnets")
	}
	if !mock.IsIPInHostSubnets(net.ParseIP("192.168.1.200")) {
		t.Errorf("expected 192.168.1.200 to be in host subnets")
	}
	if mock.IsIPInHostSubnets(net.ParseIP("172.16.1.1")) {
		t.Errorf("expected 172.16.1.1 NOT to be in host subnets")
	}

	// Test FindInterfaceForIP
	ifc, matchedSubnet, err := mock.FindInterfaceForIP(net.ParseIP("10.20.30.99"))
	if err != nil {
		t.Fatalf("FindInterfaceForIP failed: %v", err)
	}
	if ifc.Name != "eth0" {
		t.Errorf("expected eth0, got %s", ifc.Name)
	}
	if matchedSubnet.String() != "10.20.30.0/24" {
		t.Errorf("expected 10.20.30.0/24, got %s", matchedSubnet.String())
	}

	// IP not in any interface
	_, _, err = mock.FindInterfaceForIP(net.ParseIP("8.8.8.8"))
	if err == nil {
		t.Errorf("expected error for 8.8.8.8, got nil")
	}
}

func TestParseCIDRs(t *testing.T) {
	cidrs := []string{"192.168.10.0/24", "10.0.0.0/8"}
	parsed, err := ParseCIDRs(cidrs)
	if err != nil {
		t.Fatalf("ParseCIDRs failed: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 parsed CIDRs, got %d", len(parsed))
	}

	_, err = ParseCIDRs([]string{"invalid-cidr"})
	if err == nil {
		t.Errorf("expected error for invalid-cidr")
	}
}
