package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

// Guessing the router's address for the Firewall's Connect router form. The
// router that matters is the one the cameras are on, which is not always this
// PC's default gateway: a travel router plugged into the main router, with the
// cameras on its Wi-Fi, is a common setup, and a PC wired to the main router
// reaches the internet through that one instead. It is only a suggestion.

// routerSuggestion is the prefilled address and where it came from:
// "last" (the router BombeCam last connected to), "cameras" (the router on
// the cameras' network), "default_gateway" (this PC's default gateway) or
// "guess".
type routerSuggestion struct {
	Address string `json:"address"`
	Source  string `json:"source"`
}

// defaultRoute is one IPv4 default route of this PC.
type defaultRoute struct {
	Gateway net.IP
	Metric  int
}

// suggestRouterAddress looks at this PC's networks and routes, the cameras'
// addresses and the last router used.
func suggestRouterAddress(last string, cameraIPs []string) routerSuggestion {
	s := pickRouterAddress(last, cameraIPs, localIPv4Nets(), defaultRoutesIPv4())
	if s.Address != "" {
		return s
	}
	if ip := net.ParseIP(primaryLANIPv4()).To4(); ip != nil {
		return routerSuggestion{fmt.Sprintf("%d.%d.%d.1", ip[0], ip[1], ip[2]), "guess"}
	}
	return routerSuggestion{"192.168.8.1", "guess"}
}

// pickRouterAddress is suggestRouterAddress without the system lookups.
func pickRouterAddress(last string, cameraIPs []string, locals []*net.IPNet, routes []defaultRoute) routerSuggestion {
	if last = stripDefaultSSHPort(last); last != "" {
		return routerSuggestion{last, "last"}
	}
	sort.SliceStable(routes, func(i, j int) bool { return routes[i].Metric < routes[j].Metric })

	// The router on the cameras' network, by number of cameras behind it.
	votes := map[string]int{}
	best := ""
	ips := append([]string(nil), cameraIPs...)
	sort.Strings(ips)
	for _, s := range ips {
		ip := net.ParseIP(strings.TrimSpace(s)).To4()
		if ip == nil || !ip.IsPrivate() {
			continue
		}
		cand := routerForCamera(ip, locals, routes)
		if cand == "" {
			continue
		}
		votes[cand]++
		if best == "" || votes[cand] > votes[best] {
			best = cand
		}
	}
	if best != "" {
		return routerSuggestion{best, "cameras"}
	}
	if len(routes) > 0 {
		return routerSuggestion{routes[0].Gateway.String(), "default_gateway"}
	}
	return routerSuggestion{}
}

// routerForCamera returns the likely router for one camera: a gateway on the
// PC network the camera is on, or else the first address of that network
// (where home routers sit); for a camera outside this PC's networks, the .1
// of its /24.
func routerForCamera(ip net.IP, locals []*net.IPNet, routes []defaultRoute) string {
	for _, n := range locals {
		if !n.Contains(ip) {
			continue
		}
		for _, r := range routes {
			if n.Contains(r.Gateway) {
				return r.Gateway.String()
			}
		}
		base := n.IP.Mask(n.Mask).To4()
		if base == nil {
			continue
		}
		first := net.IPv4(base[0], base[1], base[2], base[3]+1).To4()
		if !first.Equal(ip) {
			return first.String()
		}
	}
	return fmt.Sprintf("%d.%d.%d.1", ip[0], ip[1], ip[2])
}

func stripDefaultSSHPort(addr string) string {
	addr = strings.TrimSpace(addr)
	if host, port, err := net.SplitHostPort(addr); err == nil && port == "22" {
		return host
	}
	return addr
}

// localIPv4Nets lists this PC's private IPv4 networks.
func localIPv4Nets() []*net.IPNet {
	var out []*net.IPNet
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && ipn.IP.IsPrivate() {
				out = append(out, ipn)
			}
		}
	}
	return out
}

// defaultRoutesIPv4 lists this PC's IPv4 default routes.
func defaultRoutesIPv4() []defaultRoute {
	switch runtime.GOOS {
	case "linux":
		f, err := os.Open("/proc/net/route")
		if err != nil {
			return nil
		}
		defer f.Close()
		return parseLinuxDefaultRoutes(f)
	case "windows":
		cmd := exec.Command("route", "print", "-4", "0.0.0.0")
		hideConsole(cmd)
		out, err := cmd.Output()
		if err != nil {
			return nil
		}
		return parseWindowsDefaultRoutes(string(out))
	}
	return nil
}

// parseLinuxDefaultRoutes reads /proc/net/route, whose fields are Iface,
// Destination, Gateway, Flags, RefCnt, Use, Metric, ... in little-endian hex.
func parseLinuxDefaultRoutes(r io.Reader) []defaultRoute {
	var out []defaultRoute
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 7 || f[1] != "00000000" || f[2] == "00000000" {
			continue
		}
		var b [4]byte
		if _, err := fmt.Sscanf(f[2], "%02x%02x%02x%02x", &b[3], &b[2], &b[1], &b[0]); err != nil {
			continue
		}
		metric := 0
		_, _ = fmt.Sscanf(f[6], "%d", &metric)
		out = append(out, defaultRoute{Gateway: net.IP(b[:]).To4(), Metric: metric})
	}
	return out
}

// parseWindowsDefaultRoutes reads `route print -4 0.0.0.0` output, whose
// default route lines are "0.0.0.0  0.0.0.0  <gateway>  <interface>  <metric>".
func parseWindowsDefaultRoutes(out string) []defaultRoute {
	var routes []defaultRoute
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 5 && f[0] == "0.0.0.0" && f[1] == "0.0.0.0" {
			if ip := net.ParseIP(f[2]).To4(); ip != nil {
				metric := 0
				_, _ = fmt.Sscanf(f[4], "%d", &metric)
				routes = append(routes, defaultRoute{Gateway: ip, Metric: metric})
			}
		}
	}
	return routes
}

// parseWindowsDefaultGateway returns the lowest-metric default gateway.
func parseWindowsDefaultGateway(out string) string {
	routes := parseWindowsDefaultRoutes(out)
	sort.SliceStable(routes, func(i, j int) bool { return routes[i].Metric < routes[j].Metric })
	if len(routes) == 0 {
		return ""
	}
	return routes[0].Gateway.String()
}
