package keenetic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// IfaceMapOptions configures FetchIfaceMap. Defaults work on KeeneticOS.
type IfaceMapOptions struct {
	AwgManagerURL string        // default "http://127.0.0.1:2222"
	Client        *http.Client  // default 5s-timeout client
	Timeout       time.Duration // default 5s
}

type tunnelEntry struct {
	NDMSName      string `json:"ndmsName"`
	InterfaceName string `json:"interfaceName"`
}

type tunnelsResp struct {
	Success bool `json:"success"`
	Data    struct {
		Tunnels []tunnelEntry `json:"tunnels"`
	} `json:"data"`
}

// FetchIfaceMap returns a map from NDMSName (e.g. "Wireguard0") to the Linux
// interface name (e.g. "nwg0") for every active WG tunnel managed by
// awg-manager. Non-tunnel interfaces (Home, ISP, GigabitEthernet1) are not in
// the map — DNS endpoints bound to them go through Keenetic native routing,
// outside our agent's iface-bound dialer.
func FetchIfaceMap(ctx context.Context, opts IfaceMapOptions) (map[string]string, error) {
	url := opts.AwgManagerURL
	if url == "" {
		url = "http://127.0.0.1:2222"
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url+"/api/tunnels/all", nil)
	if err != nil {
		return nil, fmt.Errorf("awg-manager request: %w", err)
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("awg-manager: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("awg-manager: status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	var out tunnelsResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("awg-manager: decode: %w", err)
	}
	if !out.Success {
		return nil, errors.New("awg-manager: success=false")
	}
	m := make(map[string]string, len(out.Data.Tunnels))
	for _, t := range out.Data.Tunnels {
		if t.NDMSName != "" && t.InterfaceName != "" {
			m[t.NDMSName] = t.InterfaceName
		}
	}
	return m, nil
}
