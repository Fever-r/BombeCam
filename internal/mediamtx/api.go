package mediamtx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ConfigPath is the subset of a MediaMTX path configuration BombeCam manages.
type ConfigPath struct {
	Name           string `json:"name"`
	Source         string `json:"source"`
	SourceOnDemand bool   `json:"sourceOnDemand"`
}

var apiClient = &http.Client{Timeout: 3 * time.Second}

func apiDo(method, u string, body any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := apiClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("mediamtx %s %s: %d %s", method, u, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// ListConfigPaths returns the configured paths (not the live ones).
func ListConfigPaths(apiBase string) ([]ConfigPath, error) {
	resp, err := apiClient.Get(strings.TrimRight(apiBase, "/") + "/v3/config/paths/list?itemsPerPage=1000")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mediamtx api returned %d", resp.StatusCode)
	}
	var list struct {
		Items []ConfigPath `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// ProxyPathConfig is the configuration of a path that relays another path of
// the same MediaMTX on demand (BombeCam's friendly stream names).
func ProxyPathConfig(source string) map[string]any {
	return map[string]any{"source": source, "sourceOnDemand": true, "rtspTransport": "tcp"}
}

// AddConfigPath adds a path configuration (MediaMTX applies it at once).
func AddConfigPath(apiBase, name string, cfg map[string]any) error {
	return apiDo(http.MethodPost, strings.TrimRight(apiBase, "/")+"/v3/config/paths/add/"+url.PathEscape(name), cfg)
}

// PatchConfigPath changes a path configuration.
func PatchConfigPath(apiBase, name string, cfg map[string]any) error {
	return apiDo(http.MethodPatch, strings.TrimRight(apiBase, "/")+"/v3/config/paths/patch/"+url.PathEscape(name), cfg)
}

// DeleteConfigPath removes a path configuration.
func DeleteConfigPath(apiBase, name string) error {
	return apiDo(http.MethodDelete, strings.TrimRight(apiBase, "/")+"/v3/config/paths/delete/"+url.PathEscape(name), nil)
}
