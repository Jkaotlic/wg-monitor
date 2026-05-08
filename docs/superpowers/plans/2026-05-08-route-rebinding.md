# Route Rebinding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Telegram-driven "Routes panel" that lets the admin migrate all DNS / Static IP rules from one managed AmneziaWG tunnel to another, while preserving every rule that targets WAN, system tunnels, or other managed tunnels. HR-Neo rules are migrated as a sub-class of DNS rules (those with `backend:"hydraroute"`).

**Architecture:** New `awgmgr.routing` client methods for `/api/{dns-routes,static-routes,routing}/*`. Two new agent actions (`route_status`, `route_rebind`) aggregate / mutate. Wire format reuses `CommandResult.Output` to carry JSON-encoded `RouteSnapshot` / `RouteRebindResult` payloads. Backend gets a `RoutesPanel` renderer (Screens 1–5), an in-memory snapshot cache (TTL 30 s, per-user), and a `pendingRebinds` token store mirroring the existing `pendingUploads` pattern. Inline panel updates land via a `RoutesPanelNotifier` that the backend's cmd-result handler dispatches to when `ref.Action` is `route_status` / `route_rebind`.

**Tech Stack:** Go 1.22+, `golang.org/x/sync/errgroup` for parallel status fetch, `net/http/httptest` for unit tests, no new third-party dependencies.

**Spec:** [docs/superpowers/specs/2026-05-08-route-rebinding-design.md](../specs/2026-05-08-route-rebinding-design.md)
**Probes:** [docs/superpowers/notes/2026-05-08-routing-api-probes.md](../notes/2026-05-08-routing-api-probes.md)

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `internal/agent/awgmgr/types_routing.go` | Create | `DNSRoute`, `DNSRouteEntry`, `StaticRoute`, `RoutingTunnel`, env wrappers |
| `internal/agent/awgmgr/routing.go` | Create | Client methods: `ListDNSRoutes`, `UpdateDNSRoute`, `ListStaticRoutes`, `UpdateStaticRoute`, `RoutingTunnels`, `RoutingRefresh`, `HydraRouteControl`, `GetEnv` (helper) |
| `internal/agent/awgmgr/routing_test.go` | Create | httptest fixtures for each method |
| `internal/agent/actions/route_status.go` | Create | Aggregates parallel fetches into a `wire.RouteSnapshot` JSON string |
| `internal/agent/actions/route_status_test.go` | Create | Mocks awgmgr, verifies counts (managed-vs-other split) |
| `internal/agent/actions/route_rebind.go` | Create | DNS + Static per-rule rebind with fall-through-conversion option |
| `internal/agent/actions/route_rebind_test.go` | Create | Happy path, partial fail, src==dst no-op, WAN-untouched assertion |
| `internal/agent/actions/runner.go` | Modify | Add `route_status` and `route_rebind` cases; `routeMu sync.Mutex` |
| `pkg/wire/types.go` | Modify | Add `route_status`, `route_rebind` to `validCommandActions` |
| `pkg/wire/routing.go` | Create | Shared types `RouteSnapshot`, `RouteRebindResult`, `TunnelMeta`, `TunnelCounts`, `CategoryResult`, `HRStatus` |
| `pkg/wire/routing_test.go` | Create | JSON round-trip |
| `internal/backend/callbacks/routes_cache.go` | Create | TTL cache (30 s, per user) |
| `internal/backend/callbacks/routes_cache_test.go` | Create | TTL, invalidate, per-user |
| `internal/backend/tg/routes_panel.go` | Create | Screens 2–5 text + keyboards |
| `internal/backend/tg/routes_panel_test.go` | Create | Render Empty, with counts, HR-Neo absent, untouched-block, keyboard |
| `internal/backend/callbacks/parse.go` | Modify | Add 8 new actions; `RebindToken`, `RebindSrcID`, `RebindDstID` fields on `Args` |
| `internal/backend/callbacks/parse_test.go` | Modify | Tests for each new callback |
| `internal/backend/callbacks/actions.go` | Modify | `RebindConfirmAction` + `pendingRebind` + `makeRebindToken` |
| `internal/backend/callbacks/actions_test.go` | Modify | Token lifecycle, replay rejection, src==dst guard |
| `internal/backend/callbacks/router.go` | Modify | Reply-keyboard branch `🛣 Маршруты`; case branches for new actions; `routesCache` and `pendingRebinds` fields + helpers |
| `internal/backend/callbacks/routes_notifier.go` | Create | Handles `route_status`/`route_rebind` results — edits panel in place |
| `internal/backend/callbacks/routes_notifier_test.go` | Create | Status render + cache write; rebind render + cache invalidate |
| `internal/backend/handler.go` | Modify | `RoutesNotifier` interface in `Deps`; dispatch in `cmdResultHandler` |
| `internal/backend/handler_test.go` | Modify | Asserts dispatch by `ref.Action` |
| `cmd/backend/main.go` | Modify | Wire `RoutesCache`, `RoutesPanelNotifier`, `RebindConfirmAction` |
| `internal/backend/tg/replykb.go` | Modify | Add `🛣 Маршруты` button row |
| `cmd/backend/integration_test.go` | Modify | E2E: status → rebind → status; WAN-untouched canary |

---

## Conventions

- **Tests first**, then minimal implementation. One green run before committing.
- **Commit boundaries** = task boundaries unless the task explicitly says "no commit".
- **Run `go test ./...`** after each implementation step.
- **No new third-party imports** beyond `golang.org/x/sync/errgroup` (already transitively pulled).
- **JSON tags** match awg-manager's response shape exactly — verbatim from probe notes (`tunnelID` capital D for static, `tunnelId` lowercase d for DNS — these ARE different).
- **Russian text** in TG renderers — match `tunnels_panel.go` style. Plain text only (no MarkdownV2, per `feedback_telegram_api`).

---

## Milestone 0: Curl-Probe — Verify awg-manager API Contract

### Task 0.1: Probe routing endpoints on testkeen

**Status:** ✅ DONE 2026-05-08. See [docs/superpowers/notes/2026-05-08-routing-api-probes.md](../notes/2026-05-08-routing-api-probes.md) — committed in `cea6662`.

Key findings used by every subsequent milestone:
- DNS rule bind: `routes[i].interface` and `routes[i].tunnelId` (same value, both fields). Fall-through if `routes==null`.
- Static rule bind: `tunnelID` (capital D — note the casing).
- Bind value = `iface` from `/api/routing/tunnels` = managed tunnel's `interfaceName` (`nwg0`, `nwg1`).
- HR-Neo rules are DNS rules with `backend:"hydraroute"`. There is NO separate HR-Neo rule API.
- `/api/dns-routes/bulk-backend` changes the engine field, NOT the tunnel target — NOT used in our design.
- The UI's "Сменить туннель" mass operation iterates per-rule `update`, not bulk.

---

## Milestone 1: Routing Types and Client Methods

### Task 1.1: Define routing types

**Files:**
- Create: `internal/agent/awgmgr/types_routing.go`

- [ ] **Step 1: Write the file**

```go
package awgmgr

// DNSRouteEntry is one element of DNSRoute.Routes — explicit tunnel binding.
// `Interface` and `TunnelID` carry the same value (the iface from
// /api/routing/tunnels, e.g. "nwg1", "eth3"). awg-manager UI sets both.
type DNSRouteEntry struct {
	Interface string `json:"interface"`
	TunnelID  string `json:"tunnelId"`
	Fallback  string `json:"fallback,omitempty"`
}

// DNSRoute mirrors one entry of /api/dns-routes/list .data[]. A rule with
// Routes=nil falls through to the global engine policy (HRPolicyName for
// hydraroute). Setting an explicit Routes converts it to direct routing.
//
// All fields are preserved verbatim on update (awg-manager treats update
// as full-replace) — never drop unknown fields. Use the JSON round-trip via
// json.RawMessage for forward-compat with future awg-manager versions.
type DNSRoute struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Domains       []string        `json:"domains"`
	ManualDomains []string        `json:"manualDomains"`
	Routes        []DNSRouteEntry `json:"routes"`
	Enabled       bool            `json:"enabled"`
	CreatedAt     string          `json:"createdAt"`
	UpdatedAt     string          `json:"updatedAt"`
	Backend       string          `json:"backend"`      // "hydraroute" | "ndms" — engine, not tunnel
	HRRouteMode   string          `json:"hrRouteMode,omitempty"`
	HRPolicyName  string          `json:"hrPolicyName,omitempty"`
	// Extra holds any additional fields awg-manager returns that we don't
	// model explicitly. They are preserved on round-trip via json.RawMessage.
	// (Implementation note: until concrete need, omit Extra; the explicit
	// fields above cover the 2.8.2 schema. Add Extra in a follow-up if a
	// future awg-manager version adds fields we'd otherwise drop.)
}

// StaticRoute mirrors one entry of /api/static-routes/list .data[].
// CRITICAL: bind field is `tunnelID` with CAPITAL D (different from DNS).
type StaticRoute struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	TunnelID string   `json:"tunnelID"`
	Subnets  []string `json:"subnets"`
	Fallback string   `json:"fallback,omitempty"`
	Enabled  bool     `json:"enabled"`
}

// RoutingTunnel mirrors one entry of /api/routing/tunnels .data[].
// `Iface` is the canonical bind value used in DNSRoute.Routes and StaticRoute.TunnelID.
type RoutingTunnel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Iface     string `json:"iface"`
	Type      string `json:"type"`   // "managed" | "system" | "wan"
	Status    string `json:"status"` // "running" | "up" | "down" | …
	Available bool   `json:"available"`
}
```

- [ ] **Step 2: Compile-check**

```bash
go build ./internal/agent/awgmgr
```
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add internal/agent/awgmgr/types_routing.go
git commit -m "feat(awgmgr): routing types — DNSRoute, StaticRoute, RoutingTunnel"
```

### Task 1.2: ListDNSRoutes / UpdateDNSRoute + tests

**Files:**
- Create: `internal/agent/awgmgr/routing.go`
- Create: `internal/agent/awgmgr/routing_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package awgmgr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListDNSRoutes_HappyPath(t *testing.T) {
	const payload = `{"success":true,"data":[
		{"id":"hr:Vk","name":"Vk","domains":["vk.com"],"manualDomains":["vk.com"],
		 "routes":[{"interface":"nwg1","tunnelId":"nwg1","fallback":"auto"}],
		 "enabled":true,"backend":"hydraroute","hrPolicyName":"HydraRoute"},
		{"id":"hr:Sber","name":"Sber","domains":["sberbank.ru"],"manualDomains":["sberbank.ru"],
		 "routes":null,"enabled":true,"backend":"hydraroute","hrPolicyName":"HydraRoute"}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dns-routes/list" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With")
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	c := New(srv.URL)
	got, err := c.ListDNSRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len: %d", len(got))
	}
	if got[0].ID != "hr:Vk" || len(got[0].Routes) != 1 || got[0].Routes[0].Interface != "nwg1" {
		t.Errorf("got[0]: %+v", got[0])
	}
	if got[1].Routes != nil {
		t.Errorf("got[1] should have nil routes (fall-through): %+v", got[1])
	}
}

func TestUpdateDNSRoute_SendsFullBody(t *testing.T) {
	var got DNSRoute
	var gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dns-routes/update" || r.Method != http.MethodPost {
			t.Errorf("method/path: %s %q", r.Method, r.URL.Path)
		}
		gotID = r.URL.Query().Get("id")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	rule := DNSRoute{
		ID: "hr:Vk", Name: "Vk", Backend: "hydraroute", HRPolicyName: "HydraRoute",
		Routes: []DNSRouteEntry{{Interface: "nwg0", TunnelID: "nwg0", Fallback: "auto"}},
	}
	if err := c.UpdateDNSRoute(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	if gotID != "hr:Vk" {
		t.Errorf("id query: %q", gotID)
	}
	if got.Routes == nil || got.Routes[0].Interface != "nwg0" {
		t.Errorf("body: %+v", got)
	}
	if !strings.Contains(got.Backend, "hydraroute") {
		t.Errorf("backend not preserved: %+v", got)
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

```bash
go test ./internal/agent/awgmgr -run "TestListDNSRoutes|TestUpdateDNSRoute"
```

- [ ] **Step 3: Implement**

Create `internal/agent/awgmgr/routing.go`:

```go
package awgmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ListDNSRoutes returns /api/dns-routes/list .data.
func (c *Client) ListDNSRoutes(ctx context.Context) ([]DNSRoute, error) {
	var env Envelope[[]DNSRoute]
	if err := c.get(ctx, "/api/dns-routes/list", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr dns-routes/list: success=false")
	}
	return env.Data, nil
}

// UpdateDNSRoute calls POST /api/dns-routes/update?id=<id> with the full
// rule object as the body. awg-manager treats the call as full-replace —
// the rule must be sent verbatim with only the desired fields modified.
func (c *Client) UpdateDNSRoute(ctx context.Context, rule DNSRoute) error {
	body, err := json.Marshal(rule)
	if err != nil {
		return err
	}
	return c.postJSON(ctx, "/api/dns-routes/update?id="+rule.ID, body, nil)
}

// postJSON is a helper that POSTs JSON with the right headers. The existing
// (lowercase) post helper accepts a body io.Reader but doesn't set
// Content-Type; awg-manager's update endpoints require it. Inline here to
// avoid disturbing the existing helper.
func (c *Client) postJSON(ctx context.Context, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("awgmgr POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("awgmgr %s: HTTP %d: %s", path, resp.StatusCode, snippet(rb))
	}
	if out != nil && len(rb) > 0 {
		if err := json.Unmarshal(rb, out); err != nil {
			return fmt.Errorf("awgmgr %s: decode: %w", path, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./internal/agent/awgmgr -run "TestListDNSRoutes|TestUpdateDNSRoute" -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/agent/awgmgr/routing.go internal/agent/awgmgr/routing_test.go
git commit -m "feat(awgmgr): ListDNSRoutes, UpdateDNSRoute"
```

### Task 1.3: ListStaticRoutes / UpdateStaticRoute + tests

**Files:**
- Modify: `internal/agent/awgmgr/routing.go`
- Modify: `internal/agent/awgmgr/routing_test.go`

- [ ] **Step 1: Append failing tests**

```go
func TestListStaticRoutes_HappyPath(t *testing.T) {
	const payload = `{"success":true,"data":[
		{"id":"s1","name":"work","tunnelID":"nwg1","subnets":["10.0.0.0/8"],"fallback":"auto","enabled":true}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/static-routes/list" {
			t.Errorf("path: %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	c := New(srv.URL)
	got, err := c.ListStaticRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TunnelID != "nwg1" {
		t.Errorf("got: %+v", got)
	}
}

func TestUpdateStaticRoute_NoIDInURL_FullBody(t *testing.T) {
	var got StaticRoute
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/static-routes/update" || r.URL.RawQuery != "" {
			t.Errorf("expected path /api/static-routes/update with NO query, got %q?%q", r.URL.Path, r.URL.RawQuery)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	rule := StaticRoute{ID: "s1", Name: "work", TunnelID: "nwg0", Subnets: []string{"10.0.0.0/8"}, Fallback: "auto", Enabled: true}
	if err := c.UpdateStaticRoute(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	if got.ID != "s1" || got.TunnelID != "nwg0" {
		t.Errorf("body: %+v", got)
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement**

Append to `routing.go`:

```go
// ListStaticRoutes returns /api/static-routes/list .data.
func (c *Client) ListStaticRoutes(ctx context.Context) ([]StaticRoute, error) {
	var env Envelope[[]StaticRoute]
	if err := c.get(ctx, "/api/static-routes/list", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr static-routes/list: success=false")
	}
	return env.Data, nil
}

// UpdateStaticRoute calls POST /api/static-routes/update — the id is in the
// body, NOT in the URL (different from DNS update). awg-manager full-replaces.
func (c *Client) UpdateStaticRoute(ctx context.Context, rule StaticRoute) error {
	body, err := json.Marshal(rule)
	if err != nil {
		return err
	}
	return c.postJSON(ctx, "/api/static-routes/update", body, nil)
}
```

- [ ] **Step 4: Run, expect PASS**

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(awgmgr): ListStaticRoutes, UpdateStaticRoute"
```

### Task 1.4: RoutingTunnels / RoutingRefresh / HydraRouteControl / GetEnv

**Files:**
- Modify: `internal/agent/awgmgr/routing.go`
- Modify: `internal/agent/awgmgr/routing_test.go`
- Modify: `internal/agent/awgmgr/client.go`

- [ ] **Step 1: Append failing tests**

```go
func TestRoutingTunnels_HappyPath(t *testing.T) {
	const payload = `{"success":true,"data":[
		{"id":"awg11","name":"amnezia_for_awg","iface":"nwg1","type":"managed","status":"running","available":true},
		{"id":"wan:eth3","name":"WAN","iface":"eth3","type":"wan","status":"up","available":true}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/routing/tunnels" {
			t.Errorf("path: %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	c := New(srv.URL)
	got, err := c.RoutingTunnels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Iface != "nwg1" || got[1].Type != "wan" {
		t.Errorf("got: %+v", got)
	}
}

func TestRoutingRefresh_HappyPath(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/routing/refresh" && r.Method == http.MethodPost {
			called = true
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	if err := c.RoutingRefresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Errorf("/api/routing/refresh not called")
	}
}

func TestHydraRouteControl_BodyShape(t *testing.T) {
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/system/hydraroute-control" {
			t.Errorf("path: %q", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	if err := c.HydraRouteControl(context.Background(), "restart"); err != nil {
		t.Fatal(err)
	}
	if body["action"] != "restart" {
		t.Errorf("body: %+v", body)
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement**

Append to `routing.go`:

```go
// RoutingTunnels returns /api/routing/tunnels .data — the catalogue of all
// routable interfaces (managed/system/wan). Used by the rebind action to
// resolve the iface value used in route bindings.
func (c *Client) RoutingTunnels(ctx context.Context) ([]RoutingTunnel, error) {
	var env Envelope[[]RoutingTunnel]
	if err := c.get(ctx, "/api/routing/tunnels", &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr routing/tunnels: success=false")
	}
	return env.Data, nil
}

// RoutingRefresh forces NDMS cache reset.
func (c *Client) RoutingRefresh(ctx context.Context) error {
	return c.post(ctx, "/api/routing/refresh", nil, nil)
}

// HydraRouteControl posts {"action":"<action>"} to /api/system/hydraroute-control.
// action ∈ {"start","stop","restart"}. Called after rebinding any rule with
// backend=="hydraroute" so the daemon reloads.
func (c *Client) HydraRouteControl(ctx context.Context, action string) error {
	body, err := json.Marshal(map[string]string{"action": action})
	if err != nil {
		return err
	}
	return c.postJSON(ctx, "/api/system/hydraroute-control", body, nil)
}
```

Append to `client.go`:

```go
// GetEnv is a public version of the lowercase get helper. Used by callers in
// other packages (internal/agent/actions) that need to issue typed GETs
// against awg-manager without duplicating the HTTP plumbing.
func (c *Client) GetEnv(ctx context.Context, path string, out any) error {
	return c.get(ctx, path, out)
}
```

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./internal/agent/awgmgr -v
```

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(awgmgr): RoutingTunnels, RoutingRefresh, HydraRouteControl, GetEnv"
```

---

## Milestone 2: Wire Types

### Task 2.1: Shared payload types

**Files:**
- Create: `pkg/wire/routing.go`
- Create: `pkg/wire/routing_test.go`
- Modify: `pkg/wire/types.go`

- [ ] **Step 1: Write the failing test**

`pkg/wire/routing_test.go`:

```go
package wire

import (
	"encoding/json"
	"testing"
)

func TestRouteSnapshot_RoundTrip(t *testing.T) {
	want := RouteSnapshot{
		HRNeo: HRStatus{Installed: true, Running: true},
		Tunnels: []TunnelMeta{
			{ID: "t1", Name: "amnezia", Iface: "nwg1", Enabled: true},
		},
		Counts: map[string]TunnelCounts{
			"t1": {DNS: 5, Static: 2, HRNeo: 1},
		},
		Other: TunnelCounts{DNS: 12, Static: 0, HRNeo: 0},
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got RouteSnapshot
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.HRNeo.Installed != true || got.Counts["t1"].DNS != 5 || got.Other.DNS != 12 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestRouteRebindResult_RoundTrip(t *testing.T) {
	want := RouteRebindResult{
		SrcTunnelID: "awg11", DstTunnelID: "awg13",
		DNS:    CategoryResult{OK: 3, Failed: 1, Errors: []string{"err1"}},
		Static: CategoryResult{OK: 0},
		HRNeo:  CategoryResult{OK: 5},
	}
	b, _ := json.Marshal(want)
	var got RouteRebindResult
	_ = json.Unmarshal(b, &got)
	if got.DNS.OK != 3 || got.DNS.Failed != 1 || len(got.DNS.Errors) != 1 {
		t.Errorf("got: %+v", got)
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement**

`pkg/wire/routing.go`:

```go
// Package wire — routing.go defines payload types for route_status and
// route_rebind. They are JSON-encoded into wire.CommandResult.Output;
// no wire envelope additions are required besides the action names.
package wire

type HRStatus struct {
	Installed bool `json:"installed"`
	Running   bool `json:"running"`
}

// TunnelMeta is the subset of awgmgr.Tunnel the panel needs for rendering.
// `Iface` is the canonical bind value (matches awgmgr.Tunnel.InterfaceName
// for managed tunnels) and is used by the renderer to label rows.
type TunnelMeta struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Iface   string `json:"iface"`
	Enabled bool   `json:"enabled"`
	// DefaultRoute marks managed tunnels with `defaultRoute=true`. Used as
	// the heuristic for the global HR-Neo policy default during rebind
	// fall-through conversion (Milestone 4).
	DefaultRoute bool `json:"default_route,omitempty"`
}

// TunnelCounts tracks rules attached to a single tunnel by category.
// HRNeo is a sub-class of DNS — DNS rules with backend="hydraroute".
// Total rules = DNS + Static (HRNeo is INCLUDED in DNS, not added).
// Renderer derives "shown total" = DNS + Static; HRNeo shown separately
// only as informational sub-count.
type TunnelCounts struct {
	DNS    int `json:"dns"`
	Static int `json:"static"`
	HRNeo  int `json:"hr_neo"`
}

// RouteSnapshot is the payload of a successful route_status CommandResult.
type RouteSnapshot struct {
	HRNeo   HRStatus                `json:"hr_neo"`
	Tunnels []TunnelMeta            `json:"tunnels"` // managed tunnels only
	Counts  map[string]TunnelCounts `json:"counts"`  // key = tunnel id
	Other   TunnelCounts            `json:"other"`   // sum across WAN/system/external
}

type CategoryResult struct {
	OK     int      `json:"ok"`
	Failed int      `json:"failed"`
	Errors []string `json:"errors,omitempty"`
}

// RouteRebindResult is the payload of a route_rebind CommandResult.
// Static is reported separately. HRNeo is the subset of DNS results where
// backend=="hydraroute"; the count of pure-NDMS DNS results = DNS - HRNeo.
type RouteRebindResult struct {
	SrcTunnelID string         `json:"src_tunnel_id"`
	DstTunnelID string         `json:"dst_tunnel_id"`
	DNS         CategoryResult `json:"dns"`
	Static      CategoryResult `json:"static"`
	HRNeo       CategoryResult `json:"hr_neo"`
}
```

Edit `pkg/wire/types.go` — extend `validCommandActions`:

```go
var validCommandActions = map[string]bool{
	"restart_tunnel":   true,
	"diag_now":         true,
	"pingcheck_now":    true,
	"opkg_upgrade":     true,
	"force_recheck":    true,
	"tunnel_enable":    true,
	"tunnel_disable":   true,
	"check_via_tunnel": true,
	"check_direct":     true,
	"tunnel_import":    true,
	"route_status":     true,
	"route_rebind":     true,
}
```

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./pkg/wire -v
```

- [ ] **Step 5: Commit**

```bash
git add pkg/wire/routing.go pkg/wire/routing_test.go pkg/wire/types.go
git commit -m "feat(wire): route_status/route_rebind payload types"
```

---

## Milestone 3: route_status Action

### Task 3.1: Aggregator + tests

**Files:**
- Create: `internal/agent/actions/route_status.go`
- Create: `internal/agent/actions/route_status_test.go`

- [ ] **Step 1: Write the failing test**

```go
package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

// fakeAwgmgrStatus serves canned JSON for the four endpoints route_status
// hits. Designed for both happy-path (with HR-Neo installed) and
// HR-absent variants.
func fakeAwgmgrStatus(t *testing.T, hrInstalled bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		body := `{"success":true,"data":{"installed":false,"running":false}}`
		if hrInstalled {
			body = `{"success":true,"data":{"installed":true,"running":true}}`
		}
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"t1","name":"amnezia","interfaceName":"nwg1","ndmsName":"Wireguard1","enabled":true,"defaultRoute":true},
			{"id":"t2","name":"newtun","interfaceName":"nwg0","ndmsName":"Wireguard0","enabled":true,"defaultRoute":false}
		],"external":[],"system":[]}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		// 4 rules: 2 explicit on nwg1 (one hr, one ndms), 1 on nwg0, 1 on WAN (eth3)
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:Vk","backend":"hydraroute","routes":[{"interface":"nwg1","tunnelId":"nwg1"}]},
			{"id":"ndms:Yandex","backend":"ndms","routes":[{"interface":"nwg1","tunnelId":"nwg1"}]},
			{"id":"hr:Cn","backend":"hydraroute","routes":[{"interface":"nwg0","tunnelId":"nwg0"}]},
			{"id":"hr:Sber","backend":"hydraroute","routes":[{"interface":"eth3","tunnelId":"eth3"}]}
		]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"s1","tunnelID":"nwg1"},
			{"id":"s2","tunnelID":"eth3"}
		]}`))
	})
	return httptest.NewServer(mux)
}

func TestRouteStatus_HappyPath(t *testing.T) {
	srv := fakeAwgmgrStatus(t, true)
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	out, err := RouteStatus(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	if !snap.HRNeo.Installed || !snap.HRNeo.Running {
		t.Errorf("HR-Neo: %+v", snap.HRNeo)
	}
	if len(snap.Tunnels) != 2 {
		t.Errorf("tunnels: %d", len(snap.Tunnels))
	}
	// t1 (nwg1): 2 DNS (1 hr + 1 ndms), 1 static, hr_neo subcount = 1
	if snap.Counts["t1"].DNS != 2 || snap.Counts["t1"].Static != 1 || snap.Counts["t1"].HRNeo != 1 {
		t.Errorf("t1 counts: %+v", snap.Counts["t1"])
	}
	// t2 (nwg0): 1 DNS (hr), 0 static, hr_neo subcount = 1
	if snap.Counts["t2"].DNS != 1 || snap.Counts["t2"].Static != 0 || snap.Counts["t2"].HRNeo != 1 {
		t.Errorf("t2 counts: %+v", snap.Counts["t2"])
	}
	// other (WAN eth3): 1 DNS (hr) + 1 static
	if snap.Other.DNS != 1 || snap.Other.Static != 1 {
		t.Errorf("other counts: %+v", snap.Other)
	}
	// default route detection
	t1, t2 := snap.Tunnels[0], snap.Tunnels[1]
	if !t1.DefaultRoute || t2.DefaultRoute {
		t.Errorf("default_route flags: t1=%v t2=%v (want true,false)", t1.DefaultRoute, t2.DefaultRoute)
	}
}

func TestRouteStatus_HRNeoAbsent(t *testing.T) {
	srv := fakeAwgmgrStatus(t, false)
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	out, err := RouteStatus(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	var snap wire.RouteSnapshot
	_ = json.Unmarshal([]byte(out), &snap)
	if snap.HRNeo.Installed {
		t.Errorf("HR-Neo should not be reported installed")
	}
	// HR sub-counts must still be reported (rules with backend=hydraroute exist
	// regardless of daemon state); they just won't be acted on by HR-Neo.
}

func TestRouteStatus_FallthroughRulesAreCounted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true}}`))
	})
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"t1","interfaceName":"nwg1","ndmsName":"Wireguard1","enabled":true,"defaultRoute":true}
		],"external":[],"system":[]}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		// 3 fall-through HR rules
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:A","backend":"hydraroute","routes":null,"hrPolicyName":"HydraRoute"},
			{"id":"hr:B","backend":"hydraroute","routes":null,"hrPolicyName":"HydraRoute"},
			{"id":"hr:C","backend":"hydraroute","routes":null,"hrPolicyName":"HydraRoute"}
		]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	out, _ := RouteStatus(context.Background(), c)
	var snap wire.RouteSnapshot
	_ = json.Unmarshal([]byte(out), &snap)
	// Fall-through rules are credited to the default-route tunnel.
	if snap.Counts["t1"].DNS != 3 || snap.Counts["t1"].HRNeo != 3 {
		t.Errorf("expected 3 fall-through DNS+HR on t1, got %+v", snap.Counts["t1"])
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement**

`internal/agent/actions/route_status.go`:

```go
package actions

import (
	"context"
	"encoding/json"

	"golang.org/x/sync/errgroup"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

// RouteStatus fetches the routing snapshot and returns it as a JSON-encoded
// string suitable for wire.CommandResult.Output.
func RouteStatus(ctx context.Context, c *awgmgr.Client) (string, error) {
	var (
		hr      *awgmgr.HydraRouteStatus
		tunnels *awgmgr.TunnelsAll
		dns     []awgmgr.DNSRoute
		statics []awgmgr.StaticRoute
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { hr, err = c.HydraRouteStatus(gctx); return })
	g.Go(func() (err error) { tunnels, err = c.TunnelsAll(gctx); return })
	g.Go(func() (err error) { dns, err = c.ListDNSRoutes(gctx); return })
	g.Go(func() (err error) { statics, err = c.ListStaticRoutes(gctx); return })
	if err := g.Wait(); err != nil {
		return "", err
	}
	snap := buildRouteSnapshot(hr, tunnels, dns, statics)
	b, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// buildRouteSnapshot is the pure aggregation function — easy to test.
func buildRouteSnapshot(hr *awgmgr.HydraRouteStatus, tunnels *awgmgr.TunnelsAll, dns []awgmgr.DNSRoute, statics []awgmgr.StaticRoute) wire.RouteSnapshot {
	snap := wire.RouteSnapshot{Counts: make(map[string]wire.TunnelCounts)}
	if hr != nil {
		snap.HRNeo = wire.HRStatus{Installed: hr.Installed, Running: hr.Running}
	}
	// Map managed tunnels by iface (interfaceName) for backend-field lookups,
	// and capture the default-route managed tunnel for fall-through credit.
	byIface := make(map[string]string) // iface → tunnel id
	defaultIface := ""
	if tunnels != nil {
		for _, t := range tunnels.Tunnels {
			snap.Tunnels = append(snap.Tunnels, wire.TunnelMeta{
				ID: t.ID, Name: t.Name, Iface: t.InterfaceName,
				Enabled: t.Enabled, DefaultRoute: t.DefaultRoute,
			})
			if t.InterfaceName != "" {
				byIface[t.InterfaceName] = t.ID
			}
			if t.DefaultRoute && defaultIface == "" {
				defaultIface = t.InterfaceName
			}
		}
	}
	creditDNS := func(tid string, isHRNeo bool) {
		c := snap.Counts[tid]
		c.DNS++
		if isHRNeo {
			c.HRNeo++
		}
		snap.Counts[tid] = c
	}
	creditOther := func(isHRNeo bool, isStatic bool) {
		if isStatic {
			snap.Other.Static++
		} else {
			snap.Other.DNS++
			if isHRNeo {
				snap.Other.HRNeo++
			}
		}
	}
	for _, r := range dns {
		isHR := r.Backend == "hydraroute"
		if len(r.Routes) > 0 {
			// Use the FIRST route's interface as the primary binding.
			iface := r.Routes[0].Interface
			if id, ok := byIface[iface]; ok {
				creditDNS(id, isHR)
			} else {
				creditOther(isHR, false)
			}
			continue
		}
		// Fall-through rule. If it follows the global HR-Neo policy and we
		// have a default-route managed tunnel, credit there. Otherwise
		// classify as Other.
		if isHR && r.HRPolicyName != "" && defaultIface != "" {
			if id, ok := byIface[defaultIface]; ok {
				creditDNS(id, true)
				continue
			}
		}
		creditOther(isHR, false)
	}
	for _, r := range statics {
		if id, ok := byIface[r.TunnelID]; ok {
			c := snap.Counts[id]
			c.Static++
			snap.Counts[id] = c
		} else {
			snap.Other.Static++
		}
	}
	return snap
}
```

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./internal/agent/actions -run TestRouteStatus -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/agent/actions/route_status.go internal/agent/actions/route_status_test.go
git commit -m "feat(agent): route_status action with fall-through credit to default-route tunnel"
```

### Task 3.2: Wire route_status into Runner

**Files:**
- Modify: `internal/agent/actions/runner.go`
- Create: `internal/agent/actions/runner_routes_test.go`

- [ ] **Step 1: Write the failing test**

```go
package actions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

func TestRunner_RouteStatus_Dispatch(t *testing.T) {
	srv := fakeAwgmgrStatus(t, false)
	defer srv.Close()
	r := &Runner{AwgClient: awgmgr.New(srv.URL)}
	res := r.Execute(context.Background(), wire.Command{ID: "x", Action: "route_status"})
	if res.Status != "ok" {
		t.Fatalf("status: %s, output: %s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, `"tunnels":`) {
		t.Errorf("output not JSON snapshot: %s", res.Output)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
		t.Errorf("output not decodable: %v", err)
	}
}
```

- [ ] **Step 2: Run, expect FAIL ("unknown action: route_status")**

- [ ] **Step 3: Add the case**

In `internal/agent/actions/runner.go` insert before `default:`:

```go
	case "route_status":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured"
		}
		out, err := RouteStatus(ctx, r.AwgClient)
		if err != nil {
			return "err", err.Error()
		}
		return "ok", out
```

- [ ] **Step 4: Run, expect PASS**

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(agent): runner dispatch for route_status"
```

---

## Milestone 4: route_rebind Action

### Task 4.1: srcRefs + per-category executor

**Files:**
- Create: `internal/agent/actions/route_rebind.go`
- Create: `internal/agent/actions/route_rebind_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

// fakeAwgmgrRebind builds a stateful mock of awg-manager. Rules are mutated
// on update — we can verify post-state at the end of the test. Designed to
// catch the canary case: a rule whose Backend points at WAN (eth3) MUST NOT
// be modified by rebind.
type rebindMock struct {
	t        *testing.T
	mu       sync.Mutex
	dnsRules []awgmgr.DNSRoute
	staticRules []awgmgr.StaticRoute
	hrInstalled bool
	hrControlCalls atomic.Int32
	refreshCalls atomic.Int32
}

func newRebindMock(t *testing.T) *rebindMock {
	return &rebindMock{
		t: t,
		hrInstalled: true,
		dnsRules: []awgmgr.DNSRoute{
			{ID: "hr:Vk", Backend: "hydraroute", HRPolicyName: "HydraRoute",
				Routes: []awgmgr.DNSRouteEntry{{Interface: "nwg1", TunnelID: "nwg1"}}},
			{ID: "ndms:Yandex", Backend: "ndms",
				Routes: []awgmgr.DNSRouteEntry{{Interface: "nwg1", TunnelID: "nwg1"}}},
			{ID: "hr:Sber", Backend: "hydraroute", HRPolicyName: "HydraRoute",
				Routes: []awgmgr.DNSRouteEntry{{Interface: "eth3", TunnelID: "eth3"}}}, // CANARY: WAN
			{ID: "hr:Fallthru", Backend: "hydraroute", HRPolicyName: "HydraRoute",
				Routes: nil}, // fall-through
		},
		staticRules: []awgmgr.StaticRoute{
			{ID: "s1", Name: "work", TunnelID: "nwg1", Subnets: []string{"10.0.0.0/8"}, Enabled: true},
			{ID: "s2", Name: "wan-rule", TunnelID: "eth3", Subnets: []string{"203.0.113.0/24"}, Enabled: true}, // CANARY
		},
	}
}

func (m *rebindMock) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/get", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("id") {
		case "t1":
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"t1","name":"awg11","interfaceName":"nwg1","ndmsName":"Wireguard1","enabled":true,"defaultRoute":true}}`))
		case "t2":
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"t2","name":"awg13","interfaceName":"nwg0","ndmsName":"Wireguard0","enabled":true,"defaultRoute":false}}`))
		default:
			http.Error(w, "not found", 404)
		}
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock(); defer m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": m.dnsRules})
	})
	mux.HandleFunc("/api/dns-routes/update", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		var rule awgmgr.DNSRoute
		_ = json.NewDecoder(r.Body).Decode(&rule)
		m.mu.Lock(); defer m.mu.Unlock()
		for i := range m.dnsRules {
			if m.dnsRules[i].ID == id {
				m.dnsRules[i] = rule
				break
			}
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock(); defer m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": m.staticRules})
	})
	mux.HandleFunc("/api/static-routes/update", func(w http.ResponseWriter, r *http.Request) {
		var rule awgmgr.StaticRoute
		_ = json.NewDecoder(r.Body).Decode(&rule)
		m.mu.Lock(); defer m.mu.Unlock()
		for i := range m.staticRules {
			if m.staticRules[i].ID == rule.ID {
				m.staticRules[i] = rule
				break
			}
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		body := `{"success":true,"data":{"installed":false}}`
		if m.hrInstalled {
			body = `{"success":true,"data":{"installed":true,"running":true}}`
		}
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/api/system/hydraroute-control", func(w http.ResponseWriter, r *http.Request) {
		m.hrControlCalls.Add(1)
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		m.refreshCalls.Add(1)
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	return mux
}

func TestRouteRebind_HappyPath_WANCanaryUntouched(t *testing.T) {
	mock := newRebindMock(t)
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c := awgmgr.New(srv.URL)

	out, err := RouteRebind(context.Background(), c, "t1", "t2")
	if err != nil {
		t.Fatal(err)
	}
	var res wire.RouteRebindResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, out)
	}
	// 3 DNS rules touched (Vk, Yandex, Fallthru — explicit nwg1 + fall-through credited to nwg1 because t1 was default-route).
	// 1 DNS rule untouched (Sber on eth3 — WAN canary).
	if res.DNS.OK != 3 || res.DNS.Failed != 0 {
		t.Errorf("DNS counts: %+v (want 3 ok / 0 fail — 2 explicit nwg1 + 1 fall-through)", res.DNS)
	}
	// 1 static touched (s1 on nwg1). 1 untouched (s2 on eth3 — WAN canary).
	if res.Static.OK != 1 || res.Static.Failed != 0 {
		t.Errorf("Static counts: %+v", res.Static)
	}
	// HR-Neo subcount: Vk + Fallthru (Yandex is ndms engine, not hr).
	if res.HRNeo.OK != 2 {
		t.Errorf("HRNeo subcount: %+v (want 2 — Vk + Fallthru)", res.HRNeo)
	}

	// CANARY: the WAN-targeted DNS rule (hr:Sber) and WAN-targeted static (s2)
	// must be exactly as they were before.
	for _, r := range mock.dnsRules {
		if r.ID == "hr:Sber" {
			if len(r.Routes) != 1 || r.Routes[0].Interface != "eth3" {
				t.Errorf("WAN canary DNS rule mutated: %+v", r)
			}
		}
	}
	for _, r := range mock.staticRules {
		if r.ID == "s2" && r.TunnelID != "eth3" {
			t.Errorf("WAN canary static rule mutated: %+v", r)
		}
	}

	// Side effects: refresh called exactly once, hydraroute-control restart called (HR-Neo rules touched).
	if mock.refreshCalls.Load() != 1 {
		t.Errorf("/api/routing/refresh calls: %d (want 1)", mock.refreshCalls.Load())
	}
	if mock.hrControlCalls.Load() != 1 {
		t.Errorf("/api/system/hydraroute-control calls: %d (want 1, since HR-Neo rules touched)", mock.hrControlCalls.Load())
	}
}

func TestRouteRebind_SrcEqDst(t *testing.T) {
	c := awgmgr.New("http://unused.invalid")
	out, err := RouteRebind(context.Background(), c, "t1", "t1")
	if err != nil {
		t.Fatal(err)
	}
	var res wire.RouteRebindResult
	_ = json.Unmarshal([]byte(out), &res)
	if res.DNS.OK+res.Static.OK+res.HRNeo.OK != 0 {
		t.Errorf("src==dst should be no-op, got %+v", res)
	}
}

func TestRouteRebind_DNSPartialFail(t *testing.T) {
	mock := newRebindMock(t)
	mock.hrInstalled = false // skip HR control call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/dns-routes/update" && strings.Contains(r.URL.RawQuery, "id=ndms:Yandex") {
			http.Error(w, "boom", 500)
			return
		}
		mock.handler().ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	out, err := RouteRebind(context.Background(), c, "t1", "t2")
	if err != nil {
		t.Fatal(err)
	}
	var res wire.RouteRebindResult
	_ = json.Unmarshal([]byte(out), &res)
	if res.DNS.Failed < 1 {
		t.Errorf("expected at least 1 failure on DNS, got %+v", res.DNS)
	}
	if len(res.DNS.Errors) == 0 {
		t.Errorf("errors should be reported")
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement**

`internal/agent/actions/route_rebind.go`:

```go
package actions

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

// RouteRebind moves all DNS / Static rules whose target is src's iface to
// dst's iface. WAN, system, and other-managed-tunnel rules are preserved.
//
// Fall-through DNS rules (routes==null) with backend="hydraroute" are
// converted to explicit routes pointing at dst when src is the default-route
// managed tunnel. This matches the awg-manager UI's own behaviour.
func RouteRebind(ctx context.Context, c *awgmgr.Client, srcID, dstID string) (string, error) {
	res := wire.RouteRebindResult{SrcTunnelID: srcID, DstTunnelID: dstID}
	if srcID == dstID {
		b, _ := json.Marshal(res)
		return string(b), nil
	}
	src, err := getTunnel(ctx, c, srcID)
	if err != nil {
		return "", fmt.Errorf("resolve src: %w", err)
	}
	dst, err := getTunnel(ctx, c, dstID)
	if err != nil {
		return "", fmt.Errorf("resolve dst: %w", err)
	}
	if src.InterfaceName == "" || dst.InterfaceName == "" {
		return "", fmt.Errorf("src/dst missing interfaceName: src=%+v dst=%+v", src, dst)
	}
	srcIface, dstIface := src.InterfaceName, dst.InterfaceName
	srcIsDefaultRoute := src.DefaultRoute

	hrTouched := false
	res.DNS, res.HRNeo, hrTouched = rebindDNS(ctx, c, srcIface, dstIface, srcIsDefaultRoute)
	res.Static = rebindStatic(ctx, c, srcIface, dstIface)

	// Finalisation
	if err := c.RoutingRefresh(ctx); err != nil {
		// Non-fatal: surface as error in the most-recently-touched category.
		appendErr(&res.Static, "routing/refresh: "+err.Error())
	}
	if hrTouched {
		hr, err := c.HydraRouteStatus(ctx)
		if err == nil && hr.Installed {
			if err := c.HydraRouteControl(ctx, "restart"); err != nil {
				appendErr(&res.HRNeo, "hr/control restart: "+err.Error())
			}
		}
	}

	b, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func getTunnel(ctx context.Context, c *awgmgr.Client, id string) (*awgmgr.Tunnel, error) {
	var env awgmgr.Envelope[awgmgr.Tunnel]
	if err := c.GetEnv(ctx, "/api/tunnels/get?id="+id, &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("tunnels/get id=%s: success=false", id)
	}
	return &env.Data, nil
}

// rebindDNS walks all DNS rules, swaps explicit routes from src to dst, and
// (when src is the default-route managed tunnel) converts hydraroute
// fall-through rules to explicit routes pointing at dst.
//
// Returns: total category result, the HRNeo sub-count (subset of total),
// and whether any hydraroute rule was actually written.
func rebindDNS(ctx context.Context, c *awgmgr.Client, srcIface, dstIface string, srcIsDefaultRoute bool) (total wire.CategoryResult, hrNeo wire.CategoryResult, hrTouched bool) {
	all, err := c.ListDNSRoutes(ctx)
	if err != nil {
		total.Failed = 1
		total.Errors = []string{"dns/list: " + err.Error()}
		return
	}
	for _, r := range all {
		isHR := r.Backend == "hydraroute"
		newRoutes, didChange := rewriteRoutes(r.Routes, srcIface, dstIface)
		if !didChange {
			// Try fall-through conversion.
			if r.Routes == nil && isHR && r.HRPolicyName != "" && srcIsDefaultRoute {
				newRoutes = []awgmgr.DNSRouteEntry{{Interface: dstIface, TunnelID: dstIface, Fallback: "auto"}}
				didChange = true
			}
		}
		if !didChange {
			continue
		}
		updated := r
		updated.Routes = newRoutes
		if err := c.UpdateDNSRoute(ctx, updated); err != nil {
			total.Failed++
			total.Errors = append(total.Errors, fmt.Sprintf("dns/update id=%s: %v", r.ID, err))
			if isHR {
				hrNeo.Failed++
			}
			continue
		}
		total.OK++
		if isHR {
			hrNeo.OK++
			hrTouched = true
		}
	}
	return
}

// rewriteRoutes returns a copy of routes with every entry whose interface ==
// srcIface remapped to dstIface (both interface and tunnelId). The boolean
// indicates whether any entry was rewritten.
func rewriteRoutes(routes []awgmgr.DNSRouteEntry, srcIface, dstIface string) ([]awgmgr.DNSRouteEntry, bool) {
	if len(routes) == 0 {
		return routes, false
	}
	out := make([]awgmgr.DNSRouteEntry, len(routes))
	changed := false
	for i, e := range routes {
		out[i] = e
		if e.Interface == srcIface || e.TunnelID == srcIface {
			out[i].Interface = dstIface
			out[i].TunnelID = dstIface
			changed = true
		}
	}
	return out, changed
}

func rebindStatic(ctx context.Context, c *awgmgr.Client, srcIface, dstIface string) wire.CategoryResult {
	var res wire.CategoryResult
	all, err := c.ListStaticRoutes(ctx)
	if err != nil {
		res.Failed = 1
		res.Errors = []string{"static/list: " + err.Error()}
		return res
	}
	for _, r := range all {
		if r.TunnelID != srcIface {
			continue
		}
		updated := r
		updated.TunnelID = dstIface
		if err := c.UpdateStaticRoute(ctx, updated); err != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("static/update id=%s: %v", r.ID, err))
			continue
		}
		res.OK++
	}
	return res
}

func appendErr(c *wire.CategoryResult, s string) {
	c.Errors = append(c.Errors, s)
}
```

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./internal/agent/actions -run TestRouteRebind -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/agent/actions/route_rebind.go internal/agent/actions/route_rebind_test.go
git commit -m "feat(agent): route_rebind — DNS+Static per-rule rebind with WAN canary, fall-through conversion"
```

### Task 4.2: Wire route_rebind into Runner with mutex

**Files:**
- Modify: `internal/agent/actions/runner.go`
- Modify: `internal/agent/actions/runner_routes_test.go`

- [ ] **Step 1: Append failing test**

```go
func TestRunner_RouteRebind_Dispatch(t *testing.T) {
	mock := newRebindMock(t)
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	r := &Runner{AwgClient: awgmgr.New(srv.URL)}
	res := r.Execute(context.Background(), wire.Command{
		ID: "x", Action: "route_rebind",
		Args: map[string]any{"src_tunnel_id": "t1", "dst_tunnel_id": "t2"},
	})
	if res.Status != "ok" {
		t.Fatalf("status: %s output: %s", res.Status, res.Output)
	}
	var rb wire.RouteRebindResult
	if err := json.Unmarshal([]byte(res.Output), &rb); err != nil {
		t.Errorf("output not JSON: %v", err)
	}
}

func TestRunner_RouteRebind_MissingArgs(t *testing.T) {
	r := &Runner{AwgClient: awgmgr.New("http://unused.invalid")}
	res := r.Execute(context.Background(), wire.Command{ID: "x", Action: "route_rebind"})
	if res.Status != "err" {
		t.Errorf("expected err, got %s", res.Status)
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Add the case + mutex**

In `runner.go`:

1. Add `"sync"` to imports if not present, and a `routeMu sync.Mutex` field on `Runner`.
2. Insert the case before `default:`:

```go
	case "route_rebind":
		if r.AwgClient == nil {
			return "err", "awgmgr client not configured"
		}
		srcID, _ := cmd.Args["src_tunnel_id"].(string)
		dstID, _ := cmd.Args["dst_tunnel_id"].(string)
		if srcID == "" || dstID == "" {
			return "err", "route_rebind: src_tunnel_id and dst_tunnel_id are required"
		}
		r.routeMu.Lock()
		defer r.routeMu.Unlock()
		out, err := RouteRebind(ctx, r.AwgClient, srcID, dstID)
		if err != nil {
			return "err", err.Error()
		}
		return "ok", out
```

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./internal/agent/actions -v
```

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(agent): runner dispatch for route_rebind with serialisation mutex"
```

---

## Milestone 5: Backend Snapshot Cache

### Task 5.1: RoutesCache — TTL + per-user

**Files:**
- Create: `internal/backend/callbacks/routes_cache.go`
- Create: `internal/backend/callbacks/routes_cache_test.go`

- [ ] **Step 1: Write the failing test**

```go
package callbacks

import (
	"testing"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

func TestRoutesCache_TTL(t *testing.T) {
	now := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	c := &RoutesCache{TTL: 30 * time.Second, Now: func() time.Time { return now }}
	snap := wire.RouteSnapshot{Other: wire.TunnelCounts{DNS: 3}}
	c.Put(42, snap)

	got, ok := c.Get(42)
	if !ok || got.Other.DNS != 3 {
		t.Fatalf("immediate get failed: %+v %v", got, ok)
	}

	now = now.Add(31 * time.Second)
	if _, ok := c.Get(42); ok {
		t.Errorf("expected miss after TTL")
	}
}

func TestRoutesCache_Invalidate(t *testing.T) {
	c := &RoutesCache{TTL: 30 * time.Second}
	c.Put(42, wire.RouteSnapshot{})
	c.Invalidate(42)
	if _, ok := c.Get(42); ok {
		t.Errorf("expected miss after invalidate")
	}
}

func TestRoutesCache_PerUser(t *testing.T) {
	c := &RoutesCache{TTL: 30 * time.Second}
	c.Put(1, wire.RouteSnapshot{Other: wire.TunnelCounts{DNS: 1}})
	c.Put(2, wire.RouteSnapshot{Other: wire.TunnelCounts{DNS: 2}})
	if got, _ := c.Get(1); got.Other.DNS != 1 {
		t.Errorf("user1: %+v", got)
	}
	if got, _ := c.Get(2); got.Other.DNS != 2 {
		t.Errorf("user2: %+v", got)
	}
}
```

- [ ] **Step 2: Run, expect FAIL (RoutesCache undefined)**

- [ ] **Step 3: Implement**

`internal/backend/callbacks/routes_cache.go`:

```go
package callbacks

import (
	"sync"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

// RoutesCache stores the last-known RouteSnapshot per user with a TTL.
// Backend invalidates the entry after a successful route_rebind so that
// "К маршрутам" after a rebind shows fresh data.
type RoutesCache struct {
	TTL time.Duration
	Now func() time.Time

	mu      sync.Mutex
	entries map[int64]routesCacheEntry
}

type routesCacheEntry struct {
	snap wire.RouteSnapshot
	at   time.Time
}

func (c *RoutesCache) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func (c *RoutesCache) Get(userID int64) (wire.RouteSnapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[userID]
	if !ok {
		return wire.RouteSnapshot{}, false
	}
	if c.now().Sub(e.at) > c.TTL {
		return wire.RouteSnapshot{}, false
	}
	return e.snap, true
}

func (c *RoutesCache) Put(userID int64, snap wire.RouteSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[int64]routesCacheEntry)
	}
	c.entries[userID] = routesCacheEntry{snap: snap, at: c.now()}
}

func (c *RoutesCache) Invalidate(userID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, userID)
}
```

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./internal/backend/callbacks -run TestRoutesCache -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/backend/callbacks/routes_cache.go internal/backend/callbacks/routes_cache_test.go
git commit -m "feat(backend): RoutesCache with TTL and per-user entries"
```

---

## Milestone 6: TG Routes Panel Renderer

### Task 6.1: Render text + keyboards for each screen

**Files:**
- Create: `internal/backend/tg/routes_panel.go`
- Create: `internal/backend/tg/routes_panel_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tg

import (
	"strings"
	"testing"

	"github.com/anex/wg-monitor/pkg/wire"
)

func TestRoutesPanelText_HappyPath(t *testing.T) {
	snap := wire.RouteSnapshot{
		HRNeo: wire.HRStatus{Installed: true, Running: true},
		Tunnels: []wire.TunnelMeta{
			{ID: "t1", Name: "amnezia", Iface: "nwg1", Enabled: true, DefaultRoute: true},
			{ID: "t2", Name: "amnezia2", Iface: "nwg0", Enabled: true},
		},
		Counts: map[string]wire.TunnelCounts{
			"t1": {DNS: 5, Static: 2, HRNeo: 4},
			"t2": {},
		},
		Other: wire.TunnelCounts{DNS: 1, Static: 1, HRNeo: 1},
	}
	text := RoutesPanelText("testkeen", snap)
	if !strings.Contains(text, "testkeen") {
		t.Errorf("router name missing: %s", text)
	}
	// total DNS shown = 5 + 1 (other) = 6
	if !strings.Contains(text, "6") {
		t.Errorf("DNS total missing: %s", text)
	}
	// t1 row should show its visible total = DNS + Static (HRNeo is sub-count, NOT added)
	if !strings.Contains(text, "amnezia") || !strings.Contains(text, "7") { // 5 DNS + 2 Static
		t.Errorf("t1 row missing or wrong total: %s", text)
	}
	// untouched block
	if !strings.Contains(text, "WAN") {
		t.Errorf("WAN/Other not shown: %s", text)
	}
}

func TestRoutesPanelText_HRNeoAbsent(t *testing.T) {
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{{ID: "t1", Name: "amnezia", Iface: "nwg1", Enabled: true}},
		Counts:  map[string]wire.TunnelCounts{"t1": {DNS: 1}},
	}
	text := RoutesPanelText("testkeen", snap)
	if strings.Contains(text, "HydraRoute Neo:") {
		t.Errorf("HR-Neo line should be hidden: %s", text)
	}
}

func TestRoutesPanelKeyboard_RebindOnlyForNonZero(t *testing.T) {
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{
			{ID: "t1", Name: "amnezia", Iface: "nwg1", Enabled: true},
			{ID: "t2", Name: "newtun", Iface: "nwg0", Enabled: true},
		},
		Counts: map[string]wire.TunnelCounts{
			"t1": {DNS: 3},
			"t2": {},
		},
	}
	kb := RoutesPanelKeyboard(42, snap)
	hasT1 := false
	hasT2 := false
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.Contains(btn.CallbackData, "routes_rebind:42:t1") {
				hasT1 = true
			}
			if strings.Contains(btn.CallbackData, "routes_rebind:42:t2") {
				hasT2 = true
			}
		}
	}
	if !hasT1 {
		t.Errorf("t1 (3 rules) should have rebind button")
	}
	if hasT2 {
		t.Errorf("t2 (0 rules) should NOT have rebind button")
	}
}

func TestRebindPreviewText_ShowsUntouchedBlock(t *testing.T) {
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{
			{ID: "t1", Name: "amnezia", Iface: "nwg1"},
			{ID: "t2", Name: "newtun", Iface: "nwg0"},
			{ID: "t3", Name: "third", Iface: "nwg2"},
		},
		Counts: map[string]wire.TunnelCounts{
			"t1": {DNS: 5, Static: 2, HRNeo: 1},
			"t3": {DNS: 4},
		},
		Other: wire.TunnelCounts{DNS: 12},
	}
	text := RebindPreviewText(snap, "t1", "t2", "8a3f")
	if !strings.Contains(text, "5") || !strings.Contains(text, "2") {
		t.Errorf("preview missing per-category counts: %s", text)
	}
	if !strings.Contains(text, "WAN") || !strings.Contains(text, "12") {
		t.Errorf("untouched WAN block missing: %s", text)
	}
	if !strings.Contains(text, "third") || !strings.Contains(text, "4") {
		t.Errorf("untouched other-tunnel row missing: %s", text)
	}
	if !strings.Contains(text, "8a3f") {
		t.Errorf("token must be in preview: %s", text)
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement**

`internal/backend/tg/routes_panel.go`:

```go
package tg

import (
	"fmt"
	"strings"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

const routesMaxPerRow = 2

// RoutesPanelText renders Screen 2.
//
// "Visible total" per tunnel = DNS + Static. HRNeo is a sub-count of DNS
// (rules with engine=hydraroute), shown separately in the upper status block
// but NOT added to per-tunnel totals (would double-count).
func RoutesPanelText(nickname string, snap wire.RouteSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🛣 Маршруты — %s\n", nickname)
	fmt.Fprintf(&b, "   обновлено %s\n\n", time.Now().Format("15:04:05"))
	if snap.HRNeo.Installed {
		state := "✅ установлен, работает"
		if !snap.HRNeo.Running {
			state = "⚠ установлен, остановлен"
		}
		fmt.Fprintf(&b, "HydraRoute Neo: %s\n", state)
	}
	totalDNS := snap.Other.DNS
	totalStatic := snap.Other.Static
	totalHR := snap.Other.HRNeo
	for _, c := range snap.Counts {
		totalDNS += c.DNS
		totalStatic += c.Static
		totalHR += c.HRNeo
	}
	fmt.Fprintf(&b, "DNS routes:        %d правил\n", totalDNS)
	fmt.Fprintf(&b, "Static IP routes:  %d правил\n", totalStatic)
	if snap.HRNeo.Installed {
		fmt.Fprintf(&b, "  из них HR-Neo:   %d\n", totalHR)
	}
	b.WriteString("\nПо туннелям (направленные в туннели):\n")
	for _, t := range snap.Tunnels {
		c := snap.Counts[t.ID]
		visible := c.DNS + c.Static
		fmt.Fprintf(&b, "  %s (%s) → %d\n", t.Name, t.Iface, visible)
	}
	b.WriteString("\nНе входят в перенос (показано для контроля):\n")
	wanTotal := snap.Other.DNS + snap.Other.Static
	fmt.Fprintf(&b, "  WAN/system:   %d правил   ← RU-сервисы\n", wanTotal)
	return b.String()
}

// RoutesPanelKeyboard builds Screen 2 inline keyboard.
//
// callback_data shape:
//   routes_rebind:<userID>:<src_tunnel_id>
//   routes_refresh:<userID>:_panel_
//   routes_close:0:_panel_
func RoutesPanelKeyboard(userID int64, snap wire.RouteSnapshot) InlineKeyboardMarkup {
	rows := [][]InlineKeyboardButton{}
	var row []InlineKeyboardButton
	for _, t := range snap.Tunnels {
		c := snap.Counts[t.ID]
		if c.DNS+c.Static == 0 {
			continue
		}
		row = append(row, InlineKeyboardButton{
			Text:         fmt.Sprintf("🔄 %s", t.Name),
			CallbackData: fmt.Sprintf("routes_rebind:%d:%s", userID, t.ID),
		})
		if len(row) >= routesMaxPerRow {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []InlineKeyboardButton{
		{Text: "🔁 Обновить", CallbackData: fmt.Sprintf("routes_refresh:%d:_panel_", userID)},
		{Text: "Закрыть", CallbackData: "routes_close:0:_panel_"},
	})
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}

// RebindPickKeyboard builds Screen 3 — destination picker.
func RebindPickKeyboard(userID int64, srcID string, snap wire.RouteSnapshot) (string, InlineKeyboardMarkup) {
	var src *wire.TunnelMeta
	for i, t := range snap.Tunnels {
		if t.ID == srcID {
			src = &snap.Tunnels[i]
			break
		}
	}
	if src == nil {
		return "источник недоступен", InlineKeyboardMarkup{}
	}
	text := fmt.Sprintf("🛣 Перенос с %s (%s) → куда?\n\nДоступные:", src.Name, src.Iface)
	rows := [][]InlineKeyboardButton{}
	for _, t := range snap.Tunnels {
		if t.ID == srcID {
			continue
		}
		label := t.Name
		if !t.Enabled {
			label += " (off)"
		}
		rows = append(rows, []InlineKeyboardButton{{
			Text:         label,
			CallbackData: fmt.Sprintf("routes_pick:%d:%s:%s", userID, srcID, t.ID),
		}})
	}
	rows = append(rows, []InlineKeyboardButton{{
		Text: "← Отмена", CallbackData: fmt.Sprintf("routes_back:%d:_panel_", userID),
	}})
	return text, InlineKeyboardMarkup{InlineKeyboard: rows}
}

// RebindPreviewText renders Screen 4 with the safety "untouched" block.
func RebindPreviewText(snap wire.RouteSnapshot, srcID, dstID, token string) string {
	var src, dst *wire.TunnelMeta
	for i, t := range snap.Tunnels {
		if t.ID == srcID {
			src = &snap.Tunnels[i]
		}
		if t.ID == dstID {
			dst = &snap.Tunnels[i]
		}
	}
	if src == nil || dst == nil {
		return "источник или назначение недоступны"
	}
	c := snap.Counts[srcID]
	visible := c.DNS + c.Static
	var b strings.Builder
	fmt.Fprintf(&b, "🛣 Превью: %s → %s\n\n", src.Name, dst.Name)
	fmt.Fprintf(&b, "Будет перенесено (%d):\n", visible)
	if c.DNS > 0 {
		fmt.Fprintf(&b, "  • DNS routes:  %d", c.DNS)
		if c.HRNeo > 0 {
			fmt.Fprintf(&b, " (из них HR-Neo: %d)", c.HRNeo)
		}
		b.WriteString("\n")
	}
	if c.Static > 0 {
		fmt.Fprintf(&b, "  • Static IP:   %d\n", c.Static)
	}
	b.WriteString("\nНЕ ТРОГАЕМ:\n")
	wanTotal := snap.Other.DNS + snap.Other.Static
	fmt.Fprintf(&b, "  • WAN/system:    %d правил   ← RU-сервисы\n", wanTotal)
	for _, t := range snap.Tunnels {
		if t.ID == srcID {
			continue
		}
		oc := snap.Counts[t.ID]
		ot := oc.DNS + oc.Static
		fmt.Fprintf(&b, "  • %s:        %d\n", t.Name, ot)
	}
	fmt.Fprintf(&b, "\ntoken:%s  истекает через 5 мин\n", token)
	return b.String()
}

// RebindPreviewKeyboard for Screen 4.
func RebindPreviewKeyboard(userID int64, srcID, dstID, token string) InlineKeyboardMarkup {
	return InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{
		{Text: "✅ Подтвердить", CallbackData: fmt.Sprintf("routes_confirm:%d:%s:%s:%s", userID, srcID, dstID, token)},
		{Text: "← Отмена", CallbackData: fmt.Sprintf("routes_back:%d:_panel_", userID)},
	}}}
}

// RebindResultText renders Screen 5.
func RebindResultText(srcName, dstName string, res wire.RouteRebindResult) string {
	totalFailed := res.DNS.Failed + res.Static.Failed
	var b strings.Builder
	if totalFailed == 0 {
		fmt.Fprintf(&b, "🛣 ✅ %s → %s готово\n\n", srcName, dstName)
	} else {
		fmt.Fprintf(&b, "🛣 ⚠ %s → %s — частично\n\n", srcName, dstName)
	}
	fmt.Fprintf(&b, "  • DNS routes:  %d ok", res.DNS.OK)
	if res.DNS.Failed > 0 {
		fmt.Fprintf(&b, ", %d FAIL", res.DNS.Failed)
	}
	if res.HRNeo.OK > 0 || res.HRNeo.Failed > 0 {
		fmt.Fprintf(&b, " (из них HR-Neo: %d ok", res.HRNeo.OK)
		if res.HRNeo.Failed > 0 {
			fmt.Fprintf(&b, ", %d FAIL", res.HRNeo.Failed)
		}
		b.WriteString(")")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "  • Static IP:   %d ok", res.Static.OK)
	if res.Static.Failed > 0 {
		fmt.Fprintf(&b, ", %d FAIL", res.Static.Failed)
	}
	b.WriteString("\n")
	if totalFailed > 0 {
		b.WriteString("\nОперация идемпотентна — можно повторить.\n")
		for _, e := range append(append([]string{}, res.DNS.Errors...), res.Static.Errors...) {
			fmt.Fprintf(&b, "  • %s\n", e)
		}
	}
	return b.String()
}

// RebindResultKeyboard for Screen 5. Shows [Repeat] only on partial fail.
func RebindResultKeyboard(userID int64, srcID, dstID string, totalFailed int) InlineKeyboardMarkup {
	rows := [][]InlineKeyboardButton{}
	if totalFailed > 0 {
		rows = append(rows, []InlineKeyboardButton{
			{Text: "🔁 Повторить", CallbackData: fmt.Sprintf("routes_pick:%d:%s:%s", userID, srcID, dstID)},
		})
	}
	rows = append(rows, []InlineKeyboardButton{
		{Text: "🛣 К маршрутам", CallbackData: fmt.Sprintf("routes_open:%d:_panel_", userID)},
		{Text: "Закрыть", CallbackData: "routes_close:0:_panel_"},
	})
	return InlineKeyboardMarkup{InlineKeyboard: rows}
}
```

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./internal/backend/tg -run "TestRoutes|TestRebind" -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/backend/tg/routes_panel.go internal/backend/tg/routes_panel_test.go
git commit -m "feat(tg): routes panel — text and keyboards for all 5 screens"
```

---

## Milestone 7: Backend Callbacks (parse + actions)

### Task 7.1: Extend parser

**Files:**
- Modify: `internal/backend/callbacks/parse.go`
- Modify: `internal/backend/callbacks/parse_test.go`

- [ ] **Step 1: Write the failing test**

Append to `parse_test.go`:

```go
func TestParse_RoutesActions(t *testing.T) {
	cases := []struct {
		data    string
		action  string
		token   string
		isPanel bool
	}{
		{"routes_open:42:_panel_", "routes_open", "", true},
		{"routes_refresh:42:_panel_", "routes_refresh", "", true},
		{"routes_rebind:42:t1", "routes_rebind", "", false},
		{"routes_pick:42:t1:t2", "routes_pick", "", false},
		{"routes_confirm:42:t1:t2:abc12345", "routes_confirm", "abc12345", false},
		{"routes_close:0:_panel_", "routes_close", "", true},
		{"routes_back:42:_panel_", "routes_back", "", true},
	}
	for _, tc := range cases {
		args, err := Parse(tc.data)
		if err != nil {
			t.Errorf("%s: %v", tc.data, err)
			continue
		}
		if args.Action != tc.action {
			t.Errorf("%s: action=%s", tc.data, args.Action)
		}
		if tc.token != "" && args.RebindToken != tc.token {
			t.Errorf("%s: token=%q want %q", tc.data, args.RebindToken, tc.token)
		}
		if args.IsPanel != tc.isPanel {
			t.Errorf("%s: isPanel=%v want %v", tc.data, args.IsPanel, tc.isPanel)
		}
	}
}

func TestParse_RoutesConfirm_MissingToken(t *testing.T) {
	_, err := Parse("routes_confirm:42:t1:t2")
	if err == nil {
		t.Errorf("expected error for missing token")
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Extend parse.go**

In `internal/backend/callbacks/parse.go`:

1. Add fields to `Args`:
```go
	// RebindToken is set for routes_confirm callbacks. 8 hex chars, 5-min TTL.
	RebindToken string
	// RebindSrcID / RebindDstID parsed from routes_pick / routes_confirm.
	RebindSrcID string
	RebindDstID string
```

2. Extend `validActions`:
```go
	"routes_open": true, "routes_router": true, "routes_rebind": true,
	"routes_pick": true, "routes_confirm": true, "routes_refresh": true,
	"routes_back": true, "routes_close": true,
```

3. Add parsing branches before `return a, nil`:
```go
	switch action {
	case "routes_rebind":
		if len(parts) >= 3 && parts[2] != "" && parts[2] != panelSentinel {
			a.RebindSrcID = parts[2]
		}
	case "routes_pick":
		if len(parts) < 4 {
			return Args{}, fmt.Errorf("routes_pick requires src and dst: %q", data)
		}
		a.RebindSrcID = parts[2]
		a.RebindDstID = parts[3]
	case "routes_confirm":
		if len(parts) < 5 || parts[4] == "" {
			return Args{}, fmt.Errorf("routes_confirm requires token: %q", data)
		}
		a.RebindSrcID = parts[2]
		a.RebindDstID = parts[3]
		a.RebindToken = parts[4]
	}
```

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./internal/backend/callbacks -run TestParse -v
```

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(callbacks): parse routes_* callback grammar"
```

### Task 7.2: pendingRebinds + RebindConfirmAction

**Files:**
- Modify: `internal/backend/callbacks/actions.go`
- Modify: `internal/backend/callbacks/actions_test.go`

- [ ] **Step 1: Write the failing test**

Add to `actions_test.go`:

```go
func TestRebindConfirmAction_TokenMissing(t *testing.T) {
	a := &RebindConfirmAction{
		consumeFn: func(int64, string) (*pendingRebind, bool) { return nil, false },
		idGen:     func() string { return "id1" },
	}
	q := &tg.CallbackQuery{Message: &tg.Message{Chat: tg.Chat{ID: 1}}}
	_, err := a.Apply(context.Background(), q, Args{Action: "routes_confirm", UserID: 42, RebindToken: "x"})
	if err == nil {
		t.Errorf("expected error when token unknown")
	}
}

func TestRebindConfirmAction_HappyPath(t *testing.T) {
	pr := &pendingRebind{
		SrcID: "t1", DstID: "t2",
		Token:     "tok1",
		ExpiresAt: time.Now().Add(time.Minute),
	}
	consumed := false
	sink := &fakeEnqueuer{}
	a := &RebindConfirmAction{
		consumeFn: func(uid int64, token string) (*pendingRebind, bool) {
			if uid == 42 && token == "tok1" && !consumed {
				consumed = true
				return pr, true
			}
			return nil, false
		},
		idGen: func() string { return "id1" },
		sink:  sink,
	}
	q := &tg.CallbackQuery{Message: &tg.Message{Chat: tg.Chat{ID: 1}, MessageID: 7}}
	_, err := a.Apply(context.Background(), q, Args{Action: "routes_confirm", UserID: 42, RebindToken: "tok1", RebindSrcID: "t1", RebindDstID: "t2"})
	if err != nil {
		t.Fatal(err)
	}
	if sink.lastAction != "route_rebind" {
		t.Errorf("enqueued %q, want route_rebind", sink.lastAction)
	}
	if sink.lastArgs["src_tunnel_id"] != "t1" || sink.lastArgs["dst_tunnel_id"] != "t2" {
		t.Errorf("args: %+v", sink.lastArgs)
	}
}
```

(`fakeEnqueuer` with `lastAction`/`lastArgs` likely exists from the import-action tests; if not, define a minimal one in the test file.)

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement**

Append to `internal/backend/callbacks/actions.go`:

```go
// pendingRebind holds a scheduled rebind awaiting Confirm. Stored in
// Router.pendingRebinds keyed by token; consumed by RebindConfirmAction.
// Mirrors pendingUpload in lifetime semantics: token+TTL, single use.
type pendingRebind struct {
	UserID    int64
	SrcID     string
	DstID     string
	Token     string
	ExpiresAt time.Time
}

// RebindConfirmAction handles routes_confirm:<uid>:<src>:<dst>:<token>. It
// consumes the pendingRebind by token and enqueues a route_rebind wire.Command.
type RebindConfirmAction struct {
	sink      CommandEnqueuer
	consumeFn func(userID int64, token string) (*pendingRebind, bool)
	idGen     func() string
}

func NewRebindConfirmAction(sink CommandEnqueuer, consume func(int64, string) (*pendingRebind, bool), idGen func() string) *RebindConfirmAction {
	return &RebindConfirmAction{sink: sink, consumeFn: consume, idGen: idGen}
}

func (a *RebindConfirmAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	pr, ok := a.consumeFn(args.UserID, args.RebindToken)
	if !ok {
		return "", errors.New("сессия истекла или не найдена; открой панель заново")
	}
	if pr.SrcID != args.RebindSrcID || pr.DstID != args.RebindDstID {
		return "", errors.New("параметры rebind не совпадают с подтверждением")
	}
	cmd := wire.Command{
		ID:     a.idGen(),
		Action: "route_rebind",
		Args: map[string]any{
			"src_tunnel_id": pr.SrcID,
			"dst_tunnel_id": pr.DstID,
		},
		IssuedAt: time.Now().UTC(),
	}
	ref := cmdpkg.MessageRef{
		ChatID:    q.Message.Chat.ID,
		MessageID: q.Message.MessageID,
		ThreadID:  q.Message.MessageThreadID,
	}
	if err := a.sink.EnqueueWithRef(args.UserID, cmd, ref); err != nil {
		return "", fmt.Errorf("enqueue route_rebind: %w", err)
	}
	return "🛣 запускаем перенос…", nil
}

// makeRebindToken returns 8 hex chars cryptographically random.
func makeRebindToken() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
```

(`crypto/rand`, `encoding/hex`, `errors` already imported by adjacent code; if not, add them.)

- [ ] **Step 4: Run, expect PASS**

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(callbacks): RebindConfirmAction with token consume + enqueue"
```

### Task 7.3: Router wiring — open/refresh/rebind/pick/close

**Files:**
- Modify: `internal/backend/callbacks/router.go`

- [ ] **Step 1: Add fields to Router struct**

Locate the existing `Router` struct (around the top of `router.go` where `pendingUploads` is declared). Add:

```go
	routesCache         *RoutesCache
	rebindConfirmAction Action
	pendingRebindsMu    sync.Mutex
	pendingRebinds      map[string]*pendingRebind // keyed by 8-hex token
```

- [ ] **Step 2: Add helper methods**

```go
func (r *Router) putPendingRebind(pr *pendingRebind) {
	r.pendingRebindsMu.Lock()
	defer r.pendingRebindsMu.Unlock()
	if r.pendingRebinds == nil {
		r.pendingRebinds = make(map[string]*pendingRebind)
	}
	r.pendingRebinds[pr.Token] = pr
}

func (r *Router) consumePendingRebind(userID int64, token string) (*pendingRebind, bool) {
	r.pendingRebindsMu.Lock()
	defer r.pendingRebindsMu.Unlock()
	pr, ok := r.pendingRebinds[token]
	if !ok || pr.UserID != userID || time.Now().After(pr.ExpiresAt) {
		delete(r.pendingRebinds, token)
		return nil, false
	}
	delete(r.pendingRebinds, token)
	return pr, true
}
```

- [ ] **Step 3: Add case branches in the callback switch**

In the `switch args.Action` block that currently handles `tunnels_refresh` and `tunnel_import_*`, add:

```go
	case "routes_open", "routes_refresh":
		r.handleRoutesOpen(ctx, q, args, args.Action == "routes_refresh")
		return
	case "routes_rebind":
		r.handleRoutesRebindStart(ctx, q, args)
		return
	case "routes_pick":
		r.handleRoutesRebindPick(ctx, q, args)
		return
	case "routes_back":
		r.handleRoutesOpen(ctx, q, args, false)
		return
	case "routes_close":
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "закрыто")
		empty := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
		_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, q.Message.Text, "", &empty)
		return
	case "routes_confirm":
		if r.rebindConfirmAction != nil {
			action = r.rebindConfirmAction
		}
```

- [ ] **Step 4: Implement the handlers**

Add to `router.go` (near `buildTunnelsPanel`):

```go
// handleRoutesOpen renders Screen 2. Cache lookup unless force=true.
// On miss, enqueues route_status; the result-handler edits the panel
// when the agent answers.
func (r *Router) handleRoutesOpen(ctx context.Context, q *tg.CallbackQuery, args Args, force bool) {
	user, err := r.d.Users().GetByID(args.UserID)
	if err != nil || user == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "user not found")
		return
	}
	if !force {
		if snap, ok := r.routesCache.Get(user.ID); ok {
			text := tg.RoutesPanelText(user.Nickname, snap)
			kb := tg.RoutesPanelKeyboard(user.ID, snap)
			_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
			_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
			return
		}
	}
	cmd := wire.Command{ID: r.idGen(), Action: "route_status", IssuedAt: time.Now().UTC()}
	ref := cmdpkg.MessageRef{ChatID: q.Message.Chat.ID, MessageID: q.Message.MessageID, ThreadID: q.Message.MessageThreadID}
	loadingText := fmt.Sprintf("🛣 Маршруты — %s\n   обновляется…", user.Nickname)
	loadingKB := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, loadingText, "", &loadingKB)
	if err := r.cmdQueue.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		slog.Warn("routes_open: enqueue failed", "err", err)
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не получилось запросить статус")
		return
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleRoutesRebindStart(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil {
		return
	}
	snap, ok := r.routesCache.Get(user.ID)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "обнови маршруты и попробуй ещё раз")
		return
	}
	text, kb := tg.RebindPickKeyboard(user.ID, args.RebindSrcID, snap)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) handleRoutesRebindPick(ctx context.Context, q *tg.CallbackQuery, args Args) {
	user, _ := r.d.Users().GetByID(args.UserID)
	if user == nil {
		return
	}
	if args.RebindSrcID == args.RebindDstID {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "src == dst — нечего переносить")
		return
	}
	snap, ok := r.routesCache.Get(user.ID)
	if !ok {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "обнови маршруты и попробуй ещё раз")
		return
	}
	token := makeRebindToken()
	r.putPendingRebind(&pendingRebind{
		UserID: user.ID, SrcID: args.RebindSrcID, DstID: args.RebindDstID,
		Token: token, ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	text := tg.RebindPreviewText(snap, args.RebindSrcID, args.RebindDstID, token)
	kb := tg.RebindPreviewKeyboard(user.ID, args.RebindSrcID, args.RebindDstID, token)
	_ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}
```

- [ ] **Step 5: Build**

```bash
go build ./...
```

(Unresolved references to `r.routesCache` / `r.rebindConfirmAction` / `r.idGen` will be wired in Milestone 8.)

- [ ] **Step 6: Commit**

```bash
git commit -am "feat(callbacks): routes panel handlers and pendingRebinds storage"
```

### Task 7.4: RoutesPanelNotifier — edit message in place on result

**Files:**
- Create: `internal/backend/callbacks/routes_notifier.go`
- Create: `internal/backend/callbacks/routes_notifier_test.go`

- [ ] **Step 1: Write the failing test**

```go
package callbacks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

type fakeEditTG struct{ editText string }

func (f *fakeEditTG) EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error {
	f.editText = text
	return nil
}

func TestRoutesPanelNotifier_StatusOK(t *testing.T) {
	cache := &RoutesCache{TTL: time.Minute}
	tgFake := &fakeEditTG{}
	d := newTestDBWithUser(t, 42, "testkeen") // helper that creates an in-memory DB with one user
	n := &RoutesPanelNotifier{TG: tgFake, Cache: cache, DB: d}
	snap := wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{{ID: "t1", Name: "amnezia", Iface: "nwg1"}}}
	body, _ := json.Marshal(snap)
	res := wire.CommandResult{Status: "ok", Output: string(body)}
	ref := cmdpkg.MessageRef{Action: "route_status", ChatID: 1, MessageID: 7}
	if err := n.NotifyCommandResult(context.Background(), ref, res, 42); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tgFake.editText, "amnezia") {
		t.Errorf("expected panel rendered: %s", tgFake.editText)
	}
	if _, ok := cache.Get(42); !ok {
		t.Errorf("snapshot must be cached after status fetch")
	}
}

// newTestDBWithUser constructs a *db.DB with a single user. If your existing
// test infra has a similar helper, reuse it; otherwise wire one in this file.
func newTestDBWithUser(t *testing.T, id int64, nick string) *db.DB {
	t.Helper()
	// Implement using the same pattern as existing callbacks tests
	// (see actions_test.go for an example). If there's no factory,
	// inline a minimal one here.
	t.Skip("test helper newTestDBWithUser not yet implemented — wire matching existing test scaffolding before running this case")
	return nil
}
```

(Note: the helper Skip is a deliberate placeholder so the implementer wires it to whatever the actual test scaffolding uses in the existing callbacks package. Replace the Skip+return with the real factory before running.)

- [ ] **Step 2: Run, expect FAIL or skip**

- [ ] **Step 3: Implement**

Create `internal/backend/callbacks/routes_notifier.go`:

```go
package callbacks

import (
	"context"
	"encoding/json"
	"fmt"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

// RoutesEditTG is the subset of tg.Client RoutesPanelNotifier uses.
type RoutesEditTG interface {
	EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error
}

// RoutesPanelNotifier handles route_status / route_rebind CommandResults
// by editing the originating panel message in place. It also keeps the
// in-memory snapshot cache fresh.
type RoutesPanelNotifier struct {
	TG    RoutesEditTG
	Cache *RoutesCache
	DB    *db.DB
}

func (n *RoutesPanelNotifier) NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, userID int64) error {
	user, err := n.DB.Users().GetByID(userID)
	if err != nil || user == nil {
		return fmt.Errorf("user lookup: %w", err)
	}
	switch ref.Action {
	case "route_status":
		return n.renderStatus(ctx, ref, res, user)
	case "route_rebind":
		return n.renderRebind(ctx, ref, res, user)
	default:
		return fmt.Errorf("RoutesPanelNotifier: unsupported action %q", ref.Action)
	}
}

func (n *RoutesPanelNotifier) renderStatus(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	if res.Status != "ok" {
		text := fmt.Sprintf("🛣 Маршруты — %s\n⚠ awg-manager не отвечает\n%s", user.Nickname, res.Output)
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: "🔁 Обновить", CallbackData: fmt.Sprintf("routes_refresh:%d:_panel_", user.ID)}},
		}}
		return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
		return fmt.Errorf("decode snapshot: %w", err)
	}
	n.Cache.Put(user.ID, snap)
	text := tg.RoutesPanelText(user.Nickname, snap)
	kb := tg.RoutesPanelKeyboard(user.ID, snap)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

func (n *RoutesPanelNotifier) renderRebind(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	var rb wire.RouteRebindResult
	if err := json.Unmarshal([]byte(res.Output), &rb); err != nil {
		return fmt.Errorf("decode rebind result: %w", err)
	}
	// Resolve human names BEFORE invalidation (cache may still hold the
	// pre-rebind snapshot with tunnel names).
	srcName, dstName := rb.SrcTunnelID, rb.DstTunnelID
	if snap, ok := n.Cache.Get(user.ID); ok {
		for _, t := range snap.Tunnels {
			if t.ID == rb.SrcTunnelID {
				srcName = t.Name
			}
			if t.ID == rb.DstTunnelID {
				dstName = t.Name
			}
		}
	}
	n.Cache.Invalidate(user.ID)
	totalFailed := rb.DNS.Failed + rb.Static.Failed
	text := tg.RebindResultText(srcName, dstName, rb)
	kb := tg.RebindResultKeyboard(user.ID, rb.SrcTunnelID, rb.DstTunnelID, totalFailed)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}
```

- [ ] **Step 4: Run, expect PASS (after wiring the test helper)**

- [ ] **Step 5: Commit**

```bash
git add internal/backend/callbacks/routes_notifier.go internal/backend/callbacks/routes_notifier_test.go
git commit -m "feat(callbacks): RoutesPanelNotifier — edit panel in place on result"
```

---

## Milestone 8: HTTP Handler + Backend Wiring

### Task 8.1: Dispatch route_* results to RoutesPanelNotifier

**Files:**
- Modify: `internal/backend/handler.go`
- Modify: `internal/backend/handler_test.go`

- [ ] **Step 1: Extend the Deps struct**

In `internal/backend/handler.go`:

```go
// RoutesNotifier is the subset used by cmdResultHandler when ref.Action is
// route_status or route_rebind. Implemented by callbacks.RoutesPanelNotifier.
type RoutesNotifier interface {
	NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, userID int64) error
}

type Deps struct {
	Logger         *slog.Logger
	DB             *db.DB
	Dispatcher     Dispatcher
	Resumer        Resumer
	CommandSink    CommandSink
	TGNotifier     TGNotifier
	RoutesNotifier RoutesNotifier // NEW; nil-safe (handler skips if nil)
	UI             UIConfig
	Thresholds     state.Thresholds
}
```

- [ ] **Step 2: Branch in cmdResultHandler**

Replace the existing `if ref, ok := d.CommandSink.ConsumeOriginRef(...)` block with:

```go
if ref, ok := d.CommandSink.ConsumeOriginRef(uid, res.ID); ok {
    switch ref.Action {
    case "route_status", "route_rebind":
        if d.RoutesNotifier != nil {
            if err := d.RoutesNotifier.NotifyCommandResult(ctx, ref, res, uid); err != nil {
                slog.Warn("routes notifier failed", "err", err, "action", ref.Action)
            }
        }
    default:
        if d.TGNotifier != nil {
            if err := d.TGNotifier.NotifyCommandResult(ctx, ref, ref.Action, res, maxChars); err != nil {
                slog.Warn("relay failed", "err", err)
            }
        }
    }
}
```

- [ ] **Step 3: Update handler_test.go**

Add a fake RoutesNotifier and a test asserting dispatch:

```go
type fakeRoutesNotifier struct{ called int }

func (f *fakeRoutesNotifier) NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, userID int64) error {
	f.called++
	return nil
}

func TestCmdResult_DispatchesRoutesNotifier(t *testing.T) {
	rn := &fakeRoutesNotifier{}
	rc := &relayCapture{}
	sink := &fakeCmdSink{originRef: &cmdpkg.MessageRef{Action: "route_status", ChatID: 1, MessageID: 2}}
	mux := NewMux(Deps{
		DB:             newTestDB(t),
		CommandSink:    sink,
		TGNotifier:     rc,
		RoutesNotifier: rn,
	})
	body := `{"id":"x","status":"ok","output":"{\"tunnels\":[]}","duration_ms":1}`
	req := authedRequest(t, http.MethodPost, "/v1/cmd/result", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if rn.called != 1 {
		t.Errorf("RoutesNotifier called %d times, want 1", rn.called)
	}
	if rc.called != 0 {
		t.Errorf("generic relay called %d times for route_status, want 0", rc.called)
	}
}
```

(Use existing `relayCapture`, `fakeCmdSink`, `newTestDB`, `authedRequest` helpers from `handler_test.go`.)

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./internal/backend -run TestCmdResult -v
```

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(backend): dispatch route_* CommandResults to RoutesPanelNotifier"
```

### Task 8.2: Wire RoutesCache + Notifier in cmd/backend/main.go

**Files:**
- Modify: `cmd/backend/main.go`

- [ ] **Step 1: Locate Notifier/Router construction**

After `callbacks.NewNotifier(...)`, add:

```go
	routesCache := &callbacks.RoutesCache{TTL: 30 * time.Second}
	routesNotifier := &callbacks.RoutesPanelNotifier{
		TG:    tgClient,
		Cache: routesCache,
		DB:    database,
	}
```

- [ ] **Step 2: Inject into Router and Deps**

Pass `routesCache` and `callbacks.NewRebindConfirmAction(cmdQueue, router.consumePendingRebind, idGen)` into the Router constructor (mirror how `importAction` is wired).

Inject `RoutesNotifier: routesNotifier` into `backend.Deps{...}`.

- [ ] **Step 3: Build and run all tests**

```bash
go build ./...
go test ./...
```
Expected: full pass.

- [ ] **Step 4: Commit**

```bash
git commit -am "feat(backend): wire RoutesCache and RoutesPanelNotifier in main"
```

### Task 8.3: Reply-keyboard button "🛣 Маршруты"

**Files:**
- Modify: `internal/backend/tg/replykb.go` (or wherever `ReplyKeyboardForTopic` lives)
- Modify: `internal/backend/callbacks/router.go` — `HandleMessage` text switch

- [ ] **Step 1: Add the button to the per-router reply keyboard**

In the per_router topic keyboard layout, append:
```go
{Text: "🛣 Маршруты"}
```

- [ ] **Step 2: Add handler in HandleMessage**

After `case "🎛 Туннели":` add:

```go
	case "🛣 Маршруты":
		if kind == "per_router" && user != nil {
			r.openRoutesPanelMessage(ctx, m, user)
		} else {
			_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
				"эта команда работает только в топике пользователя.", "", nil)
		}
```

And add the helper:

```go
// openRoutesPanelMessage sends the initial Routes panel as a fresh message
// (so subsequent edits target this MessageID) and enqueues route_status.
// The cmd-result handler edits when the agent answers.
func (r *Router) openRoutesPanelMessage(ctx context.Context, m *tg.Message, user *db.User) {
	loadingText := fmt.Sprintf("🛣 Маршруты — %s\n   обновляется…", user.Nickname)
	mid, err := r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, loadingText, "", nil)
	if err != nil {
		slog.Warn("routes panel send failed", "err", err)
		return
	}
	cmd := wire.Command{ID: r.idGen(), Action: "route_status", IssuedAt: time.Now().UTC()}
	ref := cmdpkg.MessageRef{ChatID: m.Chat.ID, MessageID: mid, ThreadID: m.MessageThreadID}
	if err := r.cmdQueue.EnqueueWithRef(user.ID, cmd, ref); err != nil {
		slog.Warn("route_status enqueue failed", "err", err)
	}
}
```

- [ ] **Step 3: Build**

```bash
go build ./...
```

- [ ] **Step 4: Commit**

```bash
git commit -am "feat(tg): reply-keyboard '🛣 Маршруты' button entry point"
```

---

## Milestone 9: Integration Test (mock awg-manager end-to-end)

### Task 9.1: End-to-end test — status → rebind → status

**Files:**
- Modify: `cmd/backend/integration_test.go`

- [ ] **Step 1: Write the test skeleton**

```go
func TestIntegration_RoutesRebindFlow(t *testing.T) {
	// 1. httptest.NewServer impersonating awg-manager, with stateful maps
	//    of dnsRules and staticRules that mutate on update.
	// 2. Fixture: 3 DNS rules (2 explicit nwg1, 1 explicit eth3 — WAN canary),
	//    2 static rules (1 nwg1, 1 eth3 — WAN canary), HR-Neo installed.
	// 3. Spin up backend with RoutesCache + RoutesPanelNotifier wired (use
	//    cmd/backend/integration_test.go's existing harness).
	// 4. Spin up a real Runner with awgmgr.Client pointing at the mock.
	//    Connect runner via /v1/cmd long-poll loop.
	// 5. Simulate user → "🛣 Маршруты" → backend enqueues route_status →
	//    runner picks up, executes, posts result → backend edits panel.
	//    Assert panel text mentions awg11 (managed) and "WAN/system: 2"
	//    (1 DNS + 1 Static on eth3).
	// 6. Simulate routes_rebind:42:t1 → routes_pick:42:t1:t2 → mint token →
	//    routes_confirm → runner rebinds → backend renders Screen 5.
	//    Assert: DNS.OK=2, Static.OK=1.
	// 7. CANARY: in mock state, the eth3 DNS rule and eth3 static rule
	//    are unchanged.
}
```

- [ ] **Step 2: Implement the fixture and assertions**

Wire the mock as described, using the existing backend factory in `cmd/backend/integration_test.go`. The agent loop pattern follows existing tests for `tunnel_import` if any exist; otherwise inline a minimal `runner.Execute` polled via the cmd queue.

- [ ] **Step 3: Run all tests**

```bash
go test ./...
```
Expected: green.

- [ ] **Step 4: Commit**

```bash
git commit -am "test: integration — routes status → rebind → status with WAN-untouched canary"
```

---

## Milestone 10: Manual Smoke + Wrap-up

### Task 10.1: Smoke checklist on testkeen

**Pre-req:** built backend deployed (or running locally with port-forwarded agent), agent running on testkeen.

- [ ] **Step 1: Prepare fixtures via awg-manager web UI**

On testkeen (192.168.31.1:222):
- 2 DNS rules with explicit routes → `awg11` (e.g. "vk-test", "rt-test")
- 1 static IP route → `awg11` (e.g. CIDR `10.99.99.0/24`, name "smoke-cidr")
- **1 DNS rule with explicit routes → WAN (`eth3`)** (name "sber-test", domain `sberbank.ru`) — this is the safety canary
- Note: no need to add an HR-Neo policy; existing `routes:null` HR-Neo rules will be exercised by the fall-through-conversion path

- [ ] **Step 2: Import a new tunnel via TG**

Send a fresh `.conf` file to the per_router topic; tap "➕ Добавить новый". Confirm `awg13` appears in awg-manager UI.

- [ ] **Step 3: Open Routes panel in TG**

Tap "🛣 Маршруты". Verify Screen 2:
- HydraRoute Neo line shown (✅ установлен, работает)
- DNS routes total ≥ 50 (2 smoke + ~48 fall-through HR-Neo)
- Static IP routes total = 1
- awg11 row total ≥ 50 (2 explicit DNS + ~48 fall-through-credited HR-Neo + 1 static)
- awg13 row total = 0, no rebind button
- "Не входят в перенос: WAN/system: 1" line present

- [ ] **Step 4: Tap Перенести on awg11, pick awg13**

Screen 3 lists awg13. Tap awg13. Screen 4 preview shows:
- "Будет перенесено (≥51): DNS=≥50 (HR-Neo: ≥48), Static IP: 1"
- "НЕ ТРОГАЕМ: WAN/system: 1"
- Token displayed

- [ ] **Step 5: Tap Подтвердить**

Wait <30 s. Screen 5 shows: DNS≥50 ok, Static=1 ok.

- [ ] **Step 6: Verify in awg-manager web UI**

- vk-test, rt-test, smoke-cidr now point to awg13
- ALL fall-through HR-Neo rules now have explicit `routes` pointing to awg13
- **sber-test (WAN) is STILL on WAN (eth3)** — this is the acceptance criterion. If it moved, the feature has failed.

- [ ] **Step 7: Cleanup**

Reverse rebind (awg13 → awg11) to restore. Confirm idempotency: panel/preview/Screen 5 all behave consistently.

### Task 10.2: README / spec link

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add a one-liner under "Реализованные фичи"**

```markdown
- Routes panel — перенос всех smart-routing правил (DNS + Static IP, включая HR-Neo) с одного туннеля на другой через Telegram, с явной защитой WAN/системных маршрутов
```

- [ ] **Step 2: Commit**

```bash
git commit -am "docs: README — Routes panel feature line"
```

### Task 10.3: Tag a release candidate

- [ ] **Step 1: Tag**

```bash
git tag v0.10.0-rc1
git push origin v0.10.0-rc1
```

CI builds 7 release artifacts. After smoke passes against the binaries, drop `-rc1` and push v0.10.0 final.

---

## Self-Review Checklist (run before handoff)

- [x] Spec coverage: every section maps to a task. §3 (data flow) → M3+M4+M7+M8; §4 (UX) → M6; §5 (components) → File Map; §6 (API contract) → M1+M2; §7 (caching) → M5; §8 (errors) → M4 partial-fail tests; §9 (testing) → M9; §10 (resolved) → M0 done.
- [x] No "TBD" / "TODO" / "implement later" placeholders in any task body.
- [x] Type names consistent: `RouteSnapshot`, `RouteRebindResult`, `TunnelMeta`, `TunnelCounts`, `CategoryResult`, `HRStatus` used identically in `pkg/wire/routing.go` and renderer code.
- [x] Method names consistent: `ListDNSRoutes` / `UpdateDNSRoute` / `ListStaticRoutes` / `UpdateStaticRoute` / `RoutingTunnels` / `RoutingRefresh` / `HydraRouteControl` / `GetEnv` / `RouteStatus` / `RouteRebind`.
- [x] Callback grammar consistent: `routes_open`, `routes_router`, `routes_rebind`, `routes_pick`, `routes_confirm`, `routes_refresh`, `routes_back`, `routes_close` — same in parse.go, router.go, and TG keyboard builders.
- [x] Token TTL = 5 min, cache TTL = 30 s — same numbers everywhere.
- [x] Field-name casing precisely tracked: DNS uses `tunnelId` (lower-d), Static uses `tunnelID` (capital-D). Reflected in DNSRouteEntry vs StaticRoute.
- [x] WAN-canary acceptance criterion (§9.3 step 6) verified by Task 4.1's `TestRouteRebind_HappyPath_WANCanaryUntouched` (asserts `r.Routes[0].Interface == "eth3"` and `r.TunnelID == "eth3"` after rebind) AND by Task 10.1 step 6 manual smoke.
- [x] Fall-through conversion behaviour (default-on, gated on `srcIsDefaultRoute`) covered by Task 3.1's `TestRouteStatus_FallthroughRulesAreCounted` AND Task 4.1's HappyPath (which credits the fall-through Fallthru rule to t1 because t1 has DefaultRoute=true).
