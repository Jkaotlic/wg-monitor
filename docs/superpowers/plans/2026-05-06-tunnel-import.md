# Tunnel Import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Admin sends a `.conf` file to a per-router TG topic; bot imports the tunnel via awg-manager, optionally replaces the old one, and restarts HydraRoute if installed.

**Architecture:** Backend downloads the TG document, base64-encodes it, and dispatches a `tunnel_import` wire.Command to the agent. Agent parses the WG/AWG conf, calls `POST /api/tunnels/create`, deletes the old tunnel on success, and restarts hrneo if HydraRoute is installed. A stateful in-memory pending-map on the backend handles the two-step UX (file upload → name confirmation → action buttons).

**Tech Stack:** Go 1.22, existing `awgmgr` HTTP client, TG Bot API (`getFile` + file download), `wire.Command` long-poll, `callbacks.Router` in-memory pending state.

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `pkg/wire/types.go` | Modify | Add `"tunnel_import"` to validCommandActions |
| `internal/agent/awgmgr/types.go` | Modify | Add `CreateTunnelRequest`, `InterfaceConfig`, `PeerConfig` |
| `internal/agent/awgmgr/client.go` | Modify | Add `CreateTunnel`, `DeleteTunnel` |
| `internal/agent/awgmgr/client_test.go` | Modify | Tests for Create/Delete |
| `internal/agent/actions/tunnel_import.go` | Create | WG conf parser + `ImportTunnel` action func |
| `internal/agent/actions/tunnel_import_test.go` | Create | Parser + action tests |
| `internal/agent/actions/runner.go` | Modify | Add `tunnel_import` case in dispatch |
| `internal/agent/actions/runner_test.go` | Modify | runner test for tunnel_import |
| `internal/backend/tg/updates.go` | Modify | Add `Document *Document` to `Message` |
| `internal/backend/tg/client.go` | Modify | Add `GetFile`, `DownloadFile` methods |
| `internal/backend/tg/client_test.go` | Modify | Tests for GetFile/DownloadFile |
| `internal/backend/callbacks/parse.go` | Modify | Add `tunnel_import_replace/add` actions; `ImportToken` field in Args |
| `internal/backend/callbacks/parse_test.go` | Modify | Parse tests for new actions |
| `internal/backend/callbacks/actions.go` | Modify | Add `ImportAction` struct |
| `internal/backend/callbacks/actions_test.go` | Modify | ImportAction tests |
| `internal/backend/callbacks/router.go` | Modify | `pendingUpload` struct, pending map+mutex, TGClient interface ext, document handler, pending-name reply, import routing in HandleCallback |
| `cmd/backend/integration_test.go` | Modify | Add GetFile/DownloadFile no-ops to noopTG |

---

## Task 1: Wire types + awgmgr request structs

**Files:**
- Modify: `pkg/wire/types.go`
- Modify: `internal/agent/awgmgr/types.go`

- [ ] **Step 1: Write failing test for wire validation**

```go
// pkg/wire/types_test.go — add:
func TestIsValidCommandAction_TunnelImport(t *testing.T) {
    if !IsValidCommandAction("tunnel_import") {
        t.Error("tunnel_import must be valid")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```
cd C:/Users/Anex/Projects/wg-monitor
go test ./pkg/wire/ -run TestIsValidCommandAction_TunnelImport -v
```
Expected: FAIL — `tunnel_import must be valid`

- [ ] **Step 3: Add tunnel_import to validCommandActions in pkg/wire/types.go**

In the `validCommandActions` map, add:
```go
"tunnel_import": true,
```

- [ ] **Step 4: Add awgmgr request structs to internal/agent/awgmgr/types.go**

Append after the existing `HydraRouteStatus` struct:

```go
// CreateTunnelRequest is the body for POST /api/tunnels/create.
type CreateTunnelRequest struct {
	Name         string          `json:"name"`
	Type         string          `json:"type"`
	Interface    InterfaceConfig `json:"interface"`
	Peer         PeerConfig      `json:"peer"`
	DefaultRoute bool            `json:"defaultRoute"`
	Enabled      bool            `json:"enabled"`
}

type InterfaceConfig struct {
	PrivateKey string `json:"privateKey"`
	Jc         int    `json:"jc"`
	Jmin       int    `json:"jmin"`
	Jmax       int    `json:"jmax"`
	S1         int    `json:"s1"`
	S2         int    `json:"s2"`
	H1         uint32 `json:"h1"`
	H2         uint32 `json:"h2"`
	H3         uint32 `json:"h3"`
	H4         uint32 `json:"h4"`
}

type PeerConfig struct {
	PublicKey    string   `json:"publicKey"`
	PresharedKey string   `json:"presharedKey,omitempty"`
	Endpoint     string   `json:"endpoint"`
	AllowedIPs   []string `json:"allowedIPs"`
}
```

- [ ] **Step 5: Run tests + vet**

```
go test ./pkg/wire/ ./internal/agent/awgmgr/ -v
go vet ./pkg/wire/ ./internal/agent/awgmgr/
```
Expected: all PASS, no vet errors.

- [ ] **Step 6: Commit**

```
git -c user.email=asnekhaev@gmail.com commit -m "feat(wire+awgmgr): tunnel_import action + CreateTunnelRequest types" -- pkg/wire/types.go pkg/wire/types_test.go internal/agent/awgmgr/types.go
```

---

## Task 2: awgmgr CreateTunnel + DeleteTunnel

**Files:**
- Modify: `internal/agent/awgmgr/client.go`
- Modify: `internal/agent/awgmgr/client_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/agent/awgmgr/client_test.go — append:

func TestClient_CreateTunnel_OK(t *testing.T) {
	want := `{"success":true,"data":{"id":"abc123","name":"awg11","type":"amnezia_wg","status":"running","enabled":true,"defaultRoute":true}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: %s", r.Method)
		}
		if r.URL.Path != "/api/tunnels/create" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("missing Content-Type: %s", r.Header.Get("Content-Type"))
		}
		w.Write([]byte(want))
	}))
	defer srv.Close()
	c := New(srv.URL)
	req := CreateTunnelRequest{
		Name: "awg11", Type: "amnezia_wg",
		Interface: InterfaceConfig{PrivateKey: "abc", Jc: 4, Jmin: 40, Jmax: 70},
		Peer:      PeerConfig{PublicKey: "xyz", Endpoint: "1.2.3.4:51820", AllowedIPs: []string{"0.0.0.0/0"}},
		DefaultRoute: true, Enabled: true,
	}
	tun, err := c.CreateTunnel(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if tun.ID != "abc123" {
		t.Errorf("id: %q", tun.ID)
	}
}

func TestClient_CreateTunnel_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"success":false,"error":{"message":"bad request"}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	_, err := c.CreateTunnel(context.Background(), CreateTunnelRequest{Name: "x"})
	if err == nil {
		t.Fatal("expected error on 400")
	}
}

func TestClient_DeleteTunnel_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method: %s", r.Method)
		}
		if r.URL.Path != "/api/tunnels/abc123" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With")
		}
		w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	if err := c.DeleteTunnel(context.Background(), "abc123"); err != nil {
		t.Fatal(err)
	}
}

func TestClient_DeleteTunnel_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()
	c := New(srv.URL)
	err := c.DeleteTunnel(context.Background(), "bad-id")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404 error, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/agent/awgmgr/ -run "TestClient_CreateTunnel|TestClient_DeleteTunnel" -v
```
Expected: compile error — `CreateTunnel` and `DeleteTunnel` undefined.

- [ ] **Step 3: Implement CreateTunnel + DeleteTunnel in client.go**

Add after the `DiagResult` method (needs `bytes` import added — check existing imports):

```go
// CreateTunnel calls POST /api/tunnels/create. Returns the newly created Tunnel.
func (c *Client) CreateTunnel(ctx context.Context, req CreateTunnelRequest) (*Tunnel, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/tunnels/create", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("X-Requested-With", "XMLHttpRequest")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("awgmgr POST /api/tunnels/create: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("awgmgr read create: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("awgmgr create: HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	var env Envelope[Tunnel]
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("awgmgr create: decode: %w", err)
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr create: success=false")
	}
	return &env.Data, nil
}

// DeleteTunnel calls DELETE /api/tunnels/{id}.
func (c *Client) DeleteTunnel(ctx context.Context, tunnelID string) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/api/tunnels/"+tunnelID, nil)
	if err != nil {
		return err
	}
	httpReq.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return fmt.Errorf("awgmgr DELETE tunnel %s: %w", tunnelID, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("awgmgr DELETE tunnel %s: HTTP %d: %s", tunnelID, resp.StatusCode, snippet(body))
	}
	return nil
}
```

Add `"bytes"` to the import block in client.go if not already present.

- [ ] **Step 4: Run tests**

```
go test ./internal/agent/awgmgr/ -v
```
Expected: all PASS.

- [ ] **Step 5: Commit**

```
git -c user.email=asnekhaev@gmail.com commit -m "feat(awgmgr): CreateTunnel + DeleteTunnel" -- internal/agent/awgmgr/client.go internal/agent/awgmgr/client_test.go
```

---

## Task 3: WG conf parser

**Files:**
- Create: `internal/agent/actions/tunnel_import.go`
- Create: `internal/agent/actions/tunnel_import_test.go`

- [ ] **Step 1: Write failing parser tests**

Create `internal/agent/actions/tunnel_import_test.go`:

```go
package actions

import (
	"strings"
	"testing"
)

const awgConf = `
[Interface]
PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
Address = 10.8.1.2/32
Jc = 4
Jmin = 40
Jmax = 70
S1 = 0
S2 = 0
H1 = 1111111111
H2 = 2222222222
H3 = 3333333333
H4 = 4444444444
DNS = 1.1.1.1

[Peer]
PublicKey = BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=
PresharedKey = CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
`

func TestParseWGConf_Happy(t *testing.T) {
	req, err := ParseWGConf(awgConf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if req.Interface.PrivateKey != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" {
		t.Errorf("PrivateKey: %q", req.Interface.PrivateKey)
	}
	if req.Interface.Jc != 4 {
		t.Errorf("Jc: %d", req.Interface.Jc)
	}
	if req.Interface.Jmin != 40 {
		t.Errorf("Jmin: %d", req.Interface.Jmin)
	}
	if req.Interface.Jmax != 70 {
		t.Errorf("Jmax: %d", req.Interface.Jmax)
	}
	if req.Interface.H1 != 1111111111 {
		t.Errorf("H1: %d", req.Interface.H1)
	}
	if req.Interface.H4 != 4444444444 {
		t.Errorf("H4: %d", req.Interface.H4)
	}
	if req.Peer.PublicKey != "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=" {
		t.Errorf("PublicKey: %q", req.Peer.PublicKey)
	}
	if req.Peer.PresharedKey != "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=" {
		t.Errorf("PresharedKey: %q", req.Peer.PresharedKey)
	}
	if req.Peer.Endpoint != "vpn.example.com:51820" {
		t.Errorf("Endpoint: %q", req.Peer.Endpoint)
	}
	if len(req.Peer.AllowedIPs) != 2 {
		t.Errorf("AllowedIPs len: %d, want 2", len(req.Peer.AllowedIPs))
	}
	if req.Peer.AllowedIPs[0] != "0.0.0.0/0" {
		t.Errorf("AllowedIPs[0]: %q", req.Peer.AllowedIPs[0])
	}
	if req.Type != "amnezia_wg" {
		t.Errorf("Type: %q", req.Type)
	}
	if !req.Enabled {
		t.Error("Enabled must be true")
	}
	// Address/DNS/PersistentKeepalive are ignored — no fields for them
}

func TestParseWGConf_MissingPrivateKey(t *testing.T) {
	conf := strings.ReplaceAll(awgConf, "PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n", "")
	_, err := ParseWGConf(conf)
	if err == nil || !strings.Contains(err.Error(), "PrivateKey") {
		t.Errorf("expected PrivateKey error, got %v", err)
	}
}

func TestParseWGConf_MissingEndpoint(t *testing.T) {
	conf := strings.ReplaceAll(awgConf, "Endpoint = vpn.example.com:51820\n", "")
	_, err := ParseWGConf(conf)
	if err == nil || !strings.Contains(err.Error(), "Endpoint") {
		t.Errorf("expected Endpoint error, got %v", err)
	}
}

func TestParseWGConf_NoPresharedKey(t *testing.T) {
	conf := strings.ReplaceAll(awgConf, "PresharedKey = CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=\n", "")
	req, err := ParseWGConf(conf)
	if err != nil {
		t.Fatal(err)
	}
	if req.Peer.PresharedKey != "" {
		t.Errorf("PresharedKey must be empty, got %q", req.Peer.PresharedKey)
	}
}

func TestSanitizeTunnelName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"awg11.conf", "awg11"},
		{"awg11", "awg11"},
		{"VPN Config", "vpn-config"},
		{"my_tunnel-1", "my-tunnel-1"},
		{"UPPER", "upper"},
		{"bad!chars@here", "badcharshere"},
	}
	for _, tc := range tests {
		got := sanitizeTunnelName(strings.TrimSuffix(tc.in, ".conf"))
		if got != tc.want {
			t.Errorf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsValidTunnelName(t *testing.T) {
	valid := []string{"awg11", "vpn-1", "my_tunnel", "a1"}
	for _, v := range valid {
		if !isValidTunnelName(v) {
			t.Errorf("%q should be valid", v)
		}
	}
	invalid := []string{"", "A", "1start", "has space", strings.Repeat("x", 33)}
	for _, v := range invalid {
		if isValidTunnelName(v) {
			t.Errorf("%q should be invalid", v)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/agent/actions/ -run "TestParseWGConf|TestSanitize|TestIsValid" -v
```
Expected: compile error — `ParseWGConf`, `sanitizeTunnelName`, `isValidTunnelName` undefined.

- [ ] **Step 3: Create internal/agent/actions/tunnel_import.go with parser**

```go
package actions

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
)

var validNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// isValidTunnelName reports whether s is a legal awg-manager tunnel name.
func isValidTunnelName(s string) bool { return validNameRe.MatchString(s) }

// sanitizeTunnelName lowercases s and replaces any character outside
// [a-z0-9_-] with '-', then strips leading/trailing '-'.
func sanitizeTunnelName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// ParseWGConf parses a WireGuard / AmneziaWG .conf into a CreateTunnelRequest.
// Name and DefaultRoute must be set by the caller after parsing.
func ParseWGConf(data string) (awgmgr.CreateTunnelRequest, error) {
	var req awgmgr.CreateTunnelRequest
	req.Type = "amnezia_wg"
	req.Enabled = true

	section := ""
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(line[1 : len(line)-1])
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		switch section {
		case "interface":
			if err := parseInterfaceField(&req.Interface, key, val); err != nil {
				return req, fmt.Errorf("parse [Interface] %s: %w", key, err)
			}
		case "peer":
			if err := parsePeerField(&req.Peer, key, val); err != nil {
				return req, fmt.Errorf("parse [Peer] %s: %w", key, err)
			}
		}
	}
	if req.Interface.PrivateKey == "" {
		return req, fmt.Errorf("missing PrivateKey in [Interface]")
	}
	if req.Peer.PublicKey == "" {
		return req, fmt.Errorf("missing PublicKey in [Peer]")
	}
	if req.Peer.Endpoint == "" {
		return req, fmt.Errorf("missing Endpoint in [Peer]")
	}
	return req, nil
}

func parseInterfaceField(iface *awgmgr.InterfaceConfig, key, val string) error {
	parseInt := func(dst *int) error {
		n, err := strconv.Atoi(val)
		if err != nil {
			return err
		}
		*dst = n
		return nil
	}
	parseU32 := func(dst *uint32) error {
		n, err := strconv.ParseUint(val, 10, 32)
		if err != nil {
			return err
		}
		*dst = uint32(n)
		return nil
	}
	switch key {
	case "PrivateKey":
		iface.PrivateKey = val
	case "Jc":
		return parseInt(&iface.Jc)
	case "Jmin":
		return parseInt(&iface.Jmin)
	case "Jmax":
		return parseInt(&iface.Jmax)
	case "S1":
		return parseInt(&iface.S1)
	case "S2":
		return parseInt(&iface.S2)
	case "H1":
		return parseU32(&iface.H1)
	case "H2":
		return parseU32(&iface.H2)
	case "H3":
		return parseU32(&iface.H3)
	case "H4":
		return parseU32(&iface.H4)
	// Address, DNS, ListenPort: valid but ignored (Keenetic manages them)
	}
	return nil
}

func parsePeerField(peer *awgmgr.PeerConfig, key, val string) error {
	switch key {
	case "PublicKey":
		peer.PublicKey = val
	case "PresharedKey":
		peer.PresharedKey = val
	case "Endpoint":
		peer.Endpoint = val
	case "AllowedIPs":
		for _, p := range strings.Split(val, ",") {
			if p = strings.TrimSpace(p); p != "" {
				peer.AllowedIPs = append(peer.AllowedIPs, p)
			}
		}
	// PersistentKeepalive: ignored (awg-manager manages keepalive)
	}
	return nil
}

// ImportTunnel is the agent-side handler for the tunnel_import wire.Command.
// confB64 is base64-encoded .conf content. If replace=true, finds the tunnel
// by name in awg-manager and deletes it AFTER successful create. Restarts
// HydraRoute daemon if installed.
func ImportTunnel(ctx context.Context, client *awgmgr.Client, exec ExecFunc, confB64, name string, replace bool) (string, error) {
	confData, err := base64.StdEncoding.DecodeString(confB64)
	if err != nil {
		return "", fmt.Errorf("decode conf: %w", err)
	}

	req, err := ParseWGConf(string(confData))
	if err != nil {
		return "", fmt.Errorf("parse conf: %w", err)
	}
	req.Name = name

	// Find old tunnel if replacing.
	var oldTunnelID string
	if replace {
		all, err := client.TunnelsAll(ctx)
		if err != nil {
			return "", fmt.Errorf("list tunnels: %w", err)
		}
		for _, t := range all.Tunnels {
			if t.Name == name {
				oldTunnelID = t.ID
				req.DefaultRoute = t.DefaultRoute
				break
			}
		}
		if oldTunnelID == "" {
			req.DefaultRoute = true // name not found — just create as default
		}
	} else {
		req.DefaultRoute = false
	}

	// Create first (safe: if this fails the old tunnel is untouched).
	newTun, err := client.CreateTunnel(ctx, req)
	if err != nil {
		return "", fmt.Errorf("create tunnel: %w", err)
	}

	var result strings.Builder
	fmt.Fprintf(&result, "✅ Туннель %q создан (id=%s)", name, newTun.ID)

	// Delete old only after successful create.
	if oldTunnelID != "" {
		if err := client.DeleteTunnel(ctx, oldTunnelID); err != nil {
			fmt.Fprintf(&result, "\n⚠️ Удалить старый туннель не удалось: %v", err)
		} else {
			fmt.Fprintf(&result, "\n🗑 Старый туннель удалён")
		}
	}

	// Restart hrneo if HydraRoute is installed.
	if hs, err := client.HydraRouteStatus(ctx); err == nil && hs.Installed {
		out, execErr := exec(ctx, "/opt/etc/init.d/S99hrneo", "restart")
		if execErr != nil {
			fmt.Fprintf(&result, "\n⚠️ HydraRoute restart failed: %v\n%s", execErr, string(out))
		} else {
			fmt.Fprintf(&result, "\n🔁 HydraRoute перезапущен")
		}
	}

	return result.String(), nil
}
```

Note: `ImportTunnel` uses `context.Context` — add `"context"` to imports.

- [ ] **Step 4: Run tests**

```
go test ./internal/agent/actions/ -run "TestParseWGConf|TestSanitize|TestIsValid" -v
```
Expected: all PASS.

- [ ] **Step 5: Commit**

```
git -c user.email=asnekhaev@gmail.com commit -m "feat(actions): WG conf parser + ImportTunnel func" -- internal/agent/actions/tunnel_import.go internal/agent/actions/tunnel_import_test.go
```

---

## Task 4: Agent runner — tunnel_import case

**Files:**
- Modify: `internal/agent/actions/runner.go`
- Modify: `internal/agent/actions/runner_test.go`

- [ ] **Step 1: Write failing runner test**

```go
// internal/agent/actions/runner_test.go — append:

const testConfB64 = "W0ludGVyZmFjZV0KUHJpdmF0ZUtleSA9IEFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE9CkpjID0gNApKbWluID0gNDAKSm1heCA9IDcwClMxID0gMApTMiA9IDAKSDEgPSAxMTExMTExMTExCkgyID0gMjIyMjIyMjIyMgpIMyA9IDMzMzMzMzMzMzMKSDQgPSA0NDQ0NDQ0NDQ0CgpbUGVlcl0KUHVibGljS2V5ID0gQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQj0KRW5kcG9pbnQgPSB2cG4uZXhhbXBsZS5jb206NTE4MjAKQWxsb3dlZElQcyA9IDAuMC4wLjAvMAo="

func TestRunner_TunnelImport_CreateAndReplace(t *testing.T) {
	tunnelsAllResp := `{"success":true,"data":{"tunnels":[{"id":"old-id","name":"awg11","defaultRoute":true,"enabled":true}],"external":[],"system":[]}}`
	createResp := `{"success":true,"data":{"id":"new-id","name":"awg11","type":"amnezia_wg","status":"running","enabled":true,"defaultRoute":true}}`
	hydroResp := `{"success":true,"data":{"installed":false,"running":false}}`

	var deletedID string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(tunnelsAllResp))
	})
	mux.HandleFunc("/api/tunnels/create", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(createResp))
	})
	mux.HandleFunc("/api/tunnels/old-id", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletedID = "old-id"
			w.Write([]byte(`{"success":true}`))
		}
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(hydroResp))
	})
	cli := awgmgrFake(t, mux)
	r := Runner{AwgClient: cli, Exec: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, nil
	}, Now: mockNow()}

	res := r.Execute(context.Background(), wire.Command{
		ID:     "imp1",
		Action: "tunnel_import",
		Args:   map[string]any{"conf": testConfB64, "name": "awg11", "replace": true},
	})
	if res.Status != "ok" {
		t.Errorf("status=%q output=%q", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "awg11") {
		t.Errorf("output missing tunnel name: %q", res.Output)
	}
	if deletedID != "old-id" {
		t.Errorf("expected old tunnel deleted, deletedID=%q", deletedID)
	}
}

func TestRunner_TunnelImport_MissingArgs(t *testing.T) {
	r := Runner{AwgClient: awgmgrFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})), Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{
		ID: "imp2", Action: "tunnel_import",
		Args: map[string]any{}, // missing conf and name
	})
	if res.Status != "err" {
		t.Errorf("expected err, got %q", res.Status)
	}
}
```

Note: `testConfB64` is the base64 of a minimal valid AWG conf (pre-computed). Verify it decodes to a valid conf by running `echo <base64> | base64 -d` in a shell.

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/agent/actions/ -run "TestRunner_TunnelImport" -v
```
Expected: FAIL — `tunnel_import: conf and name are required` (case not in runner yet).

- [ ] **Step 3: Add tunnel_import case to runner.go dispatch**

In `runner.go`, inside `func (r *Runner) dispatch(...)`, add after the `tunnel_disable` case:

```go
case "tunnel_import":
    if r.AwgClient == nil {
        return "err", "awgmgr client not configured"
    }
    if r.Exec == nil {
        return "err", "exec not configured"
    }
    confB64, _ := cmd.Args["conf"].(string)
    name, _ := cmd.Args["name"].(string)
    replace, _ := cmd.Args["replace"].(bool)
    if confB64 == "" || name == "" {
        return "err", "tunnel_import: conf and name are required"
    }
    out, err := ImportTunnel(ctx, r.AwgClient, r.Exec, confB64, name, replace)
    if err != nil {
        return "err", err.Error()
    }
    return "ok", out
```

- [ ] **Step 4: Run all action tests**

```
go test ./internal/agent/actions/ -v
```
Expected: all PASS.

- [ ] **Step 5: Commit**

```
git -c user.email=asnekhaev@gmail.com commit -m "feat(actions): runner dispatches tunnel_import" -- internal/agent/actions/runner.go internal/agent/actions/runner_test.go
```

---

## Task 5: TG Document struct + GetFile + DownloadFile

**Files:**
- Modify: `internal/backend/tg/updates.go`
- Modify: `internal/backend/tg/client.go`
- Modify: `internal/backend/tg/client_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/backend/tg/client_test.go — append:

func TestGetFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getFile") {
			t.Fatalf("path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)
		if req["file_id"] != "file123" {
			t.Errorf("file_id: %v", req["file_id"])
		}
		w.Write([]byte(`{"ok":true,"result":{"file_id":"file123","file_path":"documents/file123.conf"}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	fp, err := c.GetFile(context.Background(), "file123")
	if err != nil {
		t.Fatal(err)
	}
	if fp != "documents/file123.conf" {
		t.Errorf("file_path: %q", fp)
	}
}

func TestDownloadFile(t *testing.T) {
	content := []byte("[Interface]\nPrivateKey = abc\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/file/bottok/documents/file123.conf") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Write(content)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client()}
	data, err := c.DownloadFile(context.Background(), "documents/file123.conf")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) {
		t.Errorf("data: %q", data)
	}
}

func TestGetUpdates_ParseDocument(t *testing.T) {
	resp := `{"ok":true,"result":[{"update_id":1,"message":{"message_id":10,"from":{"id":99},"chat":{"id":-100},"message_thread_id":5,"document":{"file_id":"fid1","file_name":"awg11.conf","file_size":512}}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(resp))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL + "/bot", Token: "tok", HTTP: srv.Client(), LongPollHTTP: srv.Client()}
	updates, err := c.GetUpdates(context.Background(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 {
		t.Fatalf("len: %d", len(updates))
	}
	doc := updates[0].Message.Document
	if doc == nil {
		t.Fatal("Document is nil")
	}
	if doc.FileID != "fid1" || doc.FileName != "awg11.conf" {
		t.Errorf("doc: %+v", doc)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/backend/tg/ -run "TestGetFile|TestDownloadFile|TestGetUpdates_ParseDocument" -v
```
Expected: compile errors — `GetFile`, `DownloadFile` undefined; `Document` field missing.

- [ ] **Step 3: Add Document to Message in updates.go**

In `tg/updates.go`, update `Message` struct and add `Document` type:

```go
type Message struct {
	MessageID       int64     `json:"message_id"`
	Chat            Chat      `json:"chat"`
	From            User      `json:"from"`
	MessageThreadID *int64    `json:"message_thread_id,omitempty"`
	Text            string    `json:"text"`
	Document        *Document `json:"document,omitempty"`
}

// Document represents a TG document (file) attachment on a Message.
type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
	MimeType string `json:"mime_type,omitempty"`
}
```

- [ ] **Step 4: Add GetFile + DownloadFile to client.go**

Add after `DeleteMessage`:

```go
type getFileReq struct {
	FileID string `json:"file_id"`
}

type getFileResult struct {
	FilePath string `json:"file_path"`
}

// GetFile returns the server-side file_path for a TG file_id. The path is
// valid for 1 hour. Use DownloadFile to fetch the actual bytes.
func (c *Client) GetFile(ctx context.Context, fileID string) (string, error) {
	body, _ := json.Marshal(getFileReq{FileID: fileID})
	var out getFileResult
	if err := c.call(ctx, "getFile", body, &out); err != nil {
		return "", err
	}
	return out.FilePath, nil
}

// DownloadFile fetches raw bytes from the TG file CDN.
// filePath comes from GetFile. Limit 20 MB (TG Bot API hard cap).
func (c *Client) DownloadFile(ctx context.Context, filePath string) ([]byte, error) {
	// BaseURL is "https://api.telegram.org/bot" — build file URL from same host.
	fileBase := strings.TrimSuffix(c.BaseURL, "bot") + "file/bot"
	url := fileBase + c.Token + "/" + filePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("tg DownloadFile: build request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tg DownloadFile: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tg DownloadFile: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
}
```

Add `"strings"` to the client.go import block if not already present.

- [ ] **Step 5: Run all tg tests**

```
go test ./internal/backend/tg/ -v
```
Expected: all PASS.

- [ ] **Step 6: Commit**

```
git -c user.email=asnekhaev@gmail.com commit -m "feat(tg): Document struct + GetFile + DownloadFile" -- internal/backend/tg/updates.go internal/backend/tg/client.go internal/backend/tg/client_test.go
```

---

## Task 6: Callbacks parse + ImportAction

**Files:**
- Modify: `internal/backend/callbacks/parse.go`
- Modify: `internal/backend/callbacks/parse_test.go`
- Modify: `internal/backend/callbacks/actions.go`
- Modify: `internal/backend/callbacks/actions_test.go`

- [ ] **Step 1: Write failing parse tests**

```go
// internal/backend/callbacks/parse_test.go — append:

func TestParse_TunnelImportReplace(t *testing.T) {
	args, err := Parse("tunnel_import_replace:42:awg11:a1b2c3d4")
	if err != nil {
		t.Fatal(err)
	}
	if args.Action != "tunnel_import_replace" {
		t.Errorf("action: %q", args.Action)
	}
	if args.UserID != 42 {
		t.Errorf("uid: %d", args.UserID)
	}
	if args.CheckName != "awg11" {
		t.Errorf("check: %q", args.CheckName)
	}
	if args.ImportToken != "a1b2c3d4" {
		t.Errorf("token: %q", args.ImportToken)
	}
}

func TestParse_TunnelImportAdd(t *testing.T) {
	args, err := Parse("tunnel_import_add:7:new-tun:deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if args.Action != "tunnel_import_add" || args.ImportToken != "deadbeef" {
		t.Errorf("args: %+v", args)
	}
}

func TestParse_TunnelImportMissingToken(t *testing.T) {
	_, err := Parse("tunnel_import_replace:42:awg11")
	if err == nil {
		t.Error("expected error for missing token")
	}
}
```

- [ ] **Step 2: Run parse tests to verify they fail**

```
go test ./internal/backend/callbacks/ -run "TestParse_TunnelImport" -v
```
Expected: FAIL — `unknown action: "tunnel_import_replace"`.

- [ ] **Step 3: Update parse.go — add ImportToken to Args and new actions**

In `parse.go`, add `ImportToken string` to the `Args` struct:
```go
type Args struct {
	Action      string
	UserID      int64
	CheckName   string
	TTL         time.Duration
	IsMenu      bool
	NDMSName    string
	IsPanel     bool
	ImportToken string // set for tunnel_import_replace / tunnel_import_add
}
```

Add to `validActions` map:
```go
"tunnel_import_replace": true,
"tunnel_import_add":     true,
```

In `Parse()`, after the `tunnel_enable/disable` block, add:
```go
if action == "tunnel_import_replace" || action == "tunnel_import_add" {
    if len(parts) < 4 || parts[3] == "" {
        return Args{}, fmt.Errorf("%s requires token: %q", action, data)
    }
    a.ImportToken = parts[3]
}
```

- [ ] **Step 4: Write failing ImportAction test**

```go
// internal/backend/callbacks/actions_test.go — append:

type fakeSink struct {
    lastCmd  wire.Command
    lastRef  cmdpkg.MessageRef
    lastUID  int64
}

func (f *fakeSink) Enqueue(userID int64, cmd wire.Command) error {
    f.lastUID, f.lastCmd = userID, cmd
    return nil
}

func (f *fakeSink) EnqueueWithRef(userID int64, cmd wire.Command, ref cmdpkg.MessageRef) error {
    f.lastUID, f.lastCmd, f.lastRef = userID, cmd, ref
    return nil
}

func TestImportAction_Apply_Replace(t *testing.T) {
    pending := map[int64]*pendingUpload{
        42: {ConfB64: "abc", Name: "awg11", Token: "tok1", ExpiresAt: time.Now().Add(time.Minute)},
    }
    sink := &fakeSink{}
    a := &ImportAction{
        sink: sink,
        consumeFn: func(uid int64, token string) (*pendingUpload, bool) {
            if uid == 42 && token == "tok1" {
                up := pending[uid]
                delete(pending, uid)
                return up, true
            }
            return nil, false
        },
        idGen: func() string { return "fixed-id" },
    }
    q := &tg.CallbackQuery{
        ID:      "cbq1",
        From:    tg.User{ID: 42},
        Message: tg.Message{MessageID: 10, Chat: tg.Chat{ID: -100}},
        Data:    "tunnel_import_replace:42:awg11:tok1",
    }
    args := Args{Action: "tunnel_import_replace", UserID: 42, CheckName: "awg11", ImportToken: "tok1"}
    status, err := a.Apply(context.Background(), q, args)
    if err != nil {
        t.Fatal(err)
    }
    if !strings.Contains(status, "awg11") {
        t.Errorf("status: %q", status)
    }
    if sink.lastCmd.Action != "tunnel_import" {
        t.Errorf("cmd action: %q", sink.lastCmd.Action)
    }
    if sink.lastCmd.Args["replace"] != true {
        t.Errorf("replace arg: %v", sink.lastCmd.Args["replace"])
    }
    if sink.lastCmd.Args["name"] != "awg11" {
        t.Errorf("name arg: %v", sink.lastCmd.Args["name"])
    }
    if len(pending) != 0 {
        t.Error("pending should be consumed")
    }
}

func TestImportAction_Apply_Expired(t *testing.T) {
    sink := &fakeSink{}
    a := &ImportAction{
        sink:      sink,
        consumeFn: func(uid int64, token string) (*pendingUpload, bool) { return nil, false },
        idGen:     func() string { return "x" },
    }
    q := &tg.CallbackQuery{Message: tg.Message{Chat: tg.Chat{ID: -100}}}
    _, err := a.Apply(context.Background(), q, Args{Action: "tunnel_import_replace", UserID: 99})
    if err == nil || !strings.Contains(err.Error(), "истекла") {
        t.Errorf("expected expiry error, got %v", err)
    }
}
```

Note: the test references `pendingUpload` — this type is defined in router.go (Task 7). For now the test file will have a compile error. We'll fix that in Task 7.

Actually, to avoid ordering issues, define `pendingUpload` in `actions.go` (or a new file `import_state.go`) so both Task 6 and Task 7 can reference it. Let's put it in `actions.go`.

- [ ] **Step 5: Add pendingUpload + ImportAction to actions.go**

Append to `internal/backend/callbacks/actions.go`:

```go
// ----- pendingUpload (import flow state) -----

// pendingUpload holds a downloaded .conf while the admin confirms what to do.
// Stored in Router.pending keyed by userID; consumed by ImportAction.
type pendingUpload struct {
	ConfB64       string
	Name          string    // empty = still waiting for admin to type tunnel name
	SuggestedName string    // sanitized from filename, shown in "how to name?" prompt
	ThreadID      *int64
	Token         string    // 8-hex random, embedded in callback_data
	ExpiresAt     time.Time // 5 min from upload
}

// ----- ImportAction -----

// ImportAction is triggered by tunnel_import_replace / tunnel_import_add
// callback buttons. It looks up the pending conf upload, then enqueues a
// tunnel_import wire.Command for the agent.
type ImportAction struct {
	sink      CommandEnqueuer
	consumeFn func(userID int64, token string) (*pendingUpload, bool)
	idGen     func() string
}

func (a *ImportAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	up, ok := a.consumeFn(args.UserID, args.ImportToken)
	if !ok {
		return "", fmt.Errorf("загрузка истекла или не найдена; отправь конфиг заново")
	}
	replace := args.Action == "tunnel_import_replace"
	cmd := wire.Command{
		ID:     a.idGen(),
		Action: "tunnel_import",
		Args: map[string]any{
			"conf":    up.ConfB64,
			"name":    up.Name,
			"replace": replace,
		},
		IssuedAt: time.Now().UTC(),
	}
	ref := cmdpkg.MessageRef{
		ChatID:    q.Message.Chat.ID,
		MessageID: q.Message.MessageID,
		ThreadID:  q.Message.MessageThreadID,
	}
	if err := a.sink.EnqueueWithRef(args.UserID, cmd, ref); err != nil {
		return "", fmt.Errorf("enqueue tunnel_import: %w", err)
	}
	verb := "добавление"
	if replace {
		verb = "замена"
	}
	return fmt.Sprintf("📤 Import (%s %q) поставлен в очередь", verb, up.Name), nil
}
```

- [ ] **Step 6: Run all callbacks tests**

```
go test ./internal/backend/callbacks/ -v
```
Expected: all PASS (ImportAction tests may still fail until Task 7 adds router changes — check compile only for now).

- [ ] **Step 7: Commit**

```
git -c user.email=asnekhaev@gmail.com commit -m "feat(callbacks): parse tunnel_import_replace/add + ImportAction" -- internal/backend/callbacks/parse.go internal/backend/callbacks/parse_test.go internal/backend/callbacks/actions.go internal/backend/callbacks/actions_test.go
```

---

## Task 7: Router — pending state + document handler

**Files:**
- Modify: `internal/backend/callbacks/router.go`
- Modify: `cmd/backend/integration_test.go`

- [ ] **Step 1: Extend TGClient interface in router.go**

In `router.go`, add to the `TGClient` interface:

```go
type TGClient interface {
	// ... existing methods ...
	GetFile(ctx context.Context, fileID string) (string, error)
	DownloadFile(ctx context.Context, filePath string) ([]byte, error)
}
```

- [ ] **Step 2: Add pending state fields + importAction to Router struct**

Add to the `Router` struct (after the `command *CommandAction` field):

```go
type Router struct {
	d            *db.DB
	tg           TGClient
	cfg          Config
	silence      *SilenceAction
	ack          *AckAction
	mute         *MuteAction
	history      *HistoryAction
	command      *CommandAction
	importAction *ImportAction
	pendingMu    sync.Mutex
	pending      map[int64]*pendingUpload
}
```

Add `"sync"` to imports if not already there.

- [ ] **Step 3: Update NewRouterWithSink — init pending map + ImportAction**

In `NewRouterWithSink`, replace `return &Router{...}` with:

```go
r := &Router{
    d:       d,
    tg:      tgClient,
    cfg:     cfg,
    pending: make(map[int64]*pendingUpload),
    silence: NewSilenceAction(d),
    ack:     NewAckAction(d),
    mute:    NewMuteAction(d, cfg.MuteCutoffHour),
    history: NewHistoryAction(d, tgClient, cfg.ChatID),
    command: NewCommandAction(sink, nil),
}
r.importAction = &ImportAction{
    sink: sink,
    consumeFn: func(userID int64, token string) (*pendingUpload, bool) {
        r.pendingMu.Lock()
        defer r.pendingMu.Unlock()
        up, ok := r.pending[userID]
        if !ok || up.Token != token || time.Now().After(up.ExpiresAt) || up.Name == "" {
            return nil, false
        }
        delete(r.pending, userID)
        return up, true
    },
    idGen: defaultCmdID,
}
return r
```

Update `NewRouter` (no-sink variant) similarly (its call to `NewRouterWithSink` already passes nil sink — but we still need pending map, so the above `NewRouterWithSink` code handles it).

- [ ] **Step 4: Add storePending helper and newImportToken to router.go**

```go
func (r *Router) storePending(userID int64, up *pendingUpload) {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	r.pending[userID] = up
}

func newImportToken() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
```

Add `"crypto/rand"`, `"encoding/base64"`, `"encoding/hex"` to imports if not present (`crypto/rand` may already be there via actions.go).

- [ ] **Step 5: Add document handler + pending-name helper to router.go**

Add these methods to Router:

```go
// handleDocumentUpload processes an incoming .conf file from the admin.
func (r *Router) handleDocumentUpload(ctx context.Context, m *tg.Message, kind string, user *db.User) {
	if kind != "per_router" || user == nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
			"конфиги принимаются только в топике роутера.", "", nil)
		return
	}
	if m.Document.FileSize > 50*1024 {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
			"файл слишком большой (максимум 50 КБ для .conf).", "", nil)
		return
	}
	filePath, err := r.tg.GetFile(ctx, m.Document.FileID)
	if err != nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
			"не удалось получить файл: "+err.Error(), "", nil)
		return
	}
	data, err := r.tg.DownloadFile(ctx, filePath)
	if err != nil {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
			"не удалось скачать файл: "+err.Error(), "", nil)
		return
	}
	confB64 := base64.StdEncoding.EncodeToString(data)
	suggested := sanitizeTunnelName(strings.TrimSuffix(m.Document.FileName, ".conf"))
	token := newImportToken()
	up := &pendingUpload{
		ConfB64: confB64, SuggestedName: suggested,
		ThreadID: m.MessageThreadID, Token: token,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	if isValidTunnelName(suggested) {
		up.Name = suggested
		r.storePending(user.ID, up)
		r.sendImportConfirmation(ctx, m.Chat.ID, m.MessageThreadID, user.ID, suggested, token)
	} else {
		r.storePending(user.ID, up)
		_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, m.Chat.ID, m.MessageThreadID,
			fmt.Sprintf("📁 Получен файл «%s». Как назвать туннель? (a-z0-9_-, начинается с буквы, предложение: %q)", m.Document.FileName, suggested),
			"", nil, tg.ReplyKeyboardForTopic("per_router"))
	}
}

// sendImportConfirmation sends the inline keyboard asking Replace vs Add.
func (r *Router) sendImportConfirmation(ctx context.Context, chatID int64, threadID *int64, userID int64, name, token string) {
	kb := tg.InlineKeyboardMarkup{
		InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: fmt.Sprintf("🔄 Заменить %s", name),
				CallbackData: fmt.Sprintf("tunnel_import_replace:%d:%s:%s", userID, name, token)}},
			{{Text: "➕ Добавить как новый",
				CallbackData: fmt.Sprintf("tunnel_import_add:%d:%s:%s", userID, name, token)}},
		},
	}
	_, _ = r.tg.SendMessageWithReplyKeyboard(ctx, chatID, threadID,
		fmt.Sprintf("📁 Конфиг для туннеля *%s*. Что делать?", name),
		"MarkdownV2", nil, &kb)
}

// handlePendingNameReply handles plain text replies when a conf is pending
// naming. Returns true if the message was consumed.
func (r *Router) handlePendingNameReply(ctx context.Context, m *tg.Message, user *db.User) bool {
	if user == nil {
		return false
	}
	r.pendingMu.Lock()
	up, ok := r.pending[user.ID]
	r.pendingMu.Unlock()
	if !ok || time.Now().After(up.ExpiresAt) || up.Name != "" {
		return false
	}
	name := sanitizeTunnelName(m.Text)
	if !isValidTunnelName(name) {
		_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
			fmt.Sprintf("Имя %q не подходит (нужно a-z0-9_-, начинается с буквы). Попробуй снова.", m.Text),
			"", nil)
		return true
	}
	up.Name = name
	r.storePending(user.ID, up)
	r.sendImportConfirmation(ctx, m.Chat.ID, m.MessageThreadID, user.ID, name, up.Token)
	return true
}
```

Add `"encoding/base64"` and `"strings"` to imports (check if already present).
`sanitizeTunnelName` and `isValidTunnelName` are defined in `internal/agent/actions/tunnel_import.go` — they need to be accessible to the backend. **Move them** to a shared package or duplicate them in `callbacks/import_util.go`.

Since the agent and backend are different binaries, duplicate the two helpers in a new file `internal/backend/callbacks/import_util.go`:

```go
package callbacks

import (
	"regexp"
	"strings"
)

var validTunnelNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

func isValidTunnelName(s string) bool { return validTunnelNameRe.MatchString(s) }

func sanitizeTunnelName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
```

- [ ] **Step 6: Wire document handler + import routing in HandleMessage/HandleCallback**

In `HandleMessage`, add document check BEFORE the switch, after `resolveTopicKind`:

```go
// Document handler — before text switch.
if m.Document != nil {
    r.handleDocumentUpload(ctx, m, kind, user)
    return
}
```

In the `default` case of the `switch m.Text` block, replace `return` with:

```go
default:
    if r.handlePendingNameReply(ctx, m, user) {
        return
    }
    return
```

In `HandleCallback`, inside the `switch args.Action` block, add:

```go
case "tunnel_import_replace", "tunnel_import_add":
    action = r.importAction
```

(Add before the `tunnels_refresh` case.)

- [ ] **Step 7: Update noopTG in integration_test.go**

Open `cmd/backend/integration_test.go` and find the `noopTG` struct. Add the two new methods:

```go
func (n *noopTG) GetFile(_ context.Context, _ string) (string, error)       { return "", nil }
func (n *noopTG) DownloadFile(_ context.Context, _ string) ([]byte, error)  { return nil, nil }
```

- [ ] **Step 8: Build + test everything**

```
go build ./...
go vet ./...
go test ./... -count=1
```
Expected: all PASS, no vet errors.

- [ ] **Step 9: Commit**

```
git -c user.email=asnekhaev@gmail.com commit -m "feat(router): document upload handler + pending-name state + import confirm" -- internal/backend/callbacks/router.go internal/backend/callbacks/import_util.go cmd/backend/integration_test.go
```

---

## Task 8: Version bump, cross-compile, deploy, verify

**Files:**
- Modify: `cmd/backend/main.go` (version string)
- Modify: `cmd/agent/main.go` (version string)
- Build artifacts: `dist/`

- [ ] **Step 1: Bump version strings**

In `cmd/backend/main.go`:
```go
var Version = "0.8.0-tunnel-import"
```

In `cmd/agent/main.go`, find the `Version` variable and update:
```go
var Version = "0.8.0-tunnel-import"
```

- [ ] **Step 2: Cross-compile agent (arm64) via PowerShell**

```powershell
cd C:\Users\Anex\Projects\wg-monitor
$env:GOOS="linux"; $env:GOARCH="arm64"; go build -ldflags "-X main.Version=0.8.0-tunnel-import" -o dist/wg-monitor-agent-arm64 ./cmd/agent/
$env:GOOS=""; $env:GOARCH=""
```

- [ ] **Step 3: Cross-compile backend (amd64) via PowerShell**

```powershell
$env:GOOS="linux"; $env:GOARCH="amd64"; go build -ldflags "-X main.Version=0.8.0-tunnel-import" -o dist/wg-monitor-backend-amd64 ./cmd/backend/
$env:GOOS=""; $env:GOARCH=""
```

- [ ] **Step 4: Deploy backend to VPS Main**

```powershell
python deploy/backend/deploy_vps_main.py --binary dist/wg-monitor-backend-amd64 --password 'c711X09M5ASy'
```

Expected output: `backend service restarted` or similar.

- [ ] **Step 5: Deploy agent to testkeen**

```powershell
python deploy/agent/deploy_keenetic.py --bin dist/wg-monitor-agent-arm64 --config deploy/agent/configs/testkeen.local.yaml
```

- [ ] **Step 6: Verify agent version on testkeen**

```powershell
python - << 'EOF'
import paramiko
c = paramiko.SSHClient(); c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
c.connect('192.168.31.1', port=222, username='root', password='Algal0n007', timeout=10)
_, o, _ = c.exec_command('/opt/bin/wg-monitor --version 2>&1 || /opt/etc/init.d/S99wg-monitor status')
print(o.read().decode())
c.close()
EOF
```

Expected: contains `0.8.0-tunnel-import`.

- [ ] **Step 7: End-to-end test — send conf to TG**

1. Export a `.conf` from the awg-manager web UI (http://192.168.31.1:2000) for any existing tunnel.
2. In TG, open the `testkeen` topic in the monitoring group.
3. Send the `.conf` file as a document.
4. Bot should reply: «📁 Конфиг для туннеля `awg11`. Что делать?» with buttons.
5. Tap «🔄 Заменить awg11» — bot acks «📤 Import (замена "awg11") поставлен в очередь».
6. Wait ~10s — bot sends result via Notifier: «✅ Туннель "awg11" создан...».

- [ ] **Step 8: Final commit + tag**

```
git -c user.email=asnekhaev@gmail.com add dist/.gitignore  # dist/ already gitignored
git -c user.email=asnekhaev@gmail.com commit -m "release: v0.8.0-tunnel-import" -- cmd/backend/main.go cmd/agent/main.go
git -c user.email=asnekhaev@gmail.com tag v0.8.0-tunnel-import
git push origin main --tags
```

---

## Self-Review

**Spec coverage check:**
- §2 Data flow ✅ Tasks 3-7 cover full flow
- §3 All 12 files ✅ All addressed across 8 tasks
- §4 WG conf parser ✅ Task 3
- §5 awg-manager API ✅ Task 2
- §6 HydraRoute rebinding ✅ Task 3 (ImportTunnel calls S99hrneo restart)
- §7 Pending state ✅ Task 7 (pendingUpload, pendingMu, storePending)
- §8 Callback format ✅ Task 6 parse + Task 7 sendImportConfirmation
- §9 Operation order (create first) ✅ Task 3 ImportTunnel
- §10 Security (per_router only, admin gate, 50KB limit) ✅ Task 7 handleDocumentUpload

**Placeholder scan:** No TBD/TODO. All code blocks are complete.

**Type consistency:**
- `pendingUpload` defined in Task 6 (actions.go), used in Task 7 (router.go) ✅
- `ImportAction.consumeFn` signature matches usage in NewRouterWithSink ✅
- `sanitizeTunnelName`/`isValidTunnelName` duplicated in agent (actions/) and backend (callbacks/) — intentional, different binaries ✅
- `tunnel_import_replace`/`tunnel_import_add` in parse.go validActions matches HandleCallback routing ✅
- `testConfB64` in runner_test is base64 of a valid minimal AWG conf — verify by decoding before running ✅

**Verify testConfB64 pre-flight:** Run this in PowerShell before Task 4:
```powershell
[System.Text.Encoding]::UTF8.GetString([System.Convert]::FromBase64String("W0ludGVyZmFjZV0KUHJpdmF0ZUtleSA9IEFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE9CkpjID0gNApKbWluID0gNDAKSm1heCA9IDcwClMxID0gMApTMiA9IDAKSDEgPSAxMTExMTExMTExCkgyID0gMjIyMjIyMjIyMgpIMyA9IDMzMzMzMzMzMzMKSDQgPSA0NDQ0NDQ0NDQ0CgpbUGVlcl0KUHVibGljS2V5ID0gQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQj0KRW5kcG9pbnQgPSB2cG4uZXhhbXBsZS5jb206NTE4MjAKQWxsb3dlZElQcyA9IDAuMC4wLjAvMAo="))
```
Should output a valid conf with `[Interface]` and `[Peer]` sections.
