package main

import (
	"bufio"
	"github.com/Fever-r/BombeCam/internal/winproc"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Camera MAC addresses come from this host's ARP/neighbour table: while a
// camera streams to BombeCam over the LAN, its entry is always present. The
// MAC is what router "block internet" rules and the firewall appliance need.

var (
	neighMu     sync.Mutex
	neighCache  map[string]string
	neighLoaded time.Time
	macPattern  = regexp.MustCompile(`(?i)\b([0-9a-f]{2}[:-]){5}[0-9a-f]{2}\b`)
)

// lookupMAC returns the MAC address (aa:bb:cc:dd:ee:ff) for a LAN IPv4, or "".
func lookupMAC(ip string) string {
	if net.ParseIP(ip) == nil {
		return ""
	}
	neighMu.Lock()
	defer neighMu.Unlock()
	if neighCache == nil || time.Since(neighLoaded) > 30*time.Second {
		neighCache = readNeighbours()
		neighLoaded = time.Now()
	}
	return neighCache[ip]
}

func readNeighbours() map[string]string {
	out := map[string]string{}
	add := func(ip, mac string) {
		mac = strings.ToLower(strings.ReplaceAll(mac, "-", ":"))
		if net.ParseIP(ip) == nil || mac == "00:00:00:00:00:00" || mac == "ff:ff:ff:ff:ff:ff" {
			return
		}
		out[ip] = mac
	}
	switch runtime.GOOS {
	case "linux":
		f, err := os.Open("/proc/net/arp")
		if err != nil {
			return out
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) >= 4 && macPattern.MatchString(fields[3]) {
				add(fields[0], fields[3])
			}
		}
	default:
		// Windows and macOS: `arp -a` lists "IP ... MAC" on each line.
		cmd := exec.Command("arp", "-a")
		hideConsole(cmd)
		data, err := cmd.Output()
		if err != nil {
			return out
		}
		for ip, mac := range parseArpOutput(string(data)) {
			add(ip, mac)
		}
	}
	return out
}

// parseArpOutput extracts IPv4 -> MAC pairs from `arp -a` output
// (Windows "192.168.1.1   a0-b1-c2-d3-e4-f5   dynamic", macOS "? (192.168.1.1) at a0:b1:...").
func parseArpOutput(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		mac := macPattern.FindString(line)
		if mac == "" {
			continue
		}
		for _, f := range strings.Fields(strings.NewReplacer("(", " ", ")", " ").Replace(line)) {
			if ip := net.ParseIP(f); ip != nil && ip.To4() != nil {
				out[ip.String()] = mac
				break
			}
		}
	}
	return out
}

// hideConsole keeps helper commands from flashing a window on Windows.
func hideConsole(cmd *exec.Cmd) {
	winproc.Hide(cmd)
}

// primaryLANIPv4 returns this machine's main LAN address (the one used for
// the default route), or "" if there is none. No packets are sent: a UDP
// "dial" only asks the OS which source address it would use.
func primaryLANIPv4() string {
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		addr := c.LocalAddr().(*net.UDPAddr).IP
		_ = c.Close()
		if addr != nil && !addr.IsLoopback() && addr.To4() != nil {
			return addr.String()
		}
	}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && ipn.IP.IsPrivate() {
				return ipn.IP.String()
			}
		}
	}
	return ""
}
