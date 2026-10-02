package mediamtx

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// VerifyReadPolicy reads the running server's configuration. A listening port
// or a saved preference alone is not proof of the actual authentication policy.
func (s *Supervisor) VerifyReadPolicy(ctx context.Context) error {
	cfg := s.Config()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/v3/config/global/get", cfg.APIPort), nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("stream authentication could not be verified: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stream authentication configuration returned HTTP %d", resp.StatusCode)
	}
	var actual struct {
		AuthMethod string `json:"authMethod"`
		Users      []struct {
			User        string                          `json:"user"`
			Pass        string                          `json:"pass"`
			IPs         []string                        `json:"ips"`
			Permissions []struct{ Action, Path string } `json:"permissions"`
		} `json:"authInternalUsers"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&actual); err != nil {
		return err
	}
	if actual.AuthMethod != "internal" {
		return fmt.Errorf("the running media server uses an unexpected authentication method")
	}
	expectedRead := false
	for _, user := range actual.Users {
		reads := false
		for _, permission := range user.Permissions {
			if permission.Action == "read" || permission.Action == "playback" {
				reads = true
			}
		}
		if !reads {
			continue
		}
		loopbackOnly := len(user.IPs) > 0
		for _, address := range user.IPs {
			if ip := net.ParseIP(address); ip != nil && ip.IsLoopback() {
				continue
			}
			if _, subnet, err := net.ParseCIDR(address); err == nil {
				ones, bits := subnet.Mask.Size()
				if subnet.IP.IsLoopback() && ((bits == 32 && ones >= 8) || (bits == 128 && ones == 128)) {
					continue
				}
			}
			loopbackOnly = false
		}
		if loopbackOnly {
			continue
		}
		if cfg.ReadUser != "" {
			if user.User != cfg.ReadUser || user.Pass != hashedPass(cfg.ReadPass) {
				return fmt.Errorf("the running media server does not enforce the requested stream password")
			}
		} else if user.User != "any" || strings.TrimSpace(user.Pass) != "" {
			return fmt.Errorf("the running media server's read policy differs from the requested public access")
		}
		for _, permission := range user.Permissions {
			if permission.Action == "read" && permission.Path == "" {
				expectedRead = true
			}
		}
	}
	if !expectedRead {
		return fmt.Errorf("the running media server has no matching read policy")
	}
	return nil
}
