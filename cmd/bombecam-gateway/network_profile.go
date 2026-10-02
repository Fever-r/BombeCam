package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Windows blocks incoming connections on networks it treats as Public, so an
// NVR on another device cannot reach mediamtx.exe there. The settings page
// warns when the network the streams are advertised on is Public.

type networkConnection struct {
	Name      string `json:"name"`
	Interface string `json:"interface"`
	Category  string `json:"category"` // Public, Private, DomainAuthenticated
}

type networkProfileView struct {
	Checked     bool                `json:"checked"` // false off Windows or if the check failed
	Public      bool                `json:"public"`  // the advertised address is on a Public network
	Connections []networkConnection `json:"connections,omitempty"`
}

var (
	netProfMu      sync.Mutex
	netProfCache   []networkConnection
	netProfChecked bool
	netProfAt      time.Time
	// netProfileFunc lists Windows' network profiles (seam for tests).
	netProfileFunc = windowsNetworkProfiles
)

// parseNetConnectionProfiles reads Get-NetConnectionProfile's JSON (an object
// or an array; NetworkCategory as a number or a name).
func parseNetConnectionProfiles(data []byte) ([]networkConnection, bool) {
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return nil, true
	}
	type raw struct {
		Name            string `json:"Name"`
		InterfaceAlias  string `json:"InterfaceAlias"`
		NetworkCategory any    `json:"NetworkCategory"`
	}
	var list []raw
	if data[0] == '{' {
		var one raw
		if err := json.Unmarshal(data, &one); err != nil {
			return nil, false
		}
		list = []raw{one}
	} else if err := json.Unmarshal(data, &list); err != nil {
		return nil, false
	}
	var out []networkConnection
	for _, r := range list {
		cat := ""
		switch v := r.NetworkCategory.(type) {
		case float64:
			cat = map[int]string{0: "Public", 1: "Private", 2: "DomainAuthenticated"}[int(v)]
		case string:
			cat = v
		}
		out = append(out, networkConnection{Name: r.Name, Interface: r.InterfaceAlias, Category: cat})
	}
	return out, true
}

func windowsNetworkProfiles() ([]networkConnection, bool) {
	if runtime.GOOS != "windows" {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Get-NetConnectionProfile | Select-Object Name,InterfaceAlias,NetworkCategory | ConvertTo-Json -Compress")
	hideConsole(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return parseNetConnectionProfiles(out)
}

// currentNetworkProfile returns the (cached, 60 s) Windows network profiles
// and whether the advertised address sits on a Public one.
func currentNetworkProfile() networkProfileView {
	netProfMu.Lock()
	if time.Since(netProfAt) > time.Minute {
		netProfMu.Unlock()
		conns, ok := netProfileFunc()
		netProfMu.Lock()
		netProfCache, netProfChecked, netProfAt = conns, ok, time.Now()
	}
	conns, ok := netProfCache, netProfChecked
	netProfMu.Unlock()

	v := networkProfileView{Checked: ok, Connections: conns}
	if !ok {
		return v
	}
	// Which interface carries the address other devices use?
	adv := ""
	if sm := currentStreamManager(); sm != nil {
		adv, _, _ = sm.AdvertisedHost(nil)
	}
	advIface := ""
	for _, a := range lanAddressesFunc() {
		if a.IP == adv {
			advIface = a.Interface
		}
	}
	for _, c := range conns {
		if !strings.EqualFold(c.Category, "Public") {
			continue
		}
		if advIface == "" || strings.EqualFold(c.Interface, advIface) {
			v.Public = true
		}
	}
	return v
}

var (
	currentSMMu sync.RWMutex
	currentSM   *StreamManager
)

func setCurrentStreamManager(sm *StreamManager) {
	currentSMMu.Lock()
	defer currentSMMu.Unlock()
	currentSM = sm
}

func currentStreamManager() *StreamManager {
	currentSMMu.RLock()
	defer currentSMMu.RUnlock()
	return currentSM
}
