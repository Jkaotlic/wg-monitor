// Package-internal helpers for the pingcheck_status / pingcheck_toggle
// agent actions. Status is a JSON passthrough so the backend owns the
// rendering shape; toggle is two-tiered (awg-mgr POST primary, ndmc CLI
// fallback) — see Section 3 of the design spec.
package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

// PingCheckStatusJSON returns the awg-mgr /api/pingcheck/status body
// re-serialised as a JSON envelope, enriched with ndmsName resolved from
// /api/tunnels/all. We re-marshal so the backend always sees a stable shape
// (envelope shape is owned by awg-mgr; we just pass it through).
func PingCheckStatusJSON(ctx context.Context, c *awgmgr.Client) (string, error) {
	st, err := c.PingCheckStatus(ctx)
	if err != nil {
		return "", err
	}
	// Resolve tunnel_id → ndms_name from /api/tunnels/all so the backend
	// can render toggle buttons. Failure to fetch tunnels/all is non-fatal
	// — pingcheck panel still renders, just without toggle capability.
	if all, terr := c.TunnelsAll(ctx); terr == nil {
		ndmsByID := make(map[string]string, len(all.Tunnels)+len(all.External)+len(all.System))
		for _, t := range all.Tunnels {
			ndmsByID[t.ID] = t.NDMSName
		}
		for _, t := range all.External {
			ndmsByID[t.ID] = t.NDMSName
		}
		for _, t := range all.System {
			ndmsByID[t.ID] = t.NDMSName
		}
		for i := range st.Tunnels {
			if name, ok := ndmsByID[st.Tunnels[i].TunnelID]; ok {
				st.Tunnels[i].NDMSName = name
			}
		}
	}
	b, err := json.Marshal(st)
	if err != nil {
		return "", fmt.Errorf("encode pingcheck status: %w", err)
	}
	return string(b), nil
}

// PingCheckToggle enables or disables the per-tunnel watchdog. Tries
// awg-mgr POST first (no body required — query params); on any error
// falls back to ndmc CLI. Returns aggregated err if both paths fail.
//
// tunnelID is the awg-mgr id ("awg10"); ndmsName is the Keenetic
// interface name ("Wireguard0"). Both are needed because the two
// paths address the tunnel differently.
//
// Trust boundary: ndmsName is interpolated into the ndmc command string
// unsanitised. The backend's callbacks/parse.go enforces the regex
// ^[A-Za-z0-9_-]{1,32}$ on this value before it reaches the wire (SEC-02);
// agent-level callers must preserve that validation. Same pattern as
// runner.go::tunnel_enable.
func PingCheckToggle(ctx context.Context, c *awgmgr.Client, exec ExecFunc, tunnelID, ndmsName string, enable bool) error {
	primaryErr := primaryPingCheckToggle(ctx, c, tunnelID, enable)
	if primaryErr == nil {
		return nil
	}
	// У opkg-туннелей ndms_name нет вовсе, и ndmc адресовать нечем. Сказать
	// это прямо честнее, чем звать команду с пустым именем интерфейса.
	if strings.TrimSpace(ndmsName) == "" {
		return fmt.Errorf("pingcheck_toggle: %v; запасной путь через ndmc недоступен -- у туннеля нет имени интерфейса", primaryErr)
	}
	cmd := "interface " + ndmsName + " ping-check"
	if !enable {
		cmd = "no " + cmd
	}
	out, ndmcErr := exec(ctx, "ndmc", "-c", cmd)
	if ndmcErr == nil {
		return nil
	}
	return fmt.Errorf("pingcheck_toggle: POST=%v; ndmc=%v (%s)", primaryErr, ndmcErr, string(out))
}

func primaryPingCheckToggle(ctx context.Context, c *awgmgr.Client, tunnelID string, enable bool) error {
	// Эндпоинт /api/pingcheck/toggle из awg-manager убран: на 2.17.2 он
	// отвечает 404. Вместо него пара per-tunnel адресов -- включить и снять.
	// Пока код бил в старый адрес, включение сторожа держалось на одном лишь
	// ndmc, а у opkg-туннелей нет ndms_name: для половины туннелей флота
	// действие было сломано целиком и молча.
	path := "/api/tunnels/pingcheck"
	if !enable {
		path = "/api/tunnels/pingcheck/remove"
	}
	q := url.Values{"id": {tunnelID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+path+"?"+q.Encode(), bytes.NewReader(nil))
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP_REFUSED: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP_%d: %s", resp.StatusCode, string(body))
	}
	var env struct {
		Success *bool  `json:"success"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Success != nil && !*env.Success {
		msg := strings.TrimSpace(env.Message)
		if msg == "" {
			msg = string(body)
		}
		return fmt.Errorf("HTTP_SUCCESS_FALSE: %s", msg)
	}
	return nil
}
