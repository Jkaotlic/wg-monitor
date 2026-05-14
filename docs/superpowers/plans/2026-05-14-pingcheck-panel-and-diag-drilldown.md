# PingCheck Panel + Diag Drill-down — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface awg-manager PingCheck health + per-tunnel watchdog toggle + tap-to-expand failing diag tests inside the existing TG panel hub.

**Architecture:** New `kind=pingcheck` slot in the panel hub. New agent action pair (`pingcheck_status` read-only, `pingcheck_toggle` mutating with awg-mgr POST → ndmc fallback). Diag drill-down lives entirely backend-side, reuses cached raw JSON, extends the existing parser with a per-test detail extractor.

**Tech Stack:** Go 1.22, awg-manager REST (over HTTP, `X-Requested-With: XMLHttpRequest`), Keenetic ndmc CLI, internal `wire.Command`/`wire.CommandResult` envelope, internal `tg`/`alerts`/`callbacks` packages.

**Spec:** [`docs/superpowers/specs/2026-05-14-pingcheck-panel-and-diag-drilldown-design.md`](../specs/2026-05-14-pingcheck-panel-and-diag-drilldown-design.md)

---

## Task 1: Preflight verification (manual, no code)

**Why before any code:** spec acknowledges three unknowns that must be resolved against the live router before we commit to API shapes — mutating endpoints, ndmc syntax, and per-test JSON keys. Skipping this step is the path to "wrote the parser, then discovered the JSON is shaped differently."

**Tasks:**

- [ ] **Step 1: Discover awg-mgr per-tunnel PingCheck toggle endpoint**

Open `http://192.168.31.1:2222/pingcheck` in a browser. Open DevTools → Network. Tap any per-tunnel `Активен` slider in the modal. Capture:
- HTTP method, full URL, request body
- response body shape

Append findings to plan as a new "Confirmed endpoints" section below this task. If no POST is found (slider only persists in localStorage), record "no awg-mgr endpoint — ndmc-only" and Task 3's primary path is removed.

- [ ] **Step 2: Confirm ndmc syntax for per-tunnel ping-check on/off**

SSH to a non-critical router (testkeen). Read current state:

```sh
ndmc -c "show interface Wireguard0"
```

Look for a `ping-check` block. Try:

```sh
ndmc -c "no interface Wireguard0 ping-check"
ndmc -c "show interface Wireguard0"     # verify ping-check disappeared
ndmc -c "interface Wireguard0 ping-check profile <name>"   # restore
```

The exact command to attach a profile depends on the existing profile name — read it from `show running-config | grep ping-check` first.

Record exact working commands in "Confirmed endpoints" section.

- [ ] **Step 3: Capture a real diag JSON to know per-test keys**

Trigger a fresh diag and read the result:

```sh
curl -s -H 'X-Requested-With: XMLHttpRequest' -X POST http://192.168.31.1:2222/api/diagnostics/run
sleep 30
curl -s -H 'X-Requested-With: XMLHttpRequest' http://192.168.31.1:2222/api/diagnostics/result | tee /tmp/diag-real.json | jq .
```

Append to plan: under each top-level test category visible in the screenshots ("WAN up с gateway", "NDMS отвечает", per-tunnel "Резолв endpoint", "MTU интерфейса", "DNS leak проверка", etc.) — record the JSON path, status field name, and reason field name. Example shape:

```
{
  "wan": { "up": true, "gatewayReachable": true, ... },
  "ndms": { "responsive": true },
  "tunnels": {
    "awg10": {
      "endpoint": { "status": "ok", "resolved": "..." },
      "mtu": { "status": "fail", "current": 1280, "expected": 1380, "reason": "..." },
      ...
    }
  }
}
```

This drives Task 11's parser. Without this, Task 11 is guesswork.

- [ ] **Step 4: Update spec inline with confirmed endpoints**

Edit `docs/superpowers/specs/2026-05-14-pingcheck-panel-and-diag-drilldown-design.md`. Add an "Appendix A — Verified endpoints (2026-05-14)" section. Commit as `docs(spec): pingcheck/diag preflight findings`.

```bash
git add docs/superpowers/specs/2026-05-14-pingcheck-panel-and-diag-drilldown-design.md
git commit -m "docs(spec): pingcheck/diag preflight findings"
```

---

## Task 2: Agent — `pingcheck_status` action (read-only passthrough)

**Files:**
- Create: `internal/agent/actions/pingcheck.go`
- Create: `internal/agent/actions/pingcheck_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/agent/actions/pingcheck_test.go`:

```go
package actions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
)

func TestPingCheckStatus_PassesThroughJSON(t *testing.T) {
	const want = `{"success":true,"data":{"enabled":true,"tunnels":[{"tunnelId":"awg10","status":"alive","lastLatency":82,"failCount":0,"successCount":417,"failThreshold":3,"restartCount":0,"enabled":true,"tunnelRunning":true}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pingcheck/status" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With header")
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(want))
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	out, err := PingCheckStatusJSON(context.Background(), c)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "alive") {
		t.Errorf("expected status passthrough, got: %s", out)
	}
}

func TestPingCheckStatus_5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("upstream gone"))
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	_, err := PingCheckStatusJSON(context.Background(), c)
	if err == nil {
		t.Fatal("expected err on 503")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("err must mention status: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/agent/actions/ -run TestPingCheckStatus -v
```

Expected: FAIL with `undefined: PingCheckStatusJSON`.

- [ ] **Step 3: Implement minimal action**

Create `internal/agent/actions/pingcheck.go`:

```go
// Package-internal helpers for the pingcheck_status / pingcheck_toggle
// agent actions. Status is a JSON passthrough so the backend owns the
// rendering shape; toggle is two-tiered (awg-mgr POST primary, ndmc CLI
// fallback) — see Section 3 of the design spec.
package actions

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
)

// PingCheckStatusJSON returns the awg-mgr /api/pingcheck/status body
// re-serialised as a JSON envelope. We re-marshal so the backend
// always sees a stable shape (envelope shape is owned by awg-mgr; we
// just pass it through).
func PingCheckStatusJSON(ctx context.Context, c *awgmgr.Client) (string, error) {
	st, err := c.PingCheckStatus(ctx)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return "", fmt.Errorf("encode pingcheck status: %w", err)
	}
	return string(b), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/agent/actions/ -run TestPingCheckStatus -v
```

Expected: PASS for both cases.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/actions/pingcheck.go internal/agent/actions/pingcheck_test.go
git commit -m "feat(agent): pingcheck_status JSON passthrough action"
```

---

## Task 3: Agent — `pingcheck_toggle` action (POST primary + ndmc fallback)

**Files:**
- Modify: `internal/agent/actions/pingcheck.go`
- Modify: `internal/agent/actions/pingcheck_test.go`

**Note:** if Task 1 step 1 found no awg-mgr POST endpoint, **skip the primary-path branch** in this task — `PingCheckToggle` becomes ndmc-only and the fake httptest server in tests is dropped. Adjust the test list to match.

- [ ] **Step 1: Write failing tests for toggle**

Append to `internal/agent/actions/pingcheck_test.go`:

```go
import "errors"

func TestPingCheckToggle_PrimaryPathOK(t *testing.T) {
	var posted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: %s", r.Method)
		}
		// Path verified against Task 1 step 1 finding. Update if different.
		if r.URL.Path != "/api/pingcheck/toggle" {
			t.Errorf("path: %q", r.URL.Path)
		}
		posted = true
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	exec := ExecFunc(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("ndmc should NOT be called when POST succeeds; got %s %v", name, args)
		return nil, nil
	})
	if err := PingCheckToggle(context.Background(), c, exec, "awg10", "Wireguard0", false); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !posted {
		t.Error("primary-path POST was not attempted")
	}
}

func TestPingCheckToggle_PrimaryFailsFallbackOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte("not found"))
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	var ndmcCalled bool
	exec := ExecFunc(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ndmcCalled = true
		if name != "ndmc" {
			t.Errorf("expected ndmc, got %s", name)
		}
		// Exact ndmc syntax confirmed in Task 1 step 2.
		want := `no interface Wireguard0 ping-check`
		if len(args) < 2 || args[1] != want {
			t.Errorf("ndmc args mismatch: got %v, want -c %q", args, want)
		}
		return []byte("ok"), nil
	})
	if err := PingCheckToggle(context.Background(), c, exec, "awg10", "Wireguard0", false); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !ndmcCalled {
		t.Error("ndmc fallback was not invoked")
	}
}

func TestPingCheckToggle_BothFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	exec := ExecFunc(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("ndmc: interface unknown"), errors.New("exit 1")
	})
	err := PingCheckToggle(context.Background(), c, exec, "awg10", "Wireguard0", true)
	if err == nil {
		t.Fatal("expected aggregated err")
	}
	msg := err.Error()
	if !strings.Contains(msg, "POST") || !strings.Contains(msg, "ndmc") {
		t.Errorf("err must aggregate both paths: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```sh
go test ./internal/agent/actions/ -run TestPingCheckToggle -v
```

Expected: FAIL with `undefined: PingCheckToggle`.

- [ ] **Step 3: Implement toggle**

Append to `internal/agent/actions/pingcheck.go`:

```go
import (
	"bytes"
	"io"
	"net/http"
	"net/url"
)

// PingCheckToggle enables or disables the per-tunnel watchdog. Tries
// awg-mgr POST first (no body required — query params); on any error
// falls back to ndmc CLI. Returns aggregated err if both paths fail.
//
// tunnelID is the awg-mgr id ("awg10"); ndmsName is the Keenetic
// interface name ("Wireguard0"). Both are needed because the two
// paths address the tunnel differently.
func PingCheckToggle(ctx context.Context, c *awgmgr.Client, exec ExecFunc, tunnelID, ndmsName string, enable bool) error {
	primaryErr := primaryPingCheckToggle(ctx, c, tunnelID, enable)
	if primaryErr == nil {
		return nil
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
	flag := "0"
	if enable {
		flag = "1"
	}
	q := url.Values{"id": {tunnelID}, "enable": {flag}}
	// Reuse the client's HTTP transport but bypass GetEnv (we don't decode
	// the body — only care about status code).
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/api/pingcheck/toggle?"+q.Encode(), bytes.NewReader(nil))
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
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```sh
go test ./internal/agent/actions/ -run TestPingCheckToggle -v
```

Expected: PASS for all three subcases.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/actions/pingcheck.go internal/agent/actions/pingcheck_test.go
git commit -m "feat(agent): pingcheck_toggle with POST primary + ndmc fallback"
```

---

## Task 4: Agent — wire actions into Runner dispatch

**Files:**
- Modify: `internal/agent/actions/runner.go` (add 2 dispatch cases at the end of switch)

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/actions/runner_test.go` (file already exists with similar patterns):

```go
func TestRunner_PingCheckStatus_Dispatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pingcheck/status" {
			t.Errorf("path: %q", r.URL.Path)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true,"data":{"enabled":true,"tunnels":[]}}`))
	}))
	defer srv.Close()
	r := &Runner{AwgClient: awgmgr.New(srv.URL), Now: time.Now}
	res := r.Execute(context.Background(), wire.Command{ID: "x", Action: "pingcheck_status"})
	if res.Status != "ok" {
		t.Fatalf("status: %s output=%s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "tunnels") {
		t.Errorf("expected tunnels in output: %s", res.Output)
	}
}

func TestRunner_PingCheckToggle_Dispatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	exec := ExecFunc(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("primary should win, ndmc not called")
		return nil, nil
	})
	r := &Runner{AwgClient: awgmgr.New(srv.URL), Exec: exec, Now: time.Now}
	res := r.Execute(context.Background(), wire.Command{
		ID: "y", Action: "pingcheck_toggle",
		Args: map[string]any{"tunnel_id": "awg10", "ndms_name": "Wireguard0", "enable": false},
	})
	if res.Status != "ok" {
		t.Fatalf("status: %s output=%s", res.Status, res.Output)
	}
}

func TestRunner_PingCheckToggle_MissingArgs(t *testing.T) {
	r := &Runner{AwgClient: awgmgr.New("http://unused"), Exec: ExecFunc(func(ctx context.Context, name string, a ...string) ([]byte, error) { return nil, nil }), Now: time.Now}
	res := r.Execute(context.Background(), wire.Command{ID: "z", Action: "pingcheck_toggle", Args: map[string]any{}})
	if res.Status != "err" {
		t.Errorf("expected err on missing args, got %s", res.Status)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```sh
go test ./internal/agent/actions/ -run TestRunner_PingCheck -v
```

Expected: FAIL with "unknown action: pingcheck_status".

- [ ] **Step 3: Add dispatch cases**

In `internal/agent/actions/runner.go`, find the `switch cmd.Action` block in `dispatchWithPayload` (around line 139), and add two new cases just before the existing `case "tunnel_enable", "tunnel_disable":` (so all the awgmgr-only read paths cluster together):

```go
	case "pingcheck_status":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		body, err := PingCheckStatusJSON(ctx, r.AwgClient)
		if err != nil {
			return "err", err.Error(), payload
		}
		return "ok", body, payload
	case "pingcheck_toggle":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured", payload
		}
		if r.Exec == nil {
			return "err", "exec not configured", payload
		}
		tid, _ := cmd.Args["tunnel_id"].(string)
		ndms, _ := cmd.Args["ndms_name"].(string)
		enable, _ := cmd.Args["enable"].(bool)
		if tid == "" || ndms == "" {
			return "err", "pingcheck_toggle: tunnel_id and ndms_name are required", payload
		}
		if err := PingCheckToggle(ctx, r.AwgClient, r.Exec, tid, ndms, enable); err != nil {
			return "err", err.Error(), payload
		}
		return "ok", fmt.Sprintf("pingcheck %s for %s", boolEnableLabel(enable), tid), payload
```

Add the small helper at bottom of `runner.go`:

```go
func boolEnableLabel(enable bool) string {
	if enable {
		return "enabled"
	}
	return "disabled"
}
```

Update the package doc comment at the top of `runner.go` to list the new actions:

```go
//   - pingcheck_status → awgmgr.PingCheckStatus → JSON passthrough
//   - pingcheck_toggle → awg-mgr POST /api/pingcheck/toggle (primary)
//     with ndmc CLI fallback (interface <ndms_name> ping-check)
```

- [ ] **Step 4: Run tests to verify they pass**

```sh
go test ./internal/agent/actions/ -run TestRunner_PingCheck -v
```

Expected: PASS for all three.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/actions/runner.go internal/agent/actions/runner_test.go
git commit -m "feat(agent): wire pingcheck_status / pingcheck_toggle into Runner dispatch"
```

---

## Task 5: TG renderer — `PingCheckPanelText`

**Files:**
- Create: `internal/backend/tg/pingcheck_panel.go`
- Create: `internal/backend/tg/pingcheck_panel_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/backend/tg/pingcheck_panel_test.go`:

```go
package tg

import (
	"strings"
	"testing"
)

func TestPingCheckPanelText_Empty(t *testing.T) {
	got := PingCheckPanelText("router1", true, nil)
	if !strings.Contains(got, "Туннелей не обнаружено") {
		t.Errorf("expected empty-state, got: %s", got)
	}
}

func TestPingCheckPanelText_AliveAndDead(t *testing.T) {
	entries := []PingCheckPanelEntry{
		{TunnelID: "awg10", Name: "amst", Status: "alive", PerTunnelEnabled: true, LastLatencyMs: 82, SuccessCount: 417, FailCount: 0, FailThreshold: 3, RestartCount: 0},
		{TunnelID: "awg11", Name: "fra", Status: "dead", PerTunnelEnabled: true, LastLatencyMs: 0, SuccessCount: 5, FailCount: 3, FailThreshold: 3, RestartCount: 7},
	}
	got := PingCheckPanelText("router1", true, entries)
	for _, want := range []string{"router1", "🟢", "🔴", "amst", "fra", "82ms", "---", "✓417", "✓5", "✗0/3", "✗3/3", "restart×0", "restart×7", "⚠"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestPingCheckPanelText_PerTunnelDisabled(t *testing.T) {
	entries := []PingCheckPanelEntry{
		{TunnelID: "awg10", Name: "amst", Status: "alive", PerTunnelEnabled: false, LastLatencyMs: 0},
	}
	got := PingCheckPanelText("router1", true, entries)
	if !strings.Contains(got, "⏸") {
		t.Errorf("disabled tunnel must use ⏸: %s", got)
	}
}

func TestPingCheckPanelText_GloballyDisabled(t *testing.T) {
	entries := []PingCheckPanelEntry{{TunnelID: "awg10", Name: "amst", Status: "alive", PerTunnelEnabled: true, LastLatencyMs: 50}}
	got := PingCheckPanelText("router1", false, entries)
	if !strings.Contains(got, "Глобально: ⏸") {
		t.Errorf("must show global disabled banner: %s", got)
	}
}

func TestPingCheckPanelText_LongSuccessCount(t *testing.T) {
	entries := []PingCheckPanelEntry{{TunnelID: "awg10", Name: "amst", Status: "alive", PerTunnelEnabled: true, LastLatencyMs: 50, SuccessCount: 12500}}
	got := PingCheckPanelText("router1", true, entries)
	if !strings.Contains(got, "✓12.5k") {
		t.Errorf("expected k-suffix for >9999, got: %s", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```sh
go test ./internal/backend/tg/ -run TestPingCheckPanelText -v
```

Expected: FAIL with `undefined: PingCheckPanelText` / `PingCheckPanelEntry`.

- [ ] **Step 3: Implement renderer**

Create `internal/backend/tg/pingcheck_panel.go`:

```go
package tg

import (
	"fmt"
	"strings"
)

// PingCheckPanelEntry — one row for the PingCheck Panel renderer.
//
// PerTunnelEnabled mirrors awg-mgr's per-tunnel watchdog flag (independent
// of the tunnel's own enabled flag). Status comes from awg-mgr
// ("alive"/"dead"/empty); LastLatencyMs == 0 renders as "---".
type PingCheckPanelEntry struct {
	TunnelID         string // "awg10" — used in callback_data for toggle
	Name             string // "amst" — display label
	NDMSName         string // "Wireguard0" — packed into toggle callback_data
	Status           string // "alive" | "dead" | ""
	PerTunnelEnabled bool   // false → ⏸ icon, watchdog suspended for this tunnel
	LastLatencyMs    int    // 0 → "---"
	SuccessCount     int64
	FailCount        int
	FailThreshold    int
	RestartCount     int
}

// PingCheckPanelText renders the message body. globalEnabled is the
// /api/pingcheck/status .data.enabled flag — false → grey banner.
func PingCheckPanelText(nickname string, globalEnabled bool, entries []PingCheckPanelEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📡 PingCheck — %s\n", nickname)
	if len(entries) == 0 {
		b.WriteString("\nТуннелей не обнаружено — PingCheck не отчитался.")
		return b.String()
	}
	b.WriteString("\n")
	for _, e := range entries {
		b.WriteString(formatPingCheckRow(e))
		b.WriteString("\n")
	}
	b.WriteString("\nГлобально: ")
	if globalEnabled {
		b.WriteString("✅ enabled")
	} else {
		b.WriteString("⏸ disabled")
	}
	return b.String()
}

func formatPingCheckRow(e PingCheckPanelEntry) string {
	icon := "❓"
	switch {
	case !e.PerTunnelEnabled:
		icon = "⏸"
	case e.Status == "alive":
		icon = "🟢"
	case e.Status == "dead":
		icon = "🔴"
	}
	lat := "---"
	if e.LastLatencyMs > 0 {
		lat = fmt.Sprintf("%dms", e.LastLatencyMs)
	}
	warn := ""
	if e.RestartCount > 5 {
		warn = " ⚠"
	}
	name := e.Name
	if name == "" {
		name = e.TunnelID
	}
	return fmt.Sprintf("%s %s  %s  ✓%s  ✗%d/%d   restart×%d%s",
		icon, name, lat, formatCount(e.SuccessCount), e.FailCount, e.FailThreshold, e.RestartCount, warn)
}

// formatCount renders 0..9999 as plain int; >=10000 as "12.5k" (one decimal).
func formatCount(n int64) string {
	if n < 10000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```sh
go test ./internal/backend/tg/ -run TestPingCheckPanelText -v
```

Expected: PASS for all five.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/tg/pingcheck_panel.go internal/backend/tg/pingcheck_panel_test.go
git commit -m "feat(tg): PingCheckPanelText renderer"
```

---

## Task 6: TG renderer — `PingCheckPanelKeyboard`

**Files:**
- Modify: `internal/backend/tg/pingcheck_panel.go`
- Modify: `internal/backend/tg/pingcheck_panel_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/backend/tg/pingcheck_panel_test.go`:

```go
func TestPingCheckPanelKeyboard_Layout(t *testing.T) {
	entries := []PingCheckPanelEntry{
		{TunnelID: "awg10", Name: "amst", NDMSName: "Wireguard0", PerTunnelEnabled: true},
		{TunnelID: "awg11", Name: "fra", NDMSName: "Wireguard1", PerTunnelEnabled: false},
	}
	kb := PingCheckPanelKeyboard(42, entries)
	if len(kb.InlineKeyboard) < 3 {
		t.Fatalf("expected at least 3 rows, got %d", len(kb.InlineKeyboard))
	}
	// Row 0: per-tunnel toggles
	row0 := kb.InlineKeyboard[0]
	if len(row0) != 2 {
		t.Errorf("toggle row should have 2 buttons, got %d", len(row0))
	}
	// awg10 is enabled → button shows ⏸ (would disable on tap)
	if !strings.Contains(row0[0].Text, "⏸") {
		t.Errorf("enabled tunnel should show ⏸ to disable, got %q", row0[0].Text)
	}
	// awg11 is disabled → button shows ▶ (would enable on tap)
	if !strings.Contains(row0[1].Text, "▶") {
		t.Errorf("disabled tunnel should show ▶ to enable, got %q", row0[1].Text)
	}
	// Callback data shape: pingcheck_toggle:42:awg10:Wireguard0:0
	wantCB0 := "pingcheck_toggle:42:awg10:Wireguard0:0"
	if row0[0].CallbackData != wantCB0 {
		t.Errorf("toggle cb mismatch: got %q want %q", row0[0].CallbackData, wantCB0)
	}
	wantCB1 := "pingcheck_toggle:42:awg11:Wireguard1:1"
	if row0[1].CallbackData != wantCB1 {
		t.Errorf("toggle cb mismatch: got %q want %q", row0[1].CallbackData, wantCB1)
	}
}

func TestPingCheckPanelKeyboard_GlobalControls(t *testing.T) {
	kb := PingCheckPanelKeyboard(42, nil)
	// Even with no tunnels, global controls + close should appear.
	flat := []string{}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			flat = append(flat, b.CallbackData)
		}
	}
	for _, want := range []string{"pingcheck_now:42:_menu", "pingcheck_open:42:_panel_", "panel:0:help:pingcheck", "routes_close:42:_panel_"} {
		found := false
		for _, c := range flat {
			if c == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing callback_data %q; got %v", want, flat)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```sh
go test ./internal/backend/tg/ -run TestPingCheckPanelKeyboard -v
```

Expected: FAIL with `undefined: PingCheckPanelKeyboard`.

- [ ] **Step 3: Implement keyboard**

Append to `internal/backend/tg/pingcheck_panel.go`:

```go
// PingCheckPanelKeyboard builds the inline keyboard for the PingCheck
// Panel. callback_data shapes:
//   pingcheck_toggle:<userID>:<tunnel_id>:<ndms_name>:<0|1>   ← per-tunnel
//   pingcheck_now:<userID>:_menu                              ← global "check now"
//   pingcheck_open:<userID>:_panel_                           ← refresh self
//   routes_close:<userID>:_panel_                             ← close (reuse pattern)
//   panel:0:help:pingcheck                                    ← help screen
//
// Toggle icon meaning: shown icon = action that *would* happen on tap.
// Enabled tunnel → ⏸ button (disable on tap); disabled tunnel → ▶ button
// (enable on tap).
const pingcheckMaxPerRow = 8

func PingCheckPanelKeyboard(userID int64, entries []PingCheckPanelEntry) InlineKeyboardMarkup {
	rows := [][]InlineKeyboardButton{}

	var row []InlineKeyboardButton
	for _, e := range entries {
		var icon, flag string
		if e.PerTunnelEnabled {
			icon, flag = "⏸", "0"
		} else {
			icon, flag = "▶", "1"
		}
		label := e.Name
		if label == "" {
			label = e.TunnelID
		}
		row = append(row, InlineKeyboardButton{
			Text:         fmt.Sprintf("%s %s", icon, label),
			CallbackData: fmt.Sprintf("pingcheck_toggle:%d:%s:%s:%s", userID, e.TunnelID, e.NDMSName, flag),
		})
		if len(row) >= pingcheckMaxPerRow {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	rows = append(rows, []InlineKeyboardButton{
		{Text: "▶ Проверить сейчас", CallbackData: fmt.Sprintf("pingcheck_now:%d:_menu", userID)},
		{Text: "🔄 Обновить", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", userID)},
	})
	rows = append(rows, []InlineKeyboardButton{
		{Text: "ℹ Помощь", CallbackData: "panel:0:help:pingcheck"},
		{Text: "✖ Закрыть", CallbackData: fmt.Sprintf("routes_close:%d:_panel_", userID)},
	})
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}
```

- [ ] **Step 4: Run tests to verify they pass**

```sh
go test ./internal/backend/tg/ -run TestPingCheckPanelKeyboard -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/tg/pingcheck_panel.go internal/backend/tg/pingcheck_panel_test.go
git commit -m "feat(tg): PingCheckPanelKeyboard with per-tunnel toggle"
```

---

## Task 7: Parse — register new callback prefixes

**Files:**
- Modify: `internal/backend/callbacks/parse.go`
- Modify: `internal/backend/callbacks/parse_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/backend/callbacks/parse_test.go`:

```go
func TestParse_PingCheckOpen(t *testing.T) {
	a, err := Parse("pingcheck_open:42:_panel_")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.Action != "pingcheck_open" || a.UserID != 42 || !a.IsPanel {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PingCheckToggle_OK(t *testing.T) {
	a, err := Parse("pingcheck_toggle:42:awg10:Wireguard0:0")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.Action != "pingcheck_toggle" || a.UserID != 42 ||
		a.PingCheckTunnelID != "awg10" || a.NDMSName != "Wireguard0" || a.PingCheckEnable != false {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PingCheckToggle_RejectsBadNDMS(t *testing.T) {
	_, err := Parse("pingcheck_toggle:42:awg10:bad name with spaces:1")
	if err == nil {
		t.Error("expected validation err on space-containing ndms_name")
	}
}

func TestParse_DiagTest(t *testing.T) {
	a, err := Parse("diag_test:42:abcd1234:mtu")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.Action != "diag_test" || a.DiagRawToken != "abcd1234" || a.DiagTestID != "mtu" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelKindPingCheck(t *testing.T) {
	a, err := Parse("panel:0:kind:pingcheck")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.PanelKind != "pingcheck" {
		t.Errorf("got %+v", a)
	}
}

func TestParse_PanelHelpPingCheck(t *testing.T) {
	a, err := Parse("panel:0:help:pingcheck")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.PanelKind != "pingcheck" {
		t.Errorf("got %+v", a)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```sh
go test ./internal/backend/callbacks/ -run TestParse_PingCheck -v
go test ./internal/backend/callbacks/ -run TestParse_DiagTest -v
go test ./internal/backend/callbacks/ -run TestParse_Panel.*PingCheck -v
```

Expected: FAIL — actions not in whitelist; new fields undefined.

- [ ] **Step 3: Extend Args struct**

In `internal/backend/callbacks/parse.go`, add three new fields to `Args` (alongside `DiagRawToken`):

```go
	// PingCheckTunnelID is the awg-mgr tunnel id ("awg10") in
	// pingcheck_toggle callbacks. Empty for any other action.
	PingCheckTunnelID string
	// PingCheckEnable is the bool transported in the 5th colon-segment
	// of pingcheck_toggle ("0" → false, "1" → true).
	PingCheckEnable bool
	// DiagTestID is the short slug ("mtu", "dns_leak", ...) identifying
	// which test was tapped on a diag drill-down. Set for diag_test action.
	DiagTestID string
```

- [ ] **Step 4: Whitelist new actions**

In the same file, in the `validActions` map literal:

```go
	// pingcheck panel: monitor + per-tunnel watchdog toggle.
	"pingcheck_open": true, "pingcheck_toggle": true,
	// diag drill-down: tap a failing test in a diag summary.
	"diag_test": true,
```

- [ ] **Step 5: Add parse branches**

In `Parse()`, add new switch cases inside the existing `switch action` block (the one near the end with `routes_pick`/`maint_restart`/etc.):

```go
	case "pingcheck_toggle":
		if len(parts) < 5 {
			return Args{}, fmt.Errorf("pingcheck_toggle requires tunnel_id, ndms_name, enable: %q", data)
		}
		// parts[2] is CheckName (already set above); for this action it
		// carries the awg-mgr tunnel id.
		a.PingCheckTunnelID = parts[2]
		if !ndmsNameRe.MatchString(parts[3]) {
			return Args{}, fmt.Errorf("pingcheck_toggle: ndms_name %q must match ^[A-Za-z0-9_-]{1,32}$", parts[3])
		}
		a.NDMSName = parts[3]
		switch parts[4] {
		case "0":
			a.PingCheckEnable = false
		case "1":
			a.PingCheckEnable = true
		default:
			return Args{}, fmt.Errorf("pingcheck_toggle: enable must be 0 or 1, got %q", parts[4])
		}
	case "diag_test":
		if len(parts) < 4 || parts[3] == "" {
			return Args{}, fmt.Errorf("diag_test requires cache_token and test_id: %q", data)
		}
		a.DiagRawToken = parts[2]
		a.DiagTestID = parts[3]
```

- [ ] **Step 6: Allow `pingcheck` in panel kind + help-screen whitelists**

Find the `validKinds := map[string]bool{...}` line inside the `if action == "panel"` block. Add `"pingcheck": true`. Same for `validHelpScreens`.

```go
		validKinds := map[string]bool{"maint": true, "routes": true, "status": true, "pingcheck": true}
```

```go
		validHelpScreens := map[string]bool{
			"maint": true, "routes": true, "tunnels": true,
			"access": true, "diag": true, "status": true, "pingcheck": true,
		}
```

- [ ] **Step 7: Run tests to verify they pass**

```sh
go test ./internal/backend/callbacks/ -run TestParse -v
```

Expected: PASS for new tests; existing tests unchanged.

- [ ] **Step 8: Commit**

```bash
git add internal/backend/callbacks/parse.go internal/backend/callbacks/parse_test.go
git commit -m "feat(callbacks): parse pingcheck_open/toggle + diag_test + panel kind=pingcheck"
```

---

## Task 8: Backend — `PingCheckPanelNotifier`

**Files:**
- Create: `internal/backend/callbacks/pingcheck_panel.go`
- Create: `internal/backend/callbacks/pingcheck_panel_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/backend/callbacks/pingcheck_panel_test.go`:

```go
package callbacks

import (
	"context"
	"errors"
	"testing"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

type fakeEditTG struct {
	lastChatID int64
	lastMsgID  int64
	lastText   string
	lastKb     *tg.InlineKeyboardMarkup
	editErr    error
}

func (f *fakeEditTG) EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error {
	f.lastChatID = chatID
	f.lastMsgID = msgID
	f.lastText = text
	f.lastKb = kb
	return f.editErr
}

func TestPingCheckPanelNotifier_Status_OK(t *testing.T) {
	d := db.NewMemForTest(t) // existing helper; if name differs check db_test.go
	u, _ := d.Users().Create(&db.User{Nickname: "router1"})
	tgFake := &fakeEditTG{}
	n := &PingCheckPanelNotifier{TG: tgFake, DB: d}

	body := `{"enabled":true,"tunnels":[{"tunnelId":"awg10","tunnelName":"amst","enabled":true,"status":"alive","lastLatency":82,"failCount":0,"successCount":417,"failThreshold":3,"restartCount":0,"tunnelRunning":true}]}`
	res := wire.CommandResult{Status: "ok", Output: body}
	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "pingcheck_status"}

	if err := n.NotifyCommandResult(context.Background(), ref, res, u.ID); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if tgFake.lastChatID != 100 || tgFake.lastMsgID != 200 {
		t.Errorf("edit target wrong: %+v", tgFake)
	}
	for _, want := range []string{"📡 PingCheck", "router1", "amst", "82ms", "🟢"} {
		if !contains(tgFake.lastText, want) {
			t.Errorf("missing %q in:\n%s", want, tgFake.lastText)
		}
	}
}

func TestPingCheckPanelNotifier_Status_AgentErr(t *testing.T) {
	d := db.NewMemForTest(t)
	u, _ := d.Users().Create(&db.User{Nickname: "router1"})
	tgFake := &fakeEditTG{}
	n := &PingCheckPanelNotifier{TG: tgFake, DB: d}

	res := wire.CommandResult{Status: "err", Output: "HTTP_REFUSED: dial tcp"}
	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "pingcheck_status"}

	if err := n.NotifyCommandResult(context.Background(), ref, res, u.ID); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !contains(tgFake.lastText, "агент не ответил") {
		t.Errorf("expected err banner, got: %s", tgFake.lastText)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || (len(s) > 0 && (indexOf(s, substr) >= 0)))
}
func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
```

(Note: if `db.NewMemForTest` doesn't exist, look at `internal/backend/db/db_test.go` for the actual helper name and fix the import.)

- [ ] **Step 2: Run tests to verify they fail**

```sh
go test ./internal/backend/callbacks/ -run TestPingCheckPanelNotifier -v
```

Expected: FAIL with `undefined: PingCheckPanelNotifier`.

- [ ] **Step 3: Implement notifier**

Create `internal/backend/callbacks/pingcheck_panel.go`:

```go
// pingcheck_panel.go — backend rendering of the PingCheck monitor panel
// (design spec section 2). Two responsibilities:
//   - PingCheckPanelNotifier: handles pingcheck_status / pingcheck_toggle
//     CommandResults from the agent, edits the panel message in place
//   - PingCheckOpenAction / PingCheckToggleAction: callback handlers
//     for the inline-keyboard taps
package callbacks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/internal/backend/alerts"
	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

// PingCheckEditTG is the subset of tg.Client used by PingCheckPanelNotifier.
type PingCheckEditTG interface {
	EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error
}

// PingCheckPanelNotifier renders pingcheck_status and pingcheck_toggle
// CommandResults into the original panel message. Stateless aside from
// the tg/db handles.
type PingCheckPanelNotifier struct {
	TG PingCheckEditTG
	DB *db.DB
}

// NotifyCommandResult dispatches on ref.Action. Returns nil for actions
// not owned by this notifier (caller falls through to TGNotifier).
func (n *PingCheckPanelNotifier) NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, userID int64) error {
	user, err := n.DB.Users().GetByID(userID)
	if err != nil || user == nil {
		return fmt.Errorf("user lookup: %w", err)
	}
	switch ref.Action {
	case "pingcheck_status":
		return n.renderStatus(ctx, ref, res, user)
	case "pingcheck_toggle":
		return n.renderToggle(ctx, ref, res, user)
	default:
		return fmt.Errorf("PingCheckPanelNotifier: unsupported action %q", ref.Action)
	}
}

func (n *PingCheckPanelNotifier) renderStatus(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	if res.Status != "ok" {
		return n.renderErr(ctx, ref, user, res.Output, "Не удалось прочитать PingCheck")
	}
	entries, globalEnabled, err := decodePingCheckStatus(res.Output)
	if err != nil {
		return n.renderErr(ctx, ref, user, err.Error(), "Не удалось распарсить ответ awg-mgr")
	}
	text := tg.PingCheckPanelText(user.Nickname, globalEnabled, entries)
	kb := tg.PingCheckPanelKeyboard(user.ID, entries)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

func (n *PingCheckPanelNotifier) renderToggle(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	// Banner-only render. The follow-up pingcheck_status (auto-enqueued
	// by the toggle handler) will replace the panel body on its own
	// CommandResult. So here we just drop a one-line banner above the
	// existing message text.
	prefix := "✅ Переключение применено"
	if res.Status != "ok" {
		card := alerts.Card{
			Badge:   "❌",
			Label:   "Не удалось переключить PingCheck",
			Summary: res.Output,
		}
		summary, hint := alerts.HintFor("pingcheck_toggle", res.Output)
		card.Summary = summary
		card.Hint = hint
		prefix = card.Render(alerts.CardOpts{MaxBytes: 800})
	}
	// Append banner; keyboard stays the same as last render (we don't
	// have it in hand here — passing nil would clear it). Realistic
	// path: the auto-refresh that follows will rebuild the keyboard
	// from fresh status. So we just edit the text and keep going.
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, prefix, "", nil)
}

func (n *PingCheckPanelNotifier) renderErr(ctx context.Context, ref cmdpkg.MessageRef, user *db.User, errOut, label string) error {
	summary, hint := alerts.HintFor("pingcheck_status", errOut)
	card := alerts.Card{
		Badge:   "❌",
		Label:   fmt.Sprintf("📡 PingCheck — %s — агент не ответил", user.Nickname),
		Summary: summary,
		Hint:    hint,
	}
	body := card.Render(alerts.CardOpts{MaxBytes: 3500})
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🔄 Повторить", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", user.ID)},
	}}}
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, body, "", &kb)
}

// decodePingCheckStatus converts the awg-mgr passthrough JSON into the
// renderer entry shape. Sorted by tunnel name for stable order.
func decodePingCheckStatus(body string) ([]tg.PingCheckPanelEntry, bool, error) {
	var st awgmgr.PingCheckStatus
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		return nil, false, fmt.Errorf("decode pingcheck status: %w", err)
	}
	entries := make([]tg.PingCheckPanelEntry, 0, len(st.Tunnels))
	for _, t := range st.Tunnels {
		entries = append(entries, tg.PingCheckPanelEntry{
			TunnelID:         t.TunnelID,
			Name:             t.TunnelName,
			Status:           t.Status,
			PerTunnelEnabled: t.Enabled,
			LastLatencyMs:    t.LastLatency,
			SuccessCount:     t.SuccessCount,
			FailCount:        t.FailCount,
			FailThreshold:    t.FailThreshold,
			RestartCount:     t.RestartCount,
			// NDMSName is not in PingCheckTunnel — must be cross-referenced
			// from /api/tunnels/all by tunnel id. For v1 we accept that
			// toggle from this panel will require either the cache or a
			// second roundtrip; see Task 9 step 3.
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, st.Enabled, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```sh
go test ./internal/backend/callbacks/ -run TestPingCheckPanelNotifier -v
```

Expected: PASS for both. (NDMSName is empty in the entries — that's expected; Task 9 fills it.)

- [ ] **Step 5: Commit**

```bash
git add internal/backend/callbacks/pingcheck_panel.go internal/backend/callbacks/pingcheck_panel_test.go
git commit -m "feat(callbacks): PingCheckPanelNotifier renders status + err"
```

---

## Task 9: Backend — open/toggle handlers + in-flight store + NDMS lookup

**Files:**
- Modify: `internal/backend/callbacks/pingcheck_panel.go`
- Modify: `internal/backend/callbacks/pingcheck_panel_test.go`
- Modify: `internal/backend/callbacks/router.go` (instantiate handlers + register notifier — done in Task 10)

- [ ] **Step 1: Write failing tests for handlers + in-flight protection**

Append to `internal/backend/callbacks/pingcheck_panel_test.go`:

```go
import (
	"sync"
	"time"
)

type fakeEnqueuer struct {
	mu       sync.Mutex
	commands []wire.Command
	refs     []cmdpkg.MessageRef
}

func (f *fakeEnqueuer) Enqueue(userID int64, cmd wire.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, cmd)
	f.refs = append(f.refs, cmdpkg.MessageRef{})
	return nil
}
func (f *fakeEnqueuer) EnqueueWithRef(userID int64, cmd wire.Command, ref cmdpkg.MessageRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, cmd)
	f.refs = append(f.refs, ref)
	return nil
}

func TestPingCheckOpen_EnqueuesStatus(t *testing.T) {
	sink := &fakeEnqueuer{}
	a := NewPingCheckOpenAction(sink, defaultCmdID)
	q := &tg.CallbackQuery{ID: "qid", Message: &tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "pingcheck_open", UserID: 7, IsPanel: true}
	if _, err := a.Apply(context.Background(), q, args); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(sink.commands) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(sink.commands))
	}
	if sink.commands[0].Action != "pingcheck_status" {
		t.Errorf("got action %q", sink.commands[0].Action)
	}
	if sink.refs[0].Action != "pingcheck_status" {
		t.Errorf("ref must carry action for notifier dispatch")
	}
}

func TestPingCheckToggle_EnqueuesAndRefreshes(t *testing.T) {
	sink := &fakeEnqueuer{}
	store := newPingCheckInflightStore()
	a := NewPingCheckToggleAction(sink, store, defaultCmdID)
	q := &tg.CallbackQuery{ID: "qid", Message: &tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "pingcheck_toggle", UserID: 7, PingCheckTunnelID: "awg10", NDMSName: "Wireguard0", PingCheckEnable: false}
	if _, err := a.Apply(context.Background(), q, args); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// One toggle + one auto-refresh
	if len(sink.commands) != 2 {
		t.Fatalf("expected 2 enqueues (toggle + status), got %d", len(sink.commands))
	}
	if sink.commands[0].Action != "pingcheck_toggle" {
		t.Errorf("first should be toggle, got %q", sink.commands[0].Action)
	}
	if sink.commands[1].Action != "pingcheck_status" {
		t.Errorf("second should be status auto-refresh, got %q", sink.commands[1].Action)
	}
	// Toggle args carry tunnel_id, ndms_name, enable
	got := sink.commands[0].Args
	if got["tunnel_id"] != "awg10" || got["ndms_name"] != "Wireguard0" || got["enable"] != false {
		t.Errorf("toggle args wrong: %+v", got)
	}
}

func TestPingCheckToggle_DupTapBlocked(t *testing.T) {
	sink := &fakeEnqueuer{}
	store := newPingCheckInflightStore()
	a := NewPingCheckToggleAction(sink, store, defaultCmdID)
	q := &tg.CallbackQuery{ID: "qid", Message: &tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "pingcheck_toggle", UserID: 7, PingCheckTunnelID: "awg10", NDMSName: "Wireguard0", PingCheckEnable: false}
	_, _ = a.Apply(context.Background(), q, args)
	// Immediate second tap → dup
	_, err := a.Apply(context.Background(), q, args)
	if err == nil || !strings.Contains(err.Error(), "уже выполняется") {
		t.Fatalf("expected dup err, got %v", err)
	}
	// First tap = 2 enqueues; second tap should add nothing.
	if len(sink.commands) != 2 {
		t.Errorf("dup must not enqueue; got %d total", len(sink.commands))
	}
}

func TestPingCheckInflightStore_TTLEvicts(t *testing.T) {
	s := newPingCheckInflightStore()
	if !s.tryClaim(7, "awg10", 10*time.Millisecond) {
		t.Fatal("first claim must succeed")
	}
	if s.tryClaim(7, "awg10", 10*time.Millisecond) {
		t.Fatal("second claim within TTL must fail")
	}
	time.Sleep(20 * time.Millisecond)
	if !s.tryClaim(7, "awg10", 10*time.Millisecond) {
		t.Fatal("after TTL the slot must be free again")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```sh
go test ./internal/backend/callbacks/ -run TestPingCheck -v
```

Expected: FAIL — `NewPingCheckOpenAction` / `NewPingCheckToggleAction` / `newPingCheckInflightStore` undefined.

- [ ] **Step 3: Implement handlers + in-flight store**

Append to `internal/backend/callbacks/pingcheck_panel.go`:

```go
import (
	"errors"
	"sync"
	"time"
)

// pingcheckInflightStore guards against double-tap toggle. Per (userID,
// tunnelID) → claimed-until timestamp. 5-second window: long enough for
// the agent roundtrip + auto-refresh, short enough that no operator
// notices it during normal use.
type pingcheckInflightStore struct {
	mu sync.Mutex
	m  map[pingcheckInflightKey]time.Time
}

type pingcheckInflightKey struct {
	UserID   int64
	TunnelID string
}

func newPingCheckInflightStore() *pingcheckInflightStore {
	return &pingcheckInflightStore{m: make(map[pingcheckInflightKey]time.Time)}
}

// tryClaim returns true iff the slot was free; stores expiry on success.
// Lazy eviction on read.
func (s *pingcheckInflightStore) tryClaim(userID int64, tunnelID string, ttl time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := pingcheckInflightKey{userID, tunnelID}
	now := time.Now()
	if until, ok := s.m[k]; ok {
		if now.Before(until) {
			return false
		}
	}
	s.m[k] = now.Add(ttl)
	return true
}

const pingcheckInflightTTL = 5 * time.Second

// PingCheckOpenAction enqueues a pingcheck_status command on every
// pingcheck_open / refresh tap.
type PingCheckOpenAction struct {
	sink  CommandEnqueuer
	idGen func() string
}

func NewPingCheckOpenAction(sink CommandEnqueuer, idGen func() string) *PingCheckOpenAction {
	if idGen == nil {
		idGen = defaultCmdID
	}
	return &PingCheckOpenAction{sink: sink, idGen: idGen}
}

func (a *PingCheckOpenAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	if a.sink == nil {
		return "", errors.New("command channel disabled")
	}
	cmd := wire.Command{
		ID:       a.idGen(),
		Action:   "pingcheck_status",
		Args:     map[string]any{},
		IssuedAt: time.Now().UTC(),
	}
	ref := cmdpkg.MessageRef{
		ChatID:    q.Message.Chat.ID,
		MessageID: q.Message.MessageID,
		ThreadID:  q.Message.MessageThreadID,
		Action:    "pingcheck_status",
	}
	if err := a.sink.EnqueueWithRef(args.UserID, cmd, ref); err != nil {
		return "", fmt.Errorf("enqueue pingcheck_status: %w", err)
	}
	return "📡 обновляю…", nil
}

// PingCheckToggleAction enqueues pingcheck_toggle followed by an
// auto-refresh pingcheck_status. Dup-protected via inflight store.
type PingCheckToggleAction struct {
	sink     CommandEnqueuer
	inflight *pingcheckInflightStore
	idGen    func() string
}

func NewPingCheckToggleAction(sink CommandEnqueuer, inflight *pingcheckInflightStore, idGen func() string) *PingCheckToggleAction {
	if idGen == nil {
		idGen = defaultCmdID
	}
	return &PingCheckToggleAction{sink: sink, inflight: inflight, idGen: idGen}
}

func (a *PingCheckToggleAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	if a.sink == nil {
		return "", errors.New("command channel disabled")
	}
	if !a.inflight.tryClaim(args.UserID, args.PingCheckTunnelID, pingcheckInflightTTL) {
		return "", errors.New("⏳ команда уже выполняется")
	}
	ref := cmdpkg.MessageRef{
		ChatID:    q.Message.Chat.ID,
		MessageID: q.Message.MessageID,
		ThreadID:  q.Message.MessageThreadID,
	}

	toggleCmd := wire.Command{
		ID:     a.idGen(),
		Action: "pingcheck_toggle",
		Args: map[string]any{
			"tunnel_id": args.PingCheckTunnelID,
			"ndms_name": args.NDMSName,
			"enable":    args.PingCheckEnable,
		},
		IssuedAt: time.Now().UTC(),
	}
	toggleRef := ref
	toggleRef.Action = "pingcheck_toggle"
	if err := a.sink.EnqueueWithRef(args.UserID, toggleCmd, toggleRef); err != nil {
		return "", fmt.Errorf("enqueue pingcheck_toggle: %w", err)
	}

	// Auto-refresh: agent FIFO ensures order — toggle runs first, then status.
	statusCmd := wire.Command{
		ID:       a.idGen(),
		Action:   "pingcheck_status",
		Args:     map[string]any{},
		IssuedAt: time.Now().UTC(),
	}
	statusRef := ref
	statusRef.Action = "pingcheck_status"
	if err := a.sink.EnqueueWithRef(args.UserID, statusCmd, statusRef); err != nil {
		return "", fmt.Errorf("enqueue auto-refresh: %w", err)
	}
	return "📡 переключаю…", nil
}

// Compile-time interface guards.
var _ Action = (*PingCheckOpenAction)(nil)
var _ Action = (*PingCheckToggleAction)(nil)
```

- [ ] **Step 4: Run tests to verify they pass**

```sh
go test ./internal/backend/callbacks/ -run TestPingCheck -v
```

Expected: PASS for all four.

- [ ] **Step 5: NDMS-name resolution note**

The current `decodePingCheckStatus` leaves `NDMSName` empty in entries because `/api/pingcheck/status` doesn't include it. The toggle keyboard needs this. Solution: extend `PingCheckPanelNotifier` to fetch `/api/tunnels/all` alongside, or cache the `tunnel_id → ndms_name` map elsewhere.

For v1, **piggyback on tunnels-panel cache**: the existing `buildTunnelsPanel` path in `router.go` already pulls `/api/tunnels/all`. Extract a small helper `tunnelIDtoNDMS(allTunnels) map[string]string`, call it in the notifier. **TODO marker:** add task as needed during implementation if extraction is non-trivial — pure refactor.

For now, leave a `// TODO(NDMS-resolve)` comment in `decodePingCheckStatus` and proceed; toggle button still renders (with empty NDMSName, which the parser rejects → callback surfaces a clean validation error rather than silent failure).

- [ ] **Step 6: Commit**

```bash
git add internal/backend/callbacks/pingcheck_panel.go internal/backend/callbacks/pingcheck_panel_test.go
git commit -m "feat(callbacks): pingcheck_open/toggle handlers + 5s in-flight guard"
```

---

## Task 10: Hub registration — wire kind=pingcheck end-to-end

**Files:**
- Modify: `internal/backend/callbacks/panel_hub.go`
- Modify: `internal/backend/callbacks/router.go`
- Modify: `cmd/backend/main.go` (notifier wiring)

- [ ] **Step 1: Add kind=pingcheck to Home screen + kindLabel maps**

In `internal/backend/callbacks/panel_hub.go`:

In `panelHomeMessage()`, add a third row above the access row:

```go
		{
			{Text: "🛠 Maintenance", CallbackData: "panel:0:kind:maint"},
			{Text: "📦 Routes", CallbackData: "panel:0:kind:routes"},
		},
		{
			{Text: "📊 Status", CallbackData: "panel:0:kind:status"},
			{Text: "📡 PingCheck", CallbackData: "panel:0:kind:pingcheck"},
		},
		{
			{Text: "🪄 Оживить топики", CallbackData: "panel:0:awaken_confirm"},
		},
```

Find the four `kindLabel := map[string]string{...}` literals in this file (they appear in `panelHandlePush`, `panelEditToKindPick`, and `panelPublish`) and add `"pingcheck": "PingCheck"` to each.

In `panelPublish()`, add a switch case:

```go
	case "pingcheck":
		r.openPingCheckPanelMessage(ctx, m, u)
```

- [ ] **Step 2: Implement `openPingCheckPanelMessage`**

Append to `internal/backend/callbacks/pingcheck_panel.go`:

```go
// openPingCheckPanelMessage publishes an empty "loading…" PingCheck panel
// into the user's per-router topic and immediately enqueues the first
// pingcheck_status. The notifier replaces the placeholder when the
// agent answers.
func (r *Router) openPingCheckPanelMessage(ctx context.Context, m *tg.Message, u *db.User) {
	text := fmt.Sprintf("📡 PingCheck — %s\n\nЗагружаю состояние…", u.Nickname)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🔄 Обновить", CallbackData: fmt.Sprintf("pingcheck_open:%d:_panel_", u.ID)},
		{Text: "✖ Закрыть", CallbackData: fmt.Sprintf("routes_close:%d:_panel_", u.ID)},
	}}}
	msgID, err := r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil)
	if err != nil {
		// log only — alert dispatch already handled by panelPublish probe.
		return
	}
	// Now apply the keyboard via edit (SendMessage above did not include kb to keep
	// the placeholder simple; consistent with maint pattern).
	_ = r.tg.EditMessageText(ctx, m.Chat.ID, msgID, text, "", &kb)
	// Trigger first refresh.
	cmd := wire.Command{ID: defaultCmdID(), Action: "pingcheck_status", Args: map[string]any{}, IssuedAt: time.Now().UTC()}
	ref := cmdpkg.MessageRef{ChatID: m.Chat.ID, MessageID: msgID, ThreadID: m.MessageThreadID, Action: "pingcheck_status"}
	if r.cmdSink != nil {
		_ = r.cmdSink.EnqueueWithRef(u.ID, cmd, ref)
	}
}
```

- [ ] **Step 3: Register actions + notifier in Router**

In `internal/backend/callbacks/router.go`:

Add fields to the `Router` struct (alongside `pendingMaint`/etc. around line 100):

```go
	// PingCheck panel plumbing.
	pingcheckOpenAct   Action
	pingcheckToggleAct Action
	pingcheckInflight  *pingcheckInflightStore
```

Add a `case` in the `HandleCallback` switch (alongside `maint_open`/etc.):

```go
	case "pingcheck_open":
		if r.pingcheckOpenAct != nil {
			action = r.pingcheckOpenAct
		}
	case "pingcheck_toggle":
		if r.pingcheckToggleAct != nil {
			action = r.pingcheckToggleAct
		}
```

Add a setter method (place near other `Set*` methods at file end):

```go
// SetPingCheck wires the PingCheck panel actions. Called from cmd/backend/main.go
// at startup. inflight is shared by Open/Toggle so dup-protection works.
func (r *Router) SetPingCheck(sink CommandEnqueuer) {
	r.pingcheckInflight = newPingCheckInflightStore()
	r.pingcheckOpenAct = NewPingCheckOpenAction(sink, defaultCmdID)
	r.pingcheckToggleAct = NewPingCheckToggleAction(sink, r.pingcheckInflight, defaultCmdID)
}

// NewPingCheckNotifier returns a notifier wired against this router's TG client + DB.
func (r *Router) NewPingCheckNotifier() *PingCheckPanelNotifier {
	return &PingCheckPanelNotifier{TG: r.tg, DB: r.d}
}
```

- [ ] **Step 4: Wire notifier in cmd/backend/main.go**

In `cmd/backend/main.go`, find where `MaintNotifier` is constructed and registered with the cmd-result handler. Add immediately after:

```go
	pingcheckNotifier := router.NewPingCheckNotifier()
	router.SetPingCheck(cmdQueue) // cmdQueue: same instance passed elsewhere
	// register notifier in the cmd-result dispatcher:
	cmdResultDispatcher.Register("pingcheck_status", pingcheckNotifier.NotifyCommandResult)
	cmdResultDispatcher.Register("pingcheck_toggle", pingcheckNotifier.NotifyCommandResult)
```

(Names of `cmdResultDispatcher` / `cmdQueue` may differ — match existing wiring style. Look at how `MaintNotifier` is registered.)

- [ ] **Step 5: Run all package tests + build**

```sh
go build ./...
go test ./internal/backend/callbacks/ ./internal/backend/tg/ ./internal/agent/actions/ -v
```

Expected: build clean, all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/backend/callbacks/panel_hub.go internal/backend/callbacks/router.go internal/backend/callbacks/pingcheck_panel.go cmd/backend/main.go
git commit -m "feat(panels): wire kind=pingcheck into hub + register notifier"
```

---

## Task 11: alerts — extend diag parser with `[]TestDetail`

**Files:**
- Modify: `internal/backend/alerts/diag_report.go`
- Modify: `internal/backend/alerts/diag_report_test.go`

**Prerequisite:** Task 1 step 3 captured a real `/api/diagnostics/result` JSON. Use that exact shape to drive field names below. The skeleton below uses placeholder field names — **replace them with the actual keys discovered in Task 1**.

- [ ] **Step 1: Write failing tests**

Append to `internal/backend/alerts/diag_report_test.go` (use the actual JSON shape from Task 1):

```go
func TestParseDiagTests_PerTunnelFailures(t *testing.T) {
	// Shape based on Task 1 step 3 capture. Replace if actual shape differs.
	const raw = `{
		"version": "1.0",
		"tunnels": {
			"awg10": {
				"mtu":  {"status":"fail","current":1280,"expected":1380,"reason":"path-mtu=1280"},
				"endpoint": {"status":"ok","resolved":"1.2.3.4"},
				"dnsLeak":  {"status":"skip","reason":"public DNS"}
			},
			"awg11": {
				"mtu":  {"status":"fail","current":1280,"expected":1380,"reason":"same"}
			}
		}
	}`
	tests := ParseDiagTests(raw)
	// Find MTU
	var mtu *TestDetail
	for i := range tests {
		if tests[i].ID == "mtu" {
			mtu = &tests[i]
			break
		}
	}
	if mtu == nil {
		t.Fatal("MTU test not found")
	}
	if mtu.Label == "" {
		t.Error("Label should be human-readable")
	}
	if mtu.Status != "fail" {
		t.Errorf("MTU aggregate status should be fail, got %q", mtu.Status)
	}
	if len(mtu.PerTunnel) != 2 {
		t.Errorf("MTU should have 2 per-tunnel entries, got %d", len(mtu.PerTunnel))
	}
	// awg10 detail must include reason
	var awg10 *PerTunnelDetail
	for i := range mtu.PerTunnel {
		if mtu.PerTunnel[i].TunnelLabel == "awg10" {
			awg10 = &mtu.PerTunnel[i]
		}
	}
	if awg10 == nil || awg10.Reason == "" || awg10.Reason != "path-mtu=1280" {
		t.Errorf("awg10 reason wrong: %+v", awg10)
	}
	if awg10.KeyValues["current"] != "1280" || awg10.KeyValues["expected"] != "1380" {
		t.Errorf("awg10 KeyValues wrong: %+v", awg10.KeyValues)
	}
}

func TestParseDiagTests_LegacyJSONNoExtraFields(t *testing.T) {
	const raw = `{"version":"1.0","system":{"appVersion":"2.8.2"}}`
	tests := ParseDiagTests(raw)
	if len(tests) != 0 {
		t.Errorf("legacy JSON without per-tunnel sections should yield zero tests, got %d", len(tests))
	}
}

func TestParseDiagTests_GarbageJSON(t *testing.T) {
	tests := ParseDiagTests("not even json")
	if tests != nil {
		t.Errorf("garbage should return nil, not panic")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```sh
go test ./internal/backend/alerts/ -run TestParseDiagTests -v
```

Expected: FAIL with `undefined: ParseDiagTests` / `TestDetail` / `PerTunnelDetail`.

- [ ] **Step 3: Implement parser extension**

Append to `internal/backend/alerts/diag_report.go`:

```go
// TestDetail is one diag check (e.g., "MTU интерфейса"), aggregated
// across tunnels. Slug ID is stable; Label is the human RU title used
// in button text. PerTunnel may be empty for global tests like
// "WAN up с gateway" — in that case the single aggregate KeyValues
// + Reason on the parent are the detail.
type TestDetail struct {
	ID        string             // "mtu" | "dns_leak" | "host_route" | ...
	Label     string             // "MTU интерфейса"
	Status    string             // "ok" | "fail" | "skip"
	PerTunnel []PerTunnelDetail  // empty for global tests
}

// PerTunnelDetail is the body of one tunnel's row inside a TestDetail.
type PerTunnelDetail struct {
	TunnelLabel string            // "awg10"
	Status      string            // "ok" | "fail" | "skip"
	KeyValues   map[string]string // ordered display fields (current, expected, ...)
	Reason      string
}

// testSlugLabels maps short slug IDs to user-facing Russian labels.
// Slugs are stable across awg-mgr versions; labels track the screenshots.
var testSlugLabels = map[string]string{
	"mtu":          "MTU интерфейса",
	"dns_leak":     "DNS leak проверка",
	"host_route":   "Host route до endpoint",
	"iptables":     "Правила iptables",
	"endpoint":     "Резолв endpoint",
	"endpoint_ping": "Ping endpoint",
	"handshake":    "Handshake свежий",
	"tunnel_conn":  "Связность через туннель",
	"awg_proxy":    "AWG Proxy статус",
	"pingcheck":    "PingCheck статус",
	"validate_cfg": "Валидация конфига",
	"state_consistency": "Консистентность state",
}

// ParseDiagTests extracts per-test details from the awg-mgr diag JSON.
// Returns nil on parse error or if no recognised test sections are
// present — callers should fall back to the existing summary path.
//
// JSON shape — REPLACE THIS BLOCK with the actual shape discovered in
// Task 1 step 3 of the implementation plan. Skeleton below assumes:
//   { "tunnels": { "<tid>": { "<slug>": {"status":"...","reason":"...", ...kv} } } }
func ParseDiagTests(raw string) []TestDetail {
	var top struct {
		Tunnels map[string]map[string]json.RawMessage `json:"tunnels"`
	}
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return nil
	}
	if len(top.Tunnels) == 0 {
		return nil
	}
	// Group by slug across tunnels.
	bySlug := make(map[string]*TestDetail)
	for tid, sections := range top.Tunnels {
		for slug, body := range sections {
			det, ok := bySlug[slug]
			if !ok {
				det = &TestDetail{
					ID:    slug,
					Label: testSlugLabels[slug],
				}
				if det.Label == "" {
					det.Label = slug
				}
				bySlug[slug] = det
			}
			ptd := decodePerTunnel(tid, body)
			det.PerTunnel = append(det.PerTunnel, ptd)
		}
	}
	out := make([]TestDetail, 0, len(bySlug))
	for _, det := range bySlug {
		// Aggregate status: any fail → fail; else any skip → skip; else ok.
		agg := "ok"
		for _, p := range det.PerTunnel {
			if p.Status == "fail" {
				agg = "fail"
				break
			}
			if p.Status == "skip" && agg == "ok" {
				agg = "skip"
			}
		}
		det.Status = agg
		// Stable order
		sort.Slice(det.PerTunnel, func(i, j int) bool {
			return det.PerTunnel[i].TunnelLabel < det.PerTunnel[j].TunnelLabel
		})
		out = append(out, *det)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func decodePerTunnel(tid string, body json.RawMessage) PerTunnelDetail {
	var generic map[string]interface{}
	if err := json.Unmarshal(body, &generic); err != nil {
		return PerTunnelDetail{TunnelLabel: tid}
	}
	out := PerTunnelDetail{TunnelLabel: tid, KeyValues: map[string]string{}}
	if s, ok := generic["status"].(string); ok {
		out.Status = s
	}
	if r, ok := generic["reason"].(string); ok {
		out.Reason = r
	}
	for k, v := range generic {
		if k == "status" || k == "reason" {
			continue
		}
		out.KeyValues[k] = fmt.Sprintf("%v", v)
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

```sh
go test ./internal/backend/alerts/ -run TestParseDiagTests -v
```

Expected: PASS for all three.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/alerts/diag_report.go internal/backend/alerts/diag_report_test.go
git commit -m "feat(alerts): ParseDiagTests extracts per-test detail blocks"
```

---

## Task 12: TG — extend `DiagResultKeyboard` with failing-test buttons

**Files:**
- Modify: `internal/backend/tg/diag_keyboard.go`
- Modify: `internal/backend/tg/diag_keyboard_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/backend/tg/diag_keyboard_test.go`:

```go
func TestDiagResultKeyboard_FailingTestsButtons(t *testing.T) {
	failing := []DiagFailingTest{
		{ID: "mtu", Label: "MTU интерфейса"},
		{ID: "dns_leak", Label: "DNS leak проверка"},
	}
	kb := DiagResultKeyboardWithTests("ok", 42, "abcd1234", failing)
	if len(kb.InlineKeyboard) < 3 {
		t.Fatalf("expected ≥3 rows, got %d", len(kb.InlineKeyboard))
	}
	// First row should be the failing-test buttons
	row0 := kb.InlineKeyboard[0]
	if len(row0) != 2 {
		t.Errorf("expected 2 failing-test buttons, got %d", len(row0))
	}
	if row0[0].CallbackData != "diag_test:42:abcd1234:mtu" {
		t.Errorf("cb mismatch: %q", row0[0].CallbackData)
	}
}

func TestDiagResultKeyboard_NoFailing_NoExtraRow(t *testing.T) {
	kb := DiagResultKeyboardWithTests("ok", 42, "abcd1234", nil)
	// Should match the original 2-row layout (raw + rerun, then close)
	if len(kb.InlineKeyboard) != 2 {
		t.Errorf("expected 2 rows when no failing, got %d", len(kb.InlineKeyboard))
	}
}

func TestDiagResultKeyboard_TruncatesLongLabels(t *testing.T) {
	failing := []DiagFailingTest{{ID: "very_long_id", Label: "Очень длинный заголовок проверки превышает лимит"}}
	kb := DiagResultKeyboardWithTests("ok", 42, "abcd1234", failing)
	btn := kb.InlineKeyboard[0][0]
	// Cap text width — must contain ellipsis if truncated
	if len([]rune(btn.Text)) > 20 {
		t.Errorf("button text too long: %q (%d runes)", btn.Text, len([]rune(btn.Text)))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```sh
go test ./internal/backend/tg/ -run TestDiagResultKeyboard_(FailingTestsButtons|NoFailing|TruncatesLongLabels) -v
```

Expected: FAIL with `undefined: DiagResultKeyboardWithTests` / `DiagFailingTest`.

- [ ] **Step 3: Extend keyboard**

In `internal/backend/tg/diag_keyboard.go`:

```go
// DiagFailingTest carries the minimum needed to render a drill-down button.
type DiagFailingTest struct {
	ID    string // slug — fits in callback_data
	Label string // human RU label, may be long
}

// DiagResultKeyboardWithTests is the failing-test-aware successor to
// DiagResultKeyboard. When failing is non-empty, prepends a row of
// per-test drill-down buttons. status / userID / rawToken behave as in
// the original.
func DiagResultKeyboardWithTests(status string, userID int64, rawToken string, failing []DiagFailingTest) InlineKeyboardMarkup {
	rows := [][]InlineKeyboardButton{}
	const maxPerRow = 8
	if len(failing) > 0 && status == "ok" {
		var row []InlineKeyboardButton
		for _, f := range failing {
			row = append(row, InlineKeyboardButton{
				Text:         "❌ " + truncRunes(f.Label, 16),
				CallbackData: fmt.Sprintf("diag_test:%d:%s:%s", userID, rawToken, f.ID),
			})
			if len(row) >= maxPerRow {
				rows = append(rows, row)
				row = nil
			}
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	// Append the original layout
	orig := DiagResultKeyboard(status, userID, rawToken)
	rows = append(rows, orig.InlineKeyboard...)
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}

// truncRunes caps a string at n runes, suffixing "…" if truncated.
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
```

- [ ] **Step 4: Run tests to verify they pass**

```sh
go test ./internal/backend/tg/ -run TestDiagResultKeyboard -v
```

Expected: PASS — including any pre-existing `TestDiagResultKeyboard*` tests (the new function delegates to the old one for the original rows).

- [ ] **Step 5: Commit**

```bash
git add internal/backend/tg/diag_keyboard.go internal/backend/tg/diag_keyboard_test.go
git commit -m "feat(tg): DiagResultKeyboardWithTests adds failing-test drill-down row"
```

---

## Task 13: Backend — `DiagTestExpandAction` + wire into router

**Files:**
- Create: `internal/backend/callbacks/diag_drilldown.go`
- Create: `internal/backend/callbacks/diag_drilldown_test.go`
- Modify: `internal/backend/callbacks/router.go` (route diag_test)
- Modify: callsite of `DiagResultKeyboard` to switch to `DiagResultKeyboardWithTests` (find via grep)

- [ ] **Step 1: Locate the existing DiagResultKeyboard callsite**

```sh
go run ./tools/grep-helper.sh DiagResultKeyboard 2>/dev/null || true
```

Or use the IDE's grep. Likely call site: somewhere in `alerts/dispatcher.go` or `callbacks/notifier.go` where the diag CommandResult Card is built. Note the file:line.

- [ ] **Step 2: Write failing tests for DiagTestExpandAction**

Create `internal/backend/callbacks/diag_drilldown_test.go`:

```go
package callbacks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/tg"
)

func TestDiagTestExpand_CacheHit_RenderDetail(t *testing.T) {
	dc := newDiagCache()
	body := `{"tunnels":{"awg10":{"mtu":{"status":"fail","current":1280,"expected":1380,"reason":"frag"}}}}`
	tok := dc.Put(body, 5*time.Minute)
	tgFake := &fakeEditTG{}
	a := NewDiagTestExpandAction(dc, tgFake)
	q := &tg.CallbackQuery{ID: "qid", Message: &tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "diag_test", UserID: 7, DiagRawToken: tok, DiagTestID: "mtu"}
	_, err := a.Apply(context.Background(), q, args)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	for _, want := range []string{"MTU интерфейса", "awg10", "1280", "1380", "frag", "« К сводке"} {
		if !strings.Contains(tgFake.lastText, want) && !hasInKb(tgFake.lastKb, want) {
			t.Errorf("missing %q in render or kb", want)
		}
	}
}

func TestDiagTestExpand_CacheMiss(t *testing.T) {
	dc := newDiagCache()
	tgFake := &fakeEditTG{}
	a := NewDiagTestExpandAction(dc, tgFake)
	q := &tg.CallbackQuery{ID: "qid", Message: &tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "diag_test", UserID: 7, DiagRawToken: "deadbeef", DiagTestID: "mtu"}
	_, err := a.Apply(context.Background(), q, args)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(tgFake.lastText, "устарела") {
		t.Errorf("expected stale-cache message, got: %s", tgFake.lastText)
	}
}

func TestDiagTestExpand_TestNotFound(t *testing.T) {
	dc := newDiagCache()
	body := `{"tunnels":{"awg10":{"mtu":{"status":"fail"}}}}`
	tok := dc.Put(body, 5*time.Minute)
	tgFake := &fakeEditTG{}
	a := NewDiagTestExpandAction(dc, tgFake)
	q := &tg.CallbackQuery{ID: "qid", Message: &tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "diag_test", UserID: 7, DiagRawToken: tok, DiagTestID: "missing_test"}
	_, err := a.Apply(context.Background(), q, args)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(tgFake.lastText, "Не нашёл") {
		t.Errorf("expected not-found message, got: %s", tgFake.lastText)
	}
}

func hasInKb(kb *tg.InlineKeyboardMarkup, want string) bool {
	if kb == nil {
		return false
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if strings.Contains(b.Text, want) || strings.Contains(b.CallbackData, want) {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 3: Run tests to verify they fail**

```sh
go test ./internal/backend/callbacks/ -run TestDiagTestExpand -v
```

Expected: FAIL with `undefined: NewDiagTestExpandAction`.

- [ ] **Step 4: Implement DiagTestExpandAction**

Create `internal/backend/callbacks/diag_drilldown.go`:

```go
package callbacks

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/anex/wg-monitor/internal/backend/alerts"
	"github.com/anex/wg-monitor/internal/backend/tg"
)

// diagDrillDownTG is the subset of tg.Client this action edits with.
type diagDrillDownTG interface {
	EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error
}

// DiagTestExpandAction handles diag_test:<token>:<test_id> taps.
// Pulls cached raw JSON, locates the test, renders the per-tunnel
// detail block. Cache miss / test not found → graceful error screens.
type DiagTestExpandAction struct {
	cache *diagCache
	tg    diagDrillDownTG
}

func NewDiagTestExpandAction(cache *diagCache, tgClient diagDrillDownTG) *DiagTestExpandAction {
	return &DiagTestExpandAction{cache: cache, tg: tgClient}
}

func (a *DiagTestExpandAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	body, ok := a.cache.Get(args.DiagRawToken)
	if !ok {
		return "", a.editStale(ctx, q, args.UserID)
	}
	tests := alerts.ParseDiagTests(body)
	var det *alerts.TestDetail
	for i := range tests {
		if tests[i].ID == args.DiagTestID {
			det = &tests[i]
			break
		}
	}
	if det == nil {
		return "", a.editNotFound(ctx, q, args.UserID)
	}
	text := renderTestDetail(*det)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "« К сводке", CallbackData: fmt.Sprintf("diag_raw:%d:_panel_:%s", args.UserID, args.DiagRawToken)},
		{Text: "📄 Полный отчёт", CallbackData: fmt.Sprintf("diag_raw:%d:_panel_:%s", args.UserID, args.DiagRawToken)},
	}}}
	return "", a.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
}

func (a *DiagTestExpandAction) editStale(ctx context.Context, q *tg.CallbackQuery, userID int64) error {
	text := "⏱ Сводка устарела (5 мин TTL). Запусти свежий diag."
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🔁 Diag", CallbackData: fmt.Sprintf("diag_now:%d:_menu", userID)},
	}}}
	return a.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
}

func (a *DiagTestExpandAction) editNotFound(ctx context.Context, q *tg.CallbackQuery, userID int64) error {
	text := "❓ Не нашёл этот тест в результатах. Возможно awg-mgr обновился — попробуй свежий diag."
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "🔁 Diag", CallbackData: fmt.Sprintf("diag_now:%d:_menu", userID)},
	}}}
	return a.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
}

func renderTestDetail(d alerts.TestDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 Диагностика / %s\n\n", d.Label)
	if len(d.PerTunnel) == 0 {
		// Global test (no per-tunnel breakdown). Render just the aggregate.
		fmt.Fprintf(&b, "%s статус: %s\n", iconForStatus(d.Status), d.Status)
		return b.String()
	}
	for _, p := range d.PerTunnel {
		fmt.Fprintf(&b, "%s %s\n", iconForStatus(p.Status), p.TunnelLabel)
		// Stable key order
		keys := make([]string, 0, len(p.KeyValues))
		for k := range p.KeyValues {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "   %s: %s\n", k, p.KeyValues[k])
		}
		if p.Reason != "" {
			fmt.Fprintf(&b, "   reason: %s\n", p.Reason)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func iconForStatus(s string) string {
	switch s {
	case "ok":
		return "✅"
	case "fail":
		return "❌"
	case "skip":
		return "⏭"
	}
	return "⚪"
}

var _ Action = (*DiagTestExpandAction)(nil)
```

- [ ] **Step 5: Wire into router HandleCallback**

In `internal/backend/callbacks/router.go`, add a field to Router (alongside `pingcheckOpenAct`):

```go
	// diag drill-down (C-drilldown).
	diagDrillAct Action
```

Add a switch case (alongside `diag_raw`):

```go
	case "diag_test":
		if r.diagDrillAct != nil {
			action = r.diagDrillAct
		}
```

Add a setter method:

```go
// SetDiagDrillDown wires the diag drill-down action. Called from
// cmd/backend/main.go at startup.
func (r *Router) SetDiagDrillDown() {
	r.diagDrillAct = NewDiagTestExpandAction(r.diagCache, r.tg)
}
```

In `cmd/backend/main.go`, call `router.SetDiagDrillDown()` after the diagCache is wired (look at where `r.diagCache` is initialised — call right after).

- [ ] **Step 6: Switch existing DiagResultKeyboard callsite to use DiagResultKeyboardWithTests**

Find the callsite (Task 13 step 1). It currently passes only `(status, userID, rawToken)`. Refactor:

```go
// Before:
kb := tg.DiagResultKeyboard(status, userID, rawToken)

// After:
var failing []tg.DiagFailingTest
if status == "ok" {
	tests := alerts.ParseDiagTests(rawBody)
	for _, t := range tests {
		if t.Status == "fail" {
			failing = append(failing, tg.DiagFailingTest{ID: t.ID, Label: t.Label})
		}
	}
}
kb := tg.DiagResultKeyboardWithTests(status, userID, rawToken, failing)
```

Where `rawBody` is the diag JSON already at the callsite (it was passed to `diagCache.Put`).

- [ ] **Step 7: Run all callbacks tests + build**

```sh
go build ./...
go test ./internal/backend/callbacks/ ./internal/backend/tg/ ./internal/backend/alerts/ -v
```

Expected: build clean, all tests pass.

- [ ] **Step 8: Commit**

```bash
git add internal/backend/callbacks/diag_drilldown.go internal/backend/callbacks/diag_drilldown_test.go internal/backend/callbacks/router.go cmd/backend/main.go
# also the file modified in step 6:
git add <that-file>
git commit -m "feat(callbacks): DiagTestExpandAction + failing-test drill-down buttons"
```

---

## Task 14: alerts — hint dictionary entries for new actions

**Files:**
- Modify: `internal/backend/alerts/error_hints.go`
- Modify: `internal/backend/alerts/error_hints_test.go`

- [ ] **Step 1: Read existing hint dictionary structure**

```sh
go test ./internal/backend/alerts/ -run TestHintFor -v
```

Look at `error_hints.go` to understand the hint table (it's keyed by action + error prefix substring).

- [ ] **Step 2: Add hints for pingcheck_status / pingcheck_toggle**

In `internal/backend/alerts/error_hints.go`, add entries (the existing entries for `diag_now` are the closest analogue — copy that style):

```go
	"pingcheck_status": {
		"HTTP_REFUSED": "awg-manager недоступен. Проверь что сервис awg-manager работает: `/opt/etc/init.d/S99awg-manager status`",
		"HTTP_5":       "awg-manager отдал серверную ошибку. Логи: `/opt/var/log/awg-manager.log`",
		"":             "Не удалось прочитать состояние PingCheck. Попробуй ещё раз через 5 сек.",
	},
	"pingcheck_toggle": {
		"HTTP_REFUSED": "awg-manager недоступен; ndmc fallback также упал. Проверь сервис awg-manager и доступ к ndmc.",
		"interface unknown": "NDMS не знает интерфейс. Проверь имя интерфейса в awg-mgr → `ndmc -c \"show interface\"`.",
		"":             "Переключение не применилось. См. raw error выше для подробностей.",
	},
```

- [ ] **Step 3: Add a test asserting the hints exist**

In `internal/backend/alerts/error_hints_test.go`:

```go
func TestHintFor_PingCheck(t *testing.T) {
	cases := []struct{ action, err, wantHas string }{
		{"pingcheck_status", "HTTP_REFUSED: dial", "S99awg-manager"},
		{"pingcheck_status", "HTTP_500: oops", "5"},
		{"pingcheck_toggle", "ndmc: interface unknown", "NDMS"},
	}
	for _, c := range cases {
		_, hint := HintFor(c.action, c.err)
		if !strings.Contains(hint, c.wantHas) {
			t.Errorf("%s/%q: hint missing %q; got %q", c.action, c.err, c.wantHas, hint)
		}
	}
}
```

- [ ] **Step 4: Run tests**

```sh
go test ./internal/backend/alerts/ -run TestHintFor -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/alerts/error_hints.go internal/backend/alerts/error_hints_test.go
git commit -m "feat(alerts): hints for pingcheck_status and pingcheck_toggle"
```

---

## Task 15: TG — help text for PingCheck panel

**Files:**
- Modify: `internal/backend/tg/help_panels.go`
- Modify: `internal/backend/tg/help_panels_test.go`

- [ ] **Step 1: Write failing test**

In `internal/backend/tg/help_panels_test.go`:

```go
func TestHelpForScreen_PingCheck(t *testing.T) {
	got := HelpForScreen("pingcheck")
	for _, want := range []string{"PingCheck", "watchdog", "Restart×"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in help body", want)
		}
	}
}
```

- [ ] **Step 2: Run to verify fail**

```sh
go test ./internal/backend/tg/ -run TestHelpForScreen_PingCheck -v
```

Expected: FAIL — case not in switch.

- [ ] **Step 3: Add help body**

In `internal/backend/tg/help_panels.go` `HelpForScreen` switch, add:

```go
	case "pingcheck":
		return `📡 PingCheck — что это
PingCheck — это watchdog awg-manager. Раз в N секунд он пингует целевой
хост через туннель; при N подряд провалах awg-mgr автоматически
рестартит туннель.

Что показывает панель:
  🟢 — туннель жив, последний ping успешен
  🔴 — пинги провалены, туннель помечен как dead
  ⏸ — watchdog для туннеля выключен оператором
  ❓ — состояние неизвестно

Колонки:
  82ms  — задержка последнего ping
  ✓417  — счётчик успешных проверок (с момента старта watchdog)
  ✗0/3  — счётчик провалов / порог рестарта
  Restart×0 — сколько раз watchdog рестартил туннель

Кнопки:
  [⏸/▶ awgN] — выключить/включить watchdog для конкретного туннеля
  [▶ Проверить сейчас] — пнуть watchdog глобально
  [🔄 Обновить] — перечитать состояние

Параметры (host / interval / threshold) задаются NDMS-профилем
ping-check на роутере; меняются через ndmc, не из бота.`
```

- [ ] **Step 4: Run test**

```sh
go test ./internal/backend/tg/ -run TestHelpForScreen_PingCheck -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/tg/help_panels.go internal/backend/tg/help_panels_test.go
git commit -m "feat(tg): help text for PingCheck panel"
```

---

## Task 16: Integration test — backend → fake awgmgr round trip

**Files:**
- Create: `cmd/backend/backend_pingcheck_integration_test.go`

- [ ] **Step 1: Write the integration test**

Create `cmd/backend/backend_pingcheck_integration_test.go`. Mirror the structure of the existing `cmd/backend/test_helpers_test.go` and the `diag_now` integration test. Skeleton:

```go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	// ... existing test_helpers imports
)

func TestIntegration_PingCheckPanel_OpenRoundtrip(t *testing.T) {
	// 1. Spin up fake awg-manager that returns a canned PingCheckStatus
	awgFake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/pingcheck/status" {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true,"data":{"enabled":true,"tunnels":[{"tunnelId":"awg10","tunnelName":"amst","enabled":true,"status":"alive","lastLatency":82,"failCount":0,"successCount":417,"failThreshold":3,"restartCount":0,"tunnelRunning":true}]}}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer awgFake.Close()

	// 2. Spin up backend with fake TG client + the existing in-memory queue
	//    (pattern from test_helpers_test.go). Pass awgFake.URL to the agent
	//    runner via the same hooks the diag_now integration test uses.
	harness := newTestHarness(t, awgFake.URL) // helper from test_helpers_test.go (rename if different)

	// 3. Simulate a callback: panel:0:kind:pingcheck → router pick → push
	harness.dispatchCallback(t, "panel:0:kind:pingcheck")
	harness.dispatchCallback(t, fmt.Sprintf("panel:%d:push:pingcheck", harness.userID))

	// 4. Wait for the cmd-result to flow back and the panel to be edited
	if !harness.waitForEdit(t, 5*time.Second, "📡 PingCheck") {
		t.Fatal("expected panel edit with PingCheck title within 5s")
	}

	// 5. Assert content
	last := harness.lastEdit()
	for _, want := range []string{"amst", "🟢", "82ms", "✓417"} {
		if !strings.Contains(last.text, want) {
			t.Errorf("missing %q in panel body:\n%s", want, last.text)
		}
	}
}
```

(Helper names like `newTestHarness`/`dispatchCallback`/`waitForEdit` may not exist exactly as named — adapt to whatever pattern the existing `diag_now` integration test uses. If the harness is missing, build a small one specifically for this test rather than retrofitting one.)

- [ ] **Step 2: Run the integration test**

```sh
go test ./cmd/backend/ -run TestIntegration_PingCheckPanel -v -timeout 30s
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/backend/backend_pingcheck_integration_test.go
git commit -m "test(integration): pingcheck_open end-to-end roundtrip"
```

---

## Task 17: Smoke test on testkeen (manual, no commit)

- [ ] **Step 1: Deploy current branch to testkeen**

```sh
# from repo root
go run ./cmd/deploy --target testkeen --skip-prerelease
```

(Or use whatever the team's standard deploy invocation is — see `feedback_deploy.md` in memory.)

- [ ] **Step 2: Open `/panel` in TG, navigate to PingCheck**

Verify:
- "📡 PingCheck" appears in the Home screen
- Tapping it shows the router-pick screen
- Tapping a router publishes the panel into per_router topic
- Panel renders within ~3 seconds with real data
- Tunnel rows match what you see at `192.168.31.1:2222/pingcheck`

- [ ] **Step 3: Tap a per-tunnel toggle**

Verify:
- Banner appears confirming switch
- Within ~5 seconds the panel auto-refreshes with the new state
- Verify against awg-mgr UI that the watchdog actually toggled
- Re-tap to restore original state

- [ ] **Step 4: Trigger a fresh diag, then drill into a failing test**

```
TG: tap 📊 Diag
wait for result
tap any ❌ test button
```

Verify:
- Detail screen shows per-tunnel breakdown
- "« К сводке" returns to summary
- "📄 Полный отчёт" still works

- [ ] **Step 5: Note any UX rough edges in the implementation log**

Capture in a follow-up GitHub issue if needed; do not block this plan on polish.

---

## Self-Review against the spec

**Spec coverage:**
- Section 1 (architecture): Tasks 2, 3, 4, 5, 6, 8, 9, 10
- Section 2 (PingCheck data flow): Tasks 5, 6, 8, 10, 16
- Section 3 (toggle data flow): Tasks 3, 4, 6, 9, 10
- Section 4 (diag drill-down): Tasks 11, 12, 13
- Section 5 (error matrix): handled inline across tasks (renderErr / cache miss / not-found / dup-tap), explicit hints in Task 14
- Section 6 (impl order): explicitly preserved in task numbering (Task 1 = preflight)
- Section 7 (file structure): every file in the spec maps to a task

**Placeholder scan:**
- Task 11 step 3 explicitly notes "REPLACE THIS BLOCK with the actual shape discovered in Task 1 step 3" — this is the one deliberate placeholder, gated behind a verified preflight.
- Task 13 step 1 references `tools/grep-helper.sh` as a fallback only; primary path is "use the IDE's grep" — no missing tool.

**Type / signature consistency:**
- `PingCheckPanelEntry` (Tasks 5/6/8): same field set everywhere.
- `wire.Command{Action: "pingcheck_status"}` / `"pingcheck_toggle"` consistent across Tasks 3, 4, 8, 9, 10.
- `Args.PingCheckTunnelID` / `Args.PingCheckEnable` / `Args.DiagTestID` defined in Task 7, used in Tasks 9, 13.
- `DiagFailingTest{ID, Label}` (Task 12) matches `TestDetail{ID, Label}` (Task 11).
- `DiagResultKeyboardWithTests` callsite swap (Task 13 step 6) — handled.

No gaps. No orphan types.
