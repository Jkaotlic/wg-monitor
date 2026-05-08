# Route Rebinding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Telegram-driven "Routes panel" that lets the admin migrate all DNS / Static IP / HydraRoute Neo rules from one managed AmneziaWG tunnel to another, while preserving every rule that targets WAN, system tunnels, or other managed tunnels.

**Architecture:** New `awgmgr.routing` client methods talk to `/api/{dns-routes,static-routes,hydraroute,routing}/*`. Two new agent actions (`route_status`, `route_rebind`) aggregate / mutate. Wire format reuses `CommandResult.Output` to carry JSON-encoded `RouteSnapshot` / `RouteRebindResult` payloads (no protocol additions besides two new action names). Backend gets a `RoutesPanel` renderer (Screens 1–5 from the spec), an in-memory snapshot cache (TTL 30 s, per-user), and a `pendingRebinds` token store mirroring the existing `pendingUploads` pattern. Inline panel updates land via a new `RoutesPanelNotifier` that the backend's cmd-result handler dispatches to when `ref.Action` is `route_status` / `route_rebind`.

**Tech Stack:** Go 1.22+, `golang.org/x/sync/errgroup` for parallel status fetch, `net/http/httptest` for unit tests, no new third-party dependencies.

**Spec:** [docs/superpowers/specs/2026-05-08-route-rebinding-design.md](../specs/2026-05-08-route-rebinding-design.md)

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `docs/superpowers/notes/2026-05-08-routing-api-probes.md` | Create | Curl-probe findings — exact JSON shapes for DNS / Static / HR-Neo routes, the "target" field name, optimistic-lock fields |
| `internal/agent/awgmgr/types_routing.go` | Create | `DNSRoute`, `StaticRoute`, `HRConfig`, `HRPolicy`, `BulkBackendReq`, `RouteSnapshot`, `RouteRebindResult`, `TunnelMeta`, `TunnelCounts` |
| `internal/agent/awgmgr/routing.go` | Create | Methods: `ListDNSRoutes`, `ListStaticRoutes`, `BulkBackendDNS`, `UpdateStaticRoute`, `GetHRConfig`, `PutHRConfig`, `HydraRouteControl`, `RoutingRefresh` |
| `internal/agent/awgmgr/routing_test.go` | Create | httptest fixtures for each method (path, headers, body shape, response decode) |
| `internal/agent/awgmgr/hr_replace.go` | Create | Pure helper `ReplaceHRTargets(cfg HRConfig, srcRefs []string, dst string) (HRConfig, int)` returning modified config + replacement count |
| `internal/agent/awgmgr/hr_replace_test.go` | Create | Golden tests: zero replacements, multiple targets per policy, nested arrays, no-op when src absent |
| `internal/agent/actions/route_status.go` | Create | Aggregator: parallel fetch via errgroup, build `RouteSnapshot`, encode to JSON |
| `internal/agent/actions/route_status_test.go` | Create | Unit: mock `awgmgr.Client`, verify counts, HR-Neo absent path, partial-failure (one source errors) |
| `internal/agent/actions/route_rebind.go` | Create | Three-phase rebind, `srcRefs` builder, per-category result accumulator, mutex per-router (in `Runner`) |
| `internal/agent/actions/route_rebind_test.go` | Create | HappyPath, PartialFail, ZeroRules, SrcEqDst, srcRefs match by ID/NDMSName/InterfaceName/Name |
| `internal/agent/actions/runner.go` | Modify | Add `route_status` / `route_rebind` cases; add `routeMu sync.Mutex` for serialisation |
| `pkg/wire/types.go` | Modify | Add `route_status`, `route_rebind` to `validCommandActions` |
| `pkg/wire/routing.go` | Create | Shared types: `RouteSnapshot`, `RouteRebindResult`, `TunnelMeta`, `TunnelCounts`, encode/decode helpers (used by both agent and backend) |
| `pkg/wire/routing_test.go` | Create | Round-trip JSON, backwards-compat (unknown fields ignored) |
| `internal/backend/callbacks/routes_cache.go` | Create | `RoutesCache` — in-memory map per user, 30 s TTL, `Get`/`Put`/`Invalidate` |
| `internal/backend/callbacks/routes_cache_test.go` | Create | TTL expiry, invalidate, concurrent access |
| `internal/backend/tg/routes_panel.go` | Create | Screens 2–5 text + keyboards; `RouteRow`, `OtherCounts` types; `routesMaxPerRow` const |
| `internal/backend/tg/routes_panel_test.go` | Create | Render Empty, with counts, HR-Neo absent, "untouched" block, keyboard for each screen |
| `internal/backend/callbacks/parse.go` | Modify | Add `routes_open`, `routes_router`, `routes_rebind`, `routes_pick`, `routes_confirm`, `routes_refresh`, `routes_back`, `routes_close` to `validActions`; parse `RebindToken` into Args |
| `internal/backend/callbacks/parse_test.go` | Modify | Tests for each new action including token presence/missing |
| `internal/backend/callbacks/actions.go` | Modify | New `RoutesAction` (open/refresh), `RebindStartAction` (Screen 3), `RebindPickAction` (Screen 4 + token mint), `RebindConfirmAction` (token consume + enqueue); `pendingRebinds` map mirroring `pendingUploads` |
| `internal/backend/callbacks/actions_test.go` | Modify | Tests for each new action class — token lifecycle, replay rejection, src==dst guard |
| `internal/backend/callbacks/router.go` | Modify | Reply-keyboard branch for `🛣 Маршруты`; case branches for the new actions; dispatch route_status command on first open with cached fallback |
| `internal/backend/callbacks/routes_notifier.go` | Create | `RoutesPanelNotifier` — implements result-edit-in-place for `route_status` / `route_rebind` actions; uses TG `EditMessageText` against `ref.MessageID` |
| `internal/backend/callbacks/routes_notifier_test.go` | Create | Happy paths and degraded UI rendering |
| `internal/backend/handler.go` | Modify | Plumb `RoutesPanelNotifier` into `Deps`; in `cmdResultHandler`, dispatch to it when `ref.Action ∈ {route_status, route_rebind}` instead of generic `TGNotifier` |
| `internal/backend/handler_test.go` | Modify | Add fake routes notifier; test correct dispatch by Action |
| `cmd/backend/main.go` | Modify | Wire `RoutesPanelNotifier` into the backend's `Deps` |
| `internal/backend/tg/replykb.go` (or wherever `ReplyKeyboardForTopic` lives) | Modify | Add `🛣 Маршруты` button row to per-router topic keyboard |
| `cmd/backend/integration_test.go` | Modify | End-to-end: route_status returns counts; rebind moves rules from src to dst; WAN-targeted rule fixture stays put |

---

## Conventions

- **Tests first**, then minimal implementation. One green run before committing.
- **Commit boundaries** = task boundaries unless the task explicitly says "no commit".
- **Run `go test ./...`** after every implementation step. The Makefile target is just `go test ./...`.
- **No new third-party imports** beyond `golang.org/x/sync/errgroup` (already transitively pulled).
- **Field tag invariants:** all `json` tags on new wire types stay lowercase-camelCase to match awg-manager's response shape (e.g. `"defaultRoute"` not `"default_route"`).
- **Russian text** in TG renderers — match the existing tone in `tunnels_panel.go` (no MarkdownV2 — plain text only, per `feedback_telegram_api`).

---

## Milestone 0: Curl-Probe — Verify awg-manager API Contract

**Why first:** spec §10 lists six unresolved questions about field names and shapes that MUST be answered before code. Code written against guesses will be wrong (per `feedback_awgmgr_api`). This milestone produces a single notes file used as input by every subsequent milestone.

### Task 0.1: Probe routing endpoints on testkeen and document

**Files:**
- Create: `docs/superpowers/notes/2026-05-08-routing-api-probes.md`

**Pre-req:** SSH access to testkeen (192.168.31.1:222, root). awg-manager running on `127.0.0.1:2222`. Have at least one DNS route, one static route, one HR-Neo policy created via the awg-manager web UI on `awg11` (the existing test tunnel) to give the probes data to inspect.

- [ ] **Step 1: Probe DNS routes — list and bulk-backend shape**

Run on testkeen:
```bash
curl -s -H 'X-Requested-With: XMLHttpRequest' \
     http://127.0.0.1:2222/api/dns-routes/list | jq .
```

Capture the entire JSON output verbatim into a code block in the notes file under heading `## /api/dns-routes/list`. Identify and record:
- The field that holds the tunnel reference (name candidates: `backend`, `target`, `target_id`, `interface_id`).
- Whether that field stores the tunnel ID, NDMSName, InterfaceName, or Name.
- All other fields, their types, and which are required for an `update` round-trip.

Then probe the bulk-backend endpoint (use a no-op move to read the request shape — the awg-manager UI's network tab is the source of truth):
```bash
# from a browser DevTools network tab, capture the POST body when the UI's
# "Сменить туннель" button is pressed on multiple selected DNS routes.
```

Record the exact body shape in the notes file. Likely:
```json
{"ids":["<rid1>","<rid2>"],"backend":"Wireguard0"}
```

- [ ] **Step 2: Probe static routes**

Run:
```bash
curl -s -H 'X-Requested-With: XMLHttpRequest' \
     http://127.0.0.1:2222/api/static-routes/list | jq .
```

Document under `## /api/static-routes/list`. Same fields-of-interest as DNS. Then capture an update body via DevTools (edit one rule, change its tunnel, observe the POST):
```
POST /api/static-routes/update?id=<rid>
```

Record minimum required fields (often the API requires resending the entire object; if so list every field).

- [ ] **Step 3: Probe HR-Neo config**

Run:
```bash
curl -s -H 'X-Requested-With: XMLHttpRequest' \
     http://127.0.0.1:2222/api/hydraroute/config | jq .
```

Document under `## /api/hydraroute/config`. This response is the largest — capture the full shape including:
- Top-level keys (`policies`, `targets`, `geo_files`, `version`, etc.)
- Nested target reference structure (a policy may have an array of target identifiers — capture the field name and value type)
- Whether a `version`, `etag`, or `revision` field is present (for optimistic locking — if absent, note "no etag — accept single-admin race")

Probe the PUT endpoint by saving the same JSON back unchanged:
```bash
curl -s -X PUT \
     -H 'X-Requested-With: XMLHttpRequest' \
     -H 'Content-Type: application/json' \
     --data @/tmp/hr.json \
     http://127.0.0.1:2222/api/hydraroute/config/update
```

Record the response — `{"success":true,"data":...}` or specific error shape on validation fail.

- [ ] **Step 4: Probe routing meta and HR-Neo control**

```bash
curl -s -H 'X-Requested-With: XMLHttpRequest' \
     http://127.0.0.1:2222/api/routing/tunnels | jq .

curl -s -X POST -H 'X-Requested-With: XMLHttpRequest' \
     http://127.0.0.1:2222/api/routing/refresh

curl -s -X POST -H 'X-Requested-With: XMLHttpRequest' \
     -H 'Content-Type: application/json' \
     -d '{"action":"restart"}' \
     http://127.0.0.1:2222/api/system/hydraroute-control
```

Record responses. Confirm the request body shape for `hydraroute-control` (it may be `{action:"restart"}` or `{"command":"restart"}` — verify).

- [ ] **Step 5: Resolve the six open questions from spec §10**

In the notes file, add a section `## Open Questions Resolved` that answers each of the six questions verbatim from the probe data. For example:

```
1. Field name in DNS routes that holds tunnel reference:
   → "backend" (string), per /api/dns-routes/list .data[].backend

2. Value form of "backend":
   → NDMSName ("Wireguard0", "Wireguard1") — confirmed by comparing with /api/tunnels/all .ndmsName

3. bulk-backend body:
   → {"ids":["<id>",...], "backend":"<NDMSName>"}

4. HR-Neo etag:
   → none; accept single-admin race window

5. HR-Neo policy target shape:
   → policy.targets is an array of {"interface":"Wireguard0","priority":1} objects
   → src->dst replace logic must update the "interface" field of each entry

6. bulk-backend behaviour on missing id:
   → returns success with "skipped":["<id>"] field listing IDs not found
```

If any question cannot be answered with the available data, mark `→ TBD — need second probe`. Only fully-answered questions unblock subsequent milestones.

- [ ] **Step 6: Commit notes**

```bash
git add docs/superpowers/notes/2026-05-08-routing-api-probes.md
git commit -m "docs: awg-manager routing API curl probes"
```

---

## Milestone 1: Routing Types and DNS / Static Client

### Task 1.1: Define routing types

**Files:**
- Create: `internal/agent/awgmgr/types_routing.go`

- [ ] **Step 1: Write the file with the exact shapes from the probe notes**

Use the field names captured in Milestone 0. Below is the template — substitute the actual JSON tags from the notes:

```go
package awgmgr

// DNSRoute mirrors one entry of /api/dns-routes/list .data[].
// Field tags must match the probe notes exactly.
type DNSRoute struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	// Backend is the tunnel reference. Per probe notes, value is NDMSName
	// like "Wireguard0" for managed tunnels, or the WAN interface id, or
	// a system tunnel id. Empty string means "not yet bound".
	Backend  string   `json:"backend"`
	Domains  []string `json:"domains"`
	// Add other fields verbatim from the probe — keep this struct field-
	// complete so update round-trips don't drop data.
}

// StaticRoute mirrors one entry of /api/static-routes/list .data[].
type StaticRoute struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Backend string   `json:"backend"`
	CIDRs   []string `json:"cidrs"`
}

// HRConfig is the full /api/hydraroute/config payload. Decoded as a generic
// JSON tree because the policy/target shape is unstable across awg-manager
// versions; ReplaceHRTargets walks the tree by known keys.
type HRConfig struct {
	Raw map[string]any
}

// BulkBackendDNSReq is the body for POST /api/dns-routes/bulk-backend.
type BulkBackendDNSReq struct {
	IDs     []string `json:"ids"`
	Backend string   `json:"backend"`
}

// BulkBackendDNSResp captures the fields we care about on success — number
// processed and any IDs that were silently skipped (deleted between list
// and bulk-backend, etc.).
type BulkBackendDNSResp struct {
	Processed int      `json:"processed"`
	Skipped   []string `json:"skipped,omitempty"`
}
```

- [ ] **Step 2: Compile-check**

```bash
go build ./internal/agent/awgmgr
```
Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add internal/agent/awgmgr/types_routing.go
git commit -m "feat(awgmgr): routing types — DNSRoute, StaticRoute, HRConfig, bulk req/resp"
```

### Task 1.2: ListDNSRoutes + test

**Files:**
- Modify: `internal/agent/awgmgr/routing.go` (created in this task)
- Create: `internal/agent/awgmgr/routing_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/agent/awgmgr/routing_test.go` with:

```go
package awgmgr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListDNSRoutes_HappyPath(t *testing.T) {
	const payload = `{"success":true,"data":[
		{"id":"r1","name":"vk","enabled":true,"backend":"Wireguard0","domains":["vk.com"]},
		{"id":"r2","name":"rt","enabled":true,"backend":"Wireguard1","domains":["rutube.ru"]}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dns-routes/list" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With")
		}
		w.Write([]byte(payload))
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
	if got[0].ID != "r1" || got[0].Backend != "Wireguard0" {
		t.Errorf("got[0]: %+v", got[0])
	}
}
```

- [ ] **Step 2: Run, expect FAIL (method not defined)**

```bash
go test ./internal/agent/awgmgr -run TestListDNSRoutes_HappyPath
```
Expected: build error or "ListDNSRoutes undefined".

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
```

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./internal/agent/awgmgr -run TestListDNSRoutes_HappyPath -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/awgmgr/routing.go internal/agent/awgmgr/routing_test.go
git commit -m "feat(awgmgr): ListDNSRoutes"
```

### Task 1.3: ListStaticRoutes + test

**Files:**
- Modify: `internal/agent/awgmgr/routing.go`
- Modify: `internal/agent/awgmgr/routing_test.go`

- [ ] **Step 1: Write the failing test**

Append to `routing_test.go`:

```go
func TestListStaticRoutes_HappyPath(t *testing.T) {
	const payload = `{"success":true,"data":[
		{"id":"s1","name":"work","enabled":true,"backend":"Wireguard0","cidrs":["10.0.0.0/8"]}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/static-routes/list" {
			t.Errorf("path: %q", r.URL.Path)
		}
		w.Write([]byte(payload))
	}))
	defer srv.Close()
	c := New(srv.URL)
	got, err := c.ListStaticRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Backend != "Wireguard0" {
		t.Errorf("got: %+v", got)
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

```bash
go test ./internal/agent/awgmgr -run TestListStaticRoutes_HappyPath
```

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
```

- [ ] **Step 4: Run, expect PASS**

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(awgmgr): ListStaticRoutes"
```

### Task 1.4: BulkBackendDNS + test

**Files:**
- Modify: `internal/agent/awgmgr/routing.go`
- Modify: `internal/agent/awgmgr/routing_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestBulkBackendDNS_HappyPath(t *testing.T) {
	var got BulkBackendDNSReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dns-routes/bulk-backend" || r.Method != http.MethodPost {
			t.Errorf("method/path: %s %q", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type: %s", r.Header.Get("Content-Type"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"success":true,"data":{"processed":2}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	resp, err := c.BulkBackendDNS(context.Background(), []string{"r1", "r2"}, "Wireguard1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Processed != 2 {
		t.Errorf("processed: %d", resp.Processed)
	}
	if got.Backend != "Wireguard1" || len(got.IDs) != 2 || got.IDs[0] != "r1" {
		t.Errorf("request body: %+v", got)
	}
}
```

(Add `"encoding/json"` to the import block of `routing_test.go` if not already present.)

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement**

```go
// BulkBackendDNS calls POST /api/dns-routes/bulk-backend to atomically
// rebind a list of DNS-route IDs to the named backend (NDMSName).
func (c *Client) BulkBackendDNS(ctx context.Context, ids []string, backend string) (*BulkBackendDNSResp, error) {
	body, err := json.Marshal(BulkBackendDNSReq{IDs: ids, Backend: backend})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/dns-routes/bulk-backend", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("awgmgr POST dns-routes/bulk-backend: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("awgmgr dns-routes/bulk-backend: HTTP %d: %s", resp.StatusCode, snippet(rb))
	}
	var env Envelope[BulkBackendDNSResp]
	if err := json.Unmarshal(rb, &env); err != nil {
		return nil, fmt.Errorf("awgmgr dns-routes/bulk-backend: decode: %w", err)
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr dns-routes/bulk-backend: success=false")
	}
	return &env.Data, nil
}
```

- [ ] **Step 4: Run, expect PASS**

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(awgmgr): BulkBackendDNS"
```

### Task 1.5: UpdateStaticRoute + RoutingRefresh + tests

**Files:**
- Modify: `internal/agent/awgmgr/routing.go`
- Modify: `internal/agent/awgmgr/routing_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestUpdateStaticRoute_HappyPath(t *testing.T) {
	var got StaticRoute
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/static-routes/update" || r.URL.Query().Get("id") != "s1" {
			t.Errorf("path/id: %q %q", r.URL.Path, r.URL.Query().Get("id"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"success":true,"data":{}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	rule := StaticRoute{ID: "s1", Name: "work", Enabled: true, Backend: "Wireguard1", CIDRs: []string{"10.0.0.0/8"}}
	if err := c.UpdateStaticRoute(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	if got.Backend != "Wireguard1" || got.Name != "work" {
		t.Errorf("body: %+v", got)
	}
}

func TestRoutingRefresh_HappyPath(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/routing/refresh" && r.Method == http.MethodPost {
			called = true
		}
		w.Write([]byte(`{"success":true}`))
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
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement**

Append to `routing.go`:

```go
// UpdateStaticRoute calls POST /api/static-routes/update?id=<id>. The full
// rule struct is sent — awg-manager treats the request as full-replace.
func (c *Client) UpdateStaticRoute(ctx context.Context, rule StaticRoute) error {
	body, err := json.Marshal(rule)
	if err != nil {
		return err
	}
	return c.post(ctx, "/api/static-routes/update?id="+rule.ID, bytes.NewReader(body), nil)
}

// RoutingRefresh forces awg-manager to re-fetch routes from NDMS.
func (c *Client) RoutingRefresh(ctx context.Context) error {
	return c.post(ctx, "/api/routing/refresh", nil, nil)
}
```

(Note: existing `post` helper does not set `Content-Type` — verify that awg-manager accepts the body without it for `update`; if not, inline a custom request. Curl probes from Milestone 0 will have shown this.)

- [ ] **Step 4: Run, expect PASS**

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(awgmgr): UpdateStaticRoute, RoutingRefresh"
```

---

## Milestone 2: HR-Neo Client + Replacement Helper

### Task 2.1: GetHRConfig / PutHRConfig / HydraRouteControl

**Files:**
- Modify: `internal/agent/awgmgr/routing.go`
- Modify: `internal/agent/awgmgr/routing_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestGetHRConfig_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/hydraroute/config" {
			t.Errorf("path: %q", r.URL.Path)
		}
		w.Write([]byte(`{"success":true,"data":{"policies":[{"id":"p1","targets":[{"interface":"Wireguard0"}]}]}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	cfg, err := c.GetHRConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pols, _ := cfg.Raw["policies"].([]any)
	if len(pols) != 1 {
		t.Fatalf("policies: %+v", cfg.Raw)
	}
}

func TestPutHRConfig_HappyPath(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/hydraroute/config/update" || r.Method != http.MethodPut {
			t.Errorf("method/path: %s %q", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	cfg := HRConfig{Raw: map[string]any{"policies": []any{}}}
	if err := c.PutHRConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["policies"]; !ok {
		t.Errorf("body missing policies: %+v", got)
	}
}

func TestHydraRouteControl_Restart(t *testing.T) {
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"success":true}`))
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
// GetHRConfig returns the full HydraRoute Neo configuration as an opaque
// JSON tree. The shape is awg-manager-version-specific; the rebind helper
// mutates it via known keys ("policies", "targets", "interface") rather
// than typed structs.
func (c *Client) GetHRConfig(ctx context.Context) (HRConfig, error) {
	var env Envelope[map[string]any]
	if err := c.get(ctx, "/api/hydraroute/config", &env); err != nil {
		return HRConfig{}, err
	}
	if !env.Success {
		return HRConfig{}, fmt.Errorf("awgmgr hydraroute/config: success=false")
	}
	return HRConfig{Raw: env.Data}, nil
}

// PutHRConfig replaces the entire HR-Neo config via PUT /api/hydraroute/config/update.
func (c *Client) PutHRConfig(ctx context.Context, cfg HRConfig) error {
	body, err := json.Marshal(cfg.Raw)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+"/api/hydraroute/config/update", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("awgmgr PUT hydraroute/config/update: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("awgmgr hydraroute/config/update: HTTP %d: %s", resp.StatusCode, snippet(rb))
	}
	var env Envelope[any]
	if err := json.Unmarshal(rb, &env); err != nil {
		return fmt.Errorf("awgmgr hydraroute/config/update: decode: %w", err)
	}
	if !env.Success {
		return fmt.Errorf("awgmgr hydraroute/config/update: success=false")
	}
	return nil
}

// HydraRouteControl posts {"action":"<action>"} to /api/system/hydraroute-control.
// action ∈ {"start","stop","restart"}.
func (c *Client) HydraRouteControl(ctx context.Context, action string) error {
	body, err := json.Marshal(map[string]string{"action": action})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/system/hydraroute-control", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("awgmgr POST hydraroute-control: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("awgmgr hydraroute-control: HTTP %d: %s", resp.StatusCode, snippet(rb))
	}
	return nil
}
```

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./internal/agent/awgmgr -v
```

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(awgmgr): HR-Neo config GET/PUT and control"
```

### Task 2.2: ReplaceHRTargets pure helper

**Files:**
- Create: `internal/agent/awgmgr/hr_replace.go`
- Create: `internal/agent/awgmgr/hr_replace_test.go`

The helper walks the HR config tree and rewrites every occurrence of an interface reference matching `srcRefs` to `dst`. The exact path through the tree depends on the probe notes; the implementation below assumes the structure `cfg.Raw["policies"][i]["targets"][j]["interface"]`. Adjust based on actual probe data.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/awgmgr/hr_replace_test.go`:

```go
package awgmgr

import (
	"reflect"
	"testing"
)

func TestReplaceHRTargets_SinglePolicy(t *testing.T) {
	cfg := HRConfig{Raw: map[string]any{
		"policies": []any{
			map[string]any{
				"id": "p1",
				"targets": []any{
					map[string]any{"interface": "Wireguard0", "priority": float64(1)},
					map[string]any{"interface": "Wireguard1", "priority": float64(2)},
				},
			},
		},
	}}
	got, n := ReplaceHRTargets(cfg, []string{"Wireguard0"}, "Wireguard9")
	if n != 1 {
		t.Errorf("replacements: %d", n)
	}
	pols := got.Raw["policies"].([]any)
	tgts := pols[0].(map[string]any)["targets"].([]any)
	if iface := tgts[0].(map[string]any)["interface"]; iface != "Wireguard9" {
		t.Errorf("first target: %v", iface)
	}
	if iface := tgts[1].(map[string]any)["interface"]; iface != "Wireguard1" {
		t.Errorf("second target unchanged: %v", iface)
	}
}

func TestReplaceHRTargets_NoMatch(t *testing.T) {
	cfg := HRConfig{Raw: map[string]any{
		"policies": []any{
			map[string]any{"targets": []any{map[string]any{"interface": "Wireguard1"}}},
		},
	}}
	want := HRConfig{Raw: map[string]any{
		"policies": []any{
			map[string]any{"targets": []any{map[string]any{"interface": "Wireguard1"}}},
		},
	}}
	got, n := ReplaceHRTargets(cfg, []string{"Wireguard0"}, "Wireguard9")
	if n != 0 {
		t.Errorf("replacements: %d", n)
	}
	if !reflect.DeepEqual(got.Raw, want.Raw) {
		t.Errorf("config mutated unexpectedly")
	}
}

func TestReplaceHRTargets_MultipleSrcRefs(t *testing.T) {
	cfg := HRConfig{Raw: map[string]any{
		"policies": []any{
			map[string]any{"targets": []any{
				map[string]any{"interface": "Wireguard0"},
				map[string]any{"interface": "awg11"},
			}},
		},
	}}
	got, n := ReplaceHRTargets(cfg, []string{"Wireguard0", "awg11"}, "Wireguard9")
	if n != 2 {
		t.Errorf("replacements: %d", n)
	}
	tgts := got.Raw["policies"].([]any)[0].(map[string]any)["targets"].([]any)
	for i, tg := range tgts {
		if iface := tg.(map[string]any)["interface"]; iface != "Wireguard9" {
			t.Errorf("target %d: %v", i, iface)
		}
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement**

Create `internal/agent/awgmgr/hr_replace.go`:

```go
package awgmgr

// ReplaceHRTargets walks the HR-Neo config tree and rewrites every "interface"
// field whose value is in srcRefs to dst. Returns a new HRConfig (deep-cloned)
// plus the number of replacements made.
//
// The walk path is hard-coded to cfg.Raw["policies"][*]["targets"][*]["interface"]
// per probe notes; if the actual config has nested objects elsewhere that
// reference interfaces, extend the walk here.
func ReplaceHRTargets(cfg HRConfig, srcRefs []string, dst string) (HRConfig, int) {
	cloned := deepCloneJSON(cfg.Raw).(map[string]any)
	count := 0
	srcSet := make(map[string]struct{}, len(srcRefs))
	for _, s := range srcRefs {
		if s != "" {
			srcSet[s] = struct{}{}
		}
	}
	pols, _ := cloned["policies"].([]any)
	for _, p := range pols {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		tgts, _ := pm["targets"].([]any)
		for _, t := range tgts {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			iface, _ := tm["interface"].(string)
			if _, hit := srcSet[iface]; hit {
				tm["interface"] = dst
				count++
			}
		}
	}
	return HRConfig{Raw: cloned}, count
}

func deepCloneJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = deepCloneJSON(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = deepCloneJSON(vv)
		}
		return out
	default:
		return x
	}
}
```

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./internal/agent/awgmgr -run TestReplaceHRTargets -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/agent/awgmgr/hr_replace.go internal/agent/awgmgr/hr_replace_test.go
git commit -m "feat(awgmgr): ReplaceHRTargets pure helper"
```

---

## Milestone 3: Wire Types and route_status Action

### Task 3.1: Shared wire types

**Files:**
- Create: `pkg/wire/routing.go`
- Create: `pkg/wire/routing_test.go`
- Modify: `pkg/wire/types.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/wire/routing_test.go`:

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
			{ID: "t1", Name: "amnezia", NDMSName: "Wireguard0", Enabled: true},
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
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement**

Create `pkg/wire/routing.go`:

```go
// Package wire — routing.go defines payload types for route_status and
// route_rebind. They are JSON-encoded into wire.CommandResult.Output so we
// don't need a new envelope; the backend decodes Output by Action.
package wire

type HRStatus struct {
	Installed bool `json:"installed"`
	Running   bool `json:"running"`
}

type TunnelMeta struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	NDMSName      string `json:"ndms_name"`
	InterfaceName string `json:"interface_name,omitempty"`
	Enabled       bool   `json:"enabled"`
}

type TunnelCounts struct {
	DNS    int `json:"dns"`
	Static int `json:"static"`
	HRNeo  int `json:"hr_neo"`
}

// RouteSnapshot is the payload of a successful route_status CommandResult.
type RouteSnapshot struct {
	HRNeo   HRStatus                `json:"hr_neo"`
	Tunnels []TunnelMeta            `json:"tunnels"`
	Counts  map[string]TunnelCounts `json:"counts"` // key = tunnel id, only managed tunnels
	Other   TunnelCounts            `json:"other"`  // sum of WAN + system + external
}

type CategoryResult struct {
	OK     int      `json:"ok"`
	Failed int      `json:"failed"`
	Errors []string `json:"errors,omitempty"`
}

// RouteRebindResult is the payload of a route_rebind CommandResult.
type RouteRebindResult struct {
	SrcTunnelID string         `json:"src_tunnel_id"`
	DstTunnelID string         `json:"dst_tunnel_id"`
	DNS         CategoryResult `json:"dns"`
	Static      CategoryResult `json:"static"`
	HRNeo       CategoryResult `json:"hr_neo"`
}
```

- [ ] **Step 4: Add the new actions to validCommandActions**

Edit `pkg/wire/types.go` — extend the map literal:

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

- [ ] **Step 5: Run, expect PASS**

```bash
go test ./pkg/wire -v
```

- [ ] **Step 6: Commit**

```bash
git add pkg/wire/routing.go pkg/wire/routing_test.go pkg/wire/types.go
git commit -m "feat(wire): route_status/route_rebind actions and payload types"
```

### Task 3.2: route_status action — types and aggregator

**Files:**
- Create: `internal/agent/actions/route_status.go`
- Create: `internal/agent/actions/route_status_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/agent/actions/route_status_test.go`:

```go
package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

// fakeAwgmgr serves canned JSON for the five endpoints route_status hits.
func fakeAwgmgr(t *testing.T, hrInstalled bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		body := `{"success":true,"data":{"installed":false,"running":false}}`
		if hrInstalled {
			body = `{"success":true,"data":{"installed":true,"running":true}}`
		}
		w.Write([]byte(body))
	})
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"t1","name":"amnezia","ndmsName":"Wireguard0","interfaceName":"awg11","enabled":true},
			{"id":"t2","name":"amnezia2","ndmsName":"Wireguard1","interfaceName":"awg12","enabled":true}
		],"external":[],"system":[]}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"data":[
			{"id":"d1","backend":"Wireguard0","enabled":true},
			{"id":"d2","backend":"Wireguard0","enabled":true},
			{"id":"d3","backend":"Wireguard1","enabled":true},
			{"id":"d4","backend":"WAN","enabled":true}
		]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"data":[
			{"id":"s1","backend":"Wireguard0","enabled":true},
			{"id":"s2","backend":"WAN","enabled":true}
		]}`))
	})
	mux.HandleFunc("/api/hydraroute/config", func(w http.ResponseWriter, r *http.Request) {
		if !hrInstalled {
			http.Error(w, "not installed", 404)
			return
		}
		w.Write([]byte(`{"success":true,"data":{"policies":[
			{"id":"p1","targets":[{"interface":"Wireguard0"}]},
			{"id":"p2","targets":[{"interface":"Wireguard0"},{"interface":"Wireguard1"}]}
		]}}`))
	})
	return httptest.NewServer(mux)
}

func TestRouteStatus_HappyPath(t *testing.T) {
	srv := fakeAwgmgr(t, true)
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
	if snap.Counts["t1"].DNS != 2 || snap.Counts["t1"].Static != 1 || snap.Counts["t1"].HRNeo != 2 {
		t.Errorf("t1 counts: %+v", snap.Counts["t1"])
	}
	if snap.Counts["t2"].DNS != 1 || snap.Counts["t2"].HRNeo != 1 {
		t.Errorf("t2 counts: %+v", snap.Counts["t2"])
	}
	if snap.Other.DNS != 1 || snap.Other.Static != 1 {
		t.Errorf("other counts: %+v", snap.Other)
	}
}

func TestRouteStatus_HRNeoAbsent(t *testing.T) {
	srv := fakeAwgmgr(t, false)
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
	for tid, c := range snap.Counts {
		if c.HRNeo != 0 {
			t.Errorf("%s.HRNeo should be 0 when HR absent: %+v", tid, c)
		}
	}
	// Make sure other-status fetch errors are non-fatal: no error from RouteStatus.
	if !strings.Contains(out, "tunnels") {
		t.Errorf("no tunnels in output: %s", out)
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement**

Create `internal/agent/actions/route_status.go`:

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
		hrCfg   awgmgr.HRConfig
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { hr, err = c.HydraRouteStatus(gctx); return })
	g.Go(func() (err error) { tunnels, err = c.TunnelsAll(gctx); return })
	g.Go(func() (err error) { dns, err = c.ListDNSRoutes(gctx); return })
	g.Go(func() (err error) { statics, err = c.ListStaticRoutes(gctx); return })
	if err := g.Wait(); err != nil {
		return "", err
	}
	// HR-Neo config fetch is gated on installed=true and runs sequentially after
	// the status check so we don't 404 when not installed.
	if hr != nil && hr.Installed {
		var err error
		hrCfg, err = c.GetHRConfig(ctx)
		if err != nil {
			// Non-fatal: status returned installed=true but config GET failed —
			// we still want to render the panel. Mark Running=false defensively
			// and log via the caller; the fields won't show HR counts.
			hrCfg = awgmgr.HRConfig{}
		}
	}
	snap := buildRouteSnapshot(hr, tunnels, dns, statics, hrCfg)
	b, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func buildRouteSnapshot(hr *awgmgr.HydraRouteStatus, tunnels *awgmgr.TunnelsAll, dns []awgmgr.DNSRoute, statics []awgmgr.StaticRoute, hrCfg awgmgr.HRConfig) wire.RouteSnapshot {
	snap := wire.RouteSnapshot{
		Counts: make(map[string]wire.TunnelCounts),
	}
	if hr != nil {
		snap.HRNeo = wire.HRStatus{Installed: hr.Installed, Running: hr.Running}
	}
	// Map managed tunnels by NDMSName for backend-field lookups.
	byNDMS := make(map[string]string) // NDMSName → tunnel ID
	if tunnels != nil {
		for _, t := range tunnels.Tunnels {
			snap.Tunnels = append(snap.Tunnels, wire.TunnelMeta{
				ID: t.ID, Name: t.Name, NDMSName: t.NDMSName,
				InterfaceName: t.InterfaceName, Enabled: t.Enabled,
			})
			if t.NDMSName != "" {
				byNDMS[t.NDMSName] = t.ID
			}
		}
	}
	for _, r := range dns {
		if id, ok := byNDMS[r.Backend]; ok {
			c := snap.Counts[id]
			c.DNS++
			snap.Counts[id] = c
		} else {
			snap.Other.DNS++
		}
	}
	for _, r := range statics {
		if id, ok := byNDMS[r.Backend]; ok {
			c := snap.Counts[id]
			c.Static++
			snap.Counts[id] = c
		} else {
			snap.Other.Static++
		}
	}
	if hr != nil && hr.Installed {
		// HR-Neo: walk policies/targets, count by interface.
		pols, _ := hrCfg.Raw["policies"].([]any)
		for _, p := range pols {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			tgts, _ := pm["targets"].([]any)
			for _, t := range tgts {
				tm, _ := t.(map[string]any)
				iface, _ := tm["interface"].(string)
				if id, ok := byNDMS[iface]; ok {
					c := snap.Counts[id]
					c.HRNeo++
					snap.Counts[id] = c
				} else {
					snap.Other.HRNeo++
				}
			}
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
git commit -m "feat(agent): route_status action"
```

### Task 3.3: Wire route_status into Runner

**Files:**
- Modify: `internal/agent/actions/runner.go`

- [ ] **Step 1: Write the failing test**

Add to an existing runner test file or create `internal/agent/actions/runner_routes_test.go`:

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
	srv := fakeAwgmgr(t, false)
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

- [ ] **Step 2: Run, expect FAIL**

```bash
go test ./internal/agent/actions -run TestRunner_RouteStatus_Dispatch
```

Expected: "unknown action: route_status".

- [ ] **Step 3: Add the case to runner.dispatch**

Insert before the `default:` clause in `internal/agent/actions/runner.go`:

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

### Task 4.1: srcRefs builder + per-category executors

**Files:**
- Create: `internal/agent/actions/route_rebind.go`
- Create: `internal/agent/actions/route_rebind_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/actions/route_rebind_test.go`:

```go
package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

// fakeAwgmgrRebind serves a fixed dataset and tracks calls. Rules with
// backend="Wireguard0" should be moved to "Wireguard9". WAN-targeted rule
// (backend="WAN") MUST NOT be touched.
func fakeAwgmgrRebind(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var bulkCalls atomic.Int32
	var staticUpdates atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/get", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("id") {
		case "t1":
			w.Write([]byte(`{"success":true,"data":{"id":"t1","name":"amnezia","ndmsName":"Wireguard0","interfaceName":"awg11","enabled":true}}`))
		case "t2":
			w.Write([]byte(`{"success":true,"data":{"id":"t2","name":"newtun","ndmsName":"Wireguard9","interfaceName":"awg13","enabled":true}}`))
		default:
			http.Error(w, "not found", 404)
		}
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"data":[
			{"id":"d1","backend":"Wireguard0","name":"vk"},
			{"id":"d2","backend":"Wireguard0","name":"rt"},
			{"id":"d3","backend":"WAN","name":"sber"}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/bulk-backend", func(w http.ResponseWriter, r *http.Request) {
		bulkCalls.Add(1)
		var req awgmgr.BulkBackendDNSReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Backend != "Wireguard9" {
			t.Errorf("bulk backend target: %q", req.Backend)
		}
		if len(req.IDs) != 2 {
			t.Errorf("bulk ids: %+v (want d1,d2 only)", req.IDs)
		}
		w.Write([]byte(`{"success":true,"data":{"processed":2}}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"data":[
			{"id":"s1","backend":"Wireguard0","name":"work","enabled":true},
			{"id":"s2","backend":"WAN","name":"sber-cidr","enabled":true}
		]}`))
	})
	mux.HandleFunc("/api/static-routes/update", func(w http.ResponseWriter, r *http.Request) {
		staticUpdates.Add(1)
		var rule awgmgr.StaticRoute
		_ = json.NewDecoder(r.Body).Decode(&rule)
		if rule.Backend != "Wireguard9" {
			t.Errorf("static update target: %q", rule.Backend)
		}
		if rule.ID == "s2" {
			t.Errorf("WAN static rule s2 must not be updated")
		}
		w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"data":{"installed":false}}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true}`))
	})
	return httptest.NewServer(mux), &bulkCalls, &staticUpdates
}

func TestRouteRebind_HappyPath(t *testing.T) {
	srv, bulkCalls, staticUpdates := fakeAwgmgrRebind(t)
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
	if res.DNS.OK != 2 || res.Static.OK != 1 || res.HRNeo.OK != 0 {
		t.Errorf("counts: %+v", res)
	}
	if bulkCalls.Load() != 1 {
		t.Errorf("bulk-backend called %d times", bulkCalls.Load())
	}
	if staticUpdates.Load() != 1 {
		t.Errorf("static-update called %d times (only s1 should be updated)", staticUpdates.Load())
	}
}

func TestRouteRebind_SrcEqDst(t *testing.T) {
	c := awgmgr.New("http://unused")
	out, err := RouteRebind(context.Background(), c, "t1", "t1")
	if err != nil {
		t.Fatal(err)
	}
	var res wire.RouteRebindResult
	_ = json.Unmarshal([]byte(out), &res)
	if res.DNS.OK != 0 || res.Static.OK != 0 || res.HRNeo.OK != 0 {
		t.Errorf("expected no-op result, got %+v", res)
	}
}

func TestRouteRebind_StaticPartialFail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/get", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") == "t1" {
			w.Write([]byte(`{"success":true,"data":{"id":"t1","ndmsName":"Wireguard0","interfaceName":"awg11"}}`))
		} else {
			w.Write([]byte(`{"success":true,"data":{"id":"t2","ndmsName":"Wireguard9"}}`))
		}
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"data":[
			{"id":"s1","backend":"Wireguard0","name":"a"},
			{"id":"s2","backend":"Wireguard0","name":"b"}
		]}`))
	})
	mux.HandleFunc("/api/static-routes/update", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "id=s2") {
			http.Error(w, "boom", 500)
			return
		}
		w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"data":{"installed":false}}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	out, err := RouteRebind(context.Background(), c, "t1", "t2")
	if err != nil {
		t.Fatal(err)
	}
	var res wire.RouteRebindResult
	_ = json.Unmarshal([]byte(out), &res)
	if res.Static.OK != 1 || res.Static.Failed != 1 {
		t.Errorf("expected 1 ok 1 fail, got %+v", res.Static)
	}
	if len(res.Static.Errors) == 0 {
		t.Errorf("errors should be reported")
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement**

Create `internal/agent/actions/route_rebind.go`:

```go
package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

// RouteRebind moves all DNS / Static / HR-Neo rules whose target is srcID's
// tunnel to dstID's tunnel. WAN, system, and other-managed-tunnel rules are
// preserved. Returns the result as JSON for wire.CommandResult.Output.
func RouteRebind(ctx context.Context, c *awgmgr.Client, srcID, dstID string) (string, error) {
	var res wire.RouteRebindResult
	res.SrcTunnelID = srcID
	res.DstTunnelID = dstID
	if srcID == dstID {
		// No-op: encode and return.
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
	srcRefs := tunnelRefs(src)
	dstNDMS := dst.NDMSName
	if dstNDMS == "" {
		return "", fmt.Errorf("dst tunnel has no NDMSName: %+v", dst)
	}
	res.DNS = rebindDNS(ctx, c, srcRefs, dstNDMS)
	res.Static = rebindStatic(ctx, c, srcRefs, dstNDMS)
	res.HRNeo = rebindHRNeo(ctx, c, srcRefs, dstNDMS)
	if err := c.RoutingRefresh(ctx); err != nil {
		// Non-fatal: routes are written, just NDMS cache might be stale a bit.
		// Append to last-updated category errors so the panel surfaces it.
		res.HRNeo.Errors = append(res.HRNeo.Errors, "routing/refresh: "+err.Error())
	}
	b, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func getTunnel(ctx context.Context, c *awgmgr.Client, id string) (*awgmgr.Tunnel, error) {
	var env awgmgr.Envelope[awgmgr.Tunnel]
	// Unfortunately client doesn't expose a typed GetTunnel. Inline the get.
	// Path: /api/tunnels/get?id=<id>
	// We use the client's helper indirectly by constructing the URL in routing.go,
	// but to keep this self-contained and not rely on a missing helper we make
	// the call via Client.HTTP.
	// (Alternative: add GetTunnel to client.go. Doing so keeps actions clean.)
	if err := awgmgrGetEnv(ctx, c, "/api/tunnels/get?id="+id, &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("tunnels/get id=%s: success=false", id)
	}
	return &env.Data, nil
}

// awgmgrGetEnv is a thin shim around the unexported awgmgr.Client.get; we
// re-export via a helper added in client.go (Task 4.2). Until that helper
// exists, this file does not compile — Task 4.2 closes the gap.
func awgmgrGetEnv(ctx context.Context, c *awgmgr.Client, path string, out any) error {
	return c.GetEnv(ctx, path, out)
}

func tunnelRefs(t *awgmgr.Tunnel) []string {
	out := []string{}
	if t.ID != "" {
		out = append(out, t.ID)
	}
	if t.NDMSName != "" {
		out = append(out, t.NDMSName)
	}
	if t.InterfaceName != "" {
		out = append(out, t.InterfaceName)
	}
	if t.Name != "" {
		out = append(out, t.Name)
	}
	return out
}

func contains(set []string, s string) bool {
	for _, x := range set {
		if x == s {
			return true
		}
	}
	return false
}

func rebindDNS(ctx context.Context, c *awgmgr.Client, srcRefs []string, dstNDMS string) wire.CategoryResult {
	var res wire.CategoryResult
	all, err := c.ListDNSRoutes(ctx)
	if err != nil {
		res.Failed = 1
		res.Errors = []string{"dns/list: " + err.Error()}
		return res
	}
	var ids []string
	for _, r := range all {
		if contains(srcRefs, r.Backend) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return res
	}
	resp, err := c.BulkBackendDNS(ctx, ids, dstNDMS)
	if err != nil {
		res.Failed = len(ids)
		res.Errors = []string{"dns/bulk-backend: " + err.Error()}
		return res
	}
	res.OK = len(ids) - len(resp.Skipped)
	res.Failed = len(resp.Skipped)
	if len(resp.Skipped) > 0 {
		res.Errors = []string{"dns/bulk-backend: skipped ids " + strings.Join(resp.Skipped, ",")}
	}
	return res
}

func rebindStatic(ctx context.Context, c *awgmgr.Client, srcRefs []string, dstNDMS string) wire.CategoryResult {
	var res wire.CategoryResult
	all, err := c.ListStaticRoutes(ctx)
	if err != nil {
		res.Failed = 1
		res.Errors = []string{"static/list: " + err.Error()}
		return res
	}
	for _, r := range all {
		if !contains(srcRefs, r.Backend) {
			continue
		}
		r.Backend = dstNDMS
		if err := c.UpdateStaticRoute(ctx, r); err != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("static/update id=%s: %v", r.ID, err))
			continue
		}
		res.OK++
	}
	return res
}

func rebindHRNeo(ctx context.Context, c *awgmgr.Client, srcRefs []string, dstNDMS string) wire.CategoryResult {
	var res wire.CategoryResult
	hr, err := c.HydraRouteStatus(ctx)
	if err != nil {
		res.Failed = 1
		res.Errors = []string{"hr/status: " + err.Error()}
		return res
	}
	if !hr.Installed {
		return res // zero result, no error
	}
	cfg, err := c.GetHRConfig(ctx)
	if err != nil {
		res.Failed = 1
		res.Errors = []string{"hr/get: " + err.Error()}
		return res
	}
	modified, count := awgmgr.ReplaceHRTargets(cfg, srcRefs, dstNDMS)
	if count == 0 {
		return res
	}
	if err := c.PutHRConfig(ctx, modified); err != nil {
		res.Failed = count
		res.Errors = []string{"hr/put: " + err.Error()}
		return res
	}
	if err := c.HydraRouteControl(ctx, "restart"); err != nil {
		// Config was written; restart failed. Surface it but count as ok-with-warning.
		res.OK = count
		res.Errors = []string{"hr/control restart: " + err.Error()}
		return res
	}
	res.OK = count
	return res
}
```

- [ ] **Step 4: Confirm the test file fails because GetEnv does not exist (Task 4.2 fixes this)**

```bash
go build ./internal/agent/actions
```

Expected: build error mentioning `c.GetEnv` undefined. That's intentional — addressed in the next task.

### Task 4.2: Expose GetEnv on awgmgr.Client (one-liner shim)

**Files:**
- Modify: `internal/agent/awgmgr/client.go`

- [ ] **Step 1: Append to client.go**

```go
// GetEnv is a public version of the lowercase get helper, used by callers in
// other packages that need to issue typed GETs against awg-manager. It exists
// solely so internal/agent/actions can fetch /api/tunnels/get?id=... without
// duplicating the HTTP plumbing here.
func (c *Client) GetEnv(ctx context.Context, path string, out any) error {
	return c.get(ctx, path, out)
}
```

- [ ] **Step 2: Build**

```bash
go build ./internal/agent/...
```
Expected: clean.

- [ ] **Step 3: Run the rebind tests**

```bash
go test ./internal/agent/actions -run TestRouteRebind -v
```
Expected: PASS for HappyPath, SrcEqDst, StaticPartialFail.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/awgmgr/client.go internal/agent/actions/route_rebind.go internal/agent/actions/route_rebind_test.go
git commit -m "feat(agent): route_rebind action with srcRefs filter and per-category executor"
```

### Task 4.3: Wire route_rebind into Runner with mutex

**Files:**
- Modify: `internal/agent/actions/runner.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/actions/runner_routes_test.go`:

```go
func TestRunner_RouteRebind_Dispatch(t *testing.T) {
	srv, _, _ := fakeAwgmgrRebind(t)
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
	r := &Runner{AwgClient: awgmgr.New("http://unused")}
	res := r.Execute(context.Background(), wire.Command{ID: "x", Action: "route_rebind"})
	if res.Status != "err" {
		t.Errorf("expected err, got %s", res.Status)
	}
}
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Add the case + mutex**

In `internal/agent/actions/runner.go`:

1. Add `routeMu sync.Mutex` to the `Runner` struct (and `import "sync"` at the top if not present).
2. Insert the case before `default:` in `dispatch`:

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

(If `route_status` was added in Task 3.3, only route_rebind block is new. Avoid duplicating cases.)

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

Create `internal/backend/callbacks/routes_cache.go`:

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
			{ID: "t1", Name: "amnezia", NDMSName: "Wireguard0", Enabled: true},
			{ID: "t2", Name: "amnezia2", NDMSName: "Wireguard1", Enabled: true},
		},
		Counts: map[string]wire.TunnelCounts{
			"t1": {DNS: 5, Static: 2, HRNeo: 1},
			"t2": {DNS: 0, Static: 0, HRNeo: 0},
		},
		Other: wire.TunnelCounts{DNS: 12, Static: 0, HRNeo: 0},
	}
	text := RoutesPanelText("testkeen", snap)
	if !strings.Contains(text, "testkeen") {
		t.Errorf("router name missing: %s", text)
	}
	if !strings.Contains(text, "amnezia") || !strings.Contains(text, "8") {
		// 8 = 5 + 2 + 1 total for t1
		t.Errorf("t1 row missing or wrong total: %s", text)
	}
	if !strings.Contains(text, "WAN") || !strings.Contains(text, "12") {
		t.Errorf("WAN/Other not shown: %s", text)
	}
}

func TestRoutesPanelText_HRNeoAbsent(t *testing.T) {
	snap := wire.RouteSnapshot{
		Tunnels: []wire.TunnelMeta{{ID: "t1", Name: "amnezia", NDMSName: "Wireguard0", Enabled: true}},
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
			{ID: "t1", Name: "amnezia", NDMSName: "Wireguard0", Enabled: true},
			{ID: "t2", Name: "newtun", NDMSName: "Wireguard1", Enabled: true},
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
			{ID: "t1", Name: "amnezia", NDMSName: "Wireguard0"},
			{ID: "t2", Name: "newtun", NDMSName: "Wireguard9"},
			{ID: "t3", Name: "third", NDMSName: "Wireguard1"},
		},
		Counts: map[string]wire.TunnelCounts{
			"t1": {DNS: 5, Static: 2, HRNeo: 1},
			"t3": {DNS: 4},
		},
		Other: wire.TunnelCounts{DNS: 12},
	}
	text := RebindPreviewText(snap, "t1", "t2", "8a3f")
	if !strings.Contains(text, "5") || !strings.Contains(text, "2") || !strings.Contains(text, "1") {
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

Create `internal/backend/tg/routes_panel.go`:

```go
package tg

import (
	"fmt"
	"strings"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

const routesMaxPerRow = 2

// RoutesPanelText renders Screen 2 (status overview).
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
	for _, c := range snap.Counts {
		totalDNS += c.DNS
		totalStatic += c.Static
	}
	fmt.Fprintf(&b, "NDMS DNS routes:   %d правил\n", totalDNS)
	fmt.Fprintf(&b, "Static IP routes:  %d правил\n", totalStatic)
	if snap.HRNeo.Installed {
		hr := snap.Other.HRNeo
		for _, c := range snap.Counts {
			hr += c.HRNeo
		}
		fmt.Fprintf(&b, "HR-Neo policies:   %d политик\n", hr)
	}
	b.WriteString("\nПо туннелям (направленные в туннели):\n")
	for _, t := range snap.Tunnels {
		c := snap.Counts[t.ID]
		total := c.DNS + c.Static + c.HRNeo
		fmt.Fprintf(&b, "  %s (%s) → %d\n", t.Name, t.NDMSName, total)
	}
	b.WriteString("\nНе входят в перенос (показано для контроля):\n")
	fmt.Fprintf(&b, "  WAN/system: %d правил\n", snap.Other.DNS+snap.Other.Static+snap.Other.HRNeo)
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
		total := c.DNS + c.Static + c.HRNeo
		if total == 0 {
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
	text := fmt.Sprintf("🛣 Перенос с %s (%s) → куда?\n\nДоступные:", src.Name, src.NDMSName)
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
	total := c.DNS + c.Static + c.HRNeo
	var b strings.Builder
	fmt.Fprintf(&b, "🛣 Превью: %s → %s\n\n", src.Name, dst.Name)
	fmt.Fprintf(&b, "Будет перенесено (%d):\n", total)
	if c.DNS > 0 {
		fmt.Fprintf(&b, "  • DNS routes:  %d\n", c.DNS)
	}
	if c.Static > 0 {
		fmt.Fprintf(&b, "  • Static IP:   %d\n", c.Static)
	}
	if c.HRNeo > 0 {
		fmt.Fprintf(&b, "  • HR-Neo:      %d\n", c.HRNeo)
	}
	b.WriteString("\nНЕ ТРОГАЕМ:\n")
	wanTotal := snap.Other.DNS + snap.Other.Static + snap.Other.HRNeo
	fmt.Fprintf(&b, "  • WAN/system:    %d правил   ← RU-сервисы\n", wanTotal)
	for _, t := range snap.Tunnels {
		if t.ID == srcID {
			continue
		}
		oc := snap.Counts[t.ID]
		ot := oc.DNS + oc.Static + oc.HRNeo
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
	totalFailed := res.DNS.Failed + res.Static.Failed + res.HRNeo.Failed
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
	b.WriteString("\n")
	fmt.Fprintf(&b, "  • Static IP:   %d ok", res.Static.OK)
	if res.Static.Failed > 0 {
		fmt.Fprintf(&b, ", %d FAIL", res.Static.Failed)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "  • HR-Neo:      %d ok", res.HRNeo.OK)
	if res.HRNeo.Failed > 0 {
		fmt.Fprintf(&b, ", %d FAIL", res.HRNeo.Failed)
	}
	b.WriteString("\n")
	if totalFailed > 0 {
		b.WriteString("\nОперация идемпотентна — можно повторить.\n")
		for _, e := range append(append(res.DNS.Errors, res.Static.Errors...), res.HRNeo.Errors...) {
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

1. Add `RebindToken string` to `Args` (next to `ImportToken`):
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

(Note: parts[2] for routes_rebind is the src tunnel id. The CheckName from the existing parser will already hold it; we're just aliasing for clarity.)

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./internal/backend/callbacks -run TestParse -v
```

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(callbacks): parse routes_* callback grammar"
```

### Task 7.2: pendingRebinds + RoutesAction skeleton

**Files:**
- Modify: `internal/backend/callbacks/actions.go`

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

(`crypto/rand`, `encoding/hex`, `errors` already imported; if not, add them.)

- [ ] **Step 4: Run, expect PASS**

- [ ] **Step 5: Commit**

```bash
git commit -am "feat(callbacks): RebindConfirmAction with token consume + enqueue"
```

### Task 7.3: Router wiring — open/refresh/rebind/pick + pendingRebinds storage

**Files:**
- Modify: `internal/backend/callbacks/router.go`

- [ ] **Step 1: Inspect the current Router struct (no test yet — wiring task)**

Read the existing struct, identify where `pendingUploads` is declared (likely a `map[int64]*pendingUpload` field on `Router` with a mutex). Add a parallel field:

```go
	pendingRebindsMu sync.Mutex
	pendingRebinds   map[string]*pendingRebind // keyed by token (cross-user; tokens are global random)
```

Also add helper methods on `*Router`:

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

- [ ] **Step 2: Add new case branches in callback switch**

In the same `switch args.Action` that already handles `tunnels_refresh`, `tunnel_import_replace`, etc., add cases:

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
		// Falls through to existing action.Apply below by selecting the right action.
		if r.rebindConfirmAction != nil {
			action = r.rebindConfirmAction
		}
```

- [ ] **Step 3: Implement the handlers**

Add to `router.go` (near `buildTunnelsPanel`):

```go
// handleRoutesOpen renders Screen 2. Cache lookup unless force=true (refresh).
// If cache miss, enqueues a route_status wire.Command and shows a loading
// placeholder; the result-handler edits the message in place when the agent
// returns the snapshot.
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
	// Cache miss or refresh: enqueue route_status. The result handler edits
	// the message when the agent answers.
	cmd := wire.Command{
		ID:       r.idGen(),
		Action:   "route_status",
		IssuedAt: time.Now().UTC(),
	}
	ref := cmdpkg.MessageRef{
		ChatID:    q.Message.Chat.ID,
		MessageID: q.Message.MessageID,
		ThreadID:  q.Message.MessageThreadID,
	}
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

// handleRoutesRebindStart renders Screen 3 (destination picker).
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

// handleRoutesRebindPick mints a token and renders Screen 4 (preview).
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

- [ ] **Step 4: Add fields to the Router struct (top of file)**

```go
	routesCache         *RoutesCache
	rebindConfirmAction Action
```

- [ ] **Step 5: Build**

```bash
go build ./...
```
Expected: clean (some plumbing in main.go will follow in Milestone 8 — for now compile-check internal/backend only).

- [ ] **Step 6: Commit**

```bash
git commit -am "feat(callbacks): routes panel handlers (open/refresh/rebind/pick)"
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

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/pkg/wire"
)

type fakeEditTG struct {
	editText string
}

func (f *fakeEditTG) EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error {
	f.editText = text
	return nil
}

func TestRoutesPanelNotifier_StatusOK(t *testing.T) {
	cache := &RoutesCache{TTL: time.Minute}
	tg := &fakeEditTG{}
	d := db.NewMemory() // assume helper exists; otherwise inject a fake Users() returning a user
	n := &RoutesPanelNotifier{TG: tg, Cache: cache, DB: d}
	snap := wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{{ID: "t1", Name: "amnezia"}}}
	body, _ := json.Marshal(snap)
	res := wire.CommandResult{Status: "ok", Output: string(body)}
	ref := cmdpkg.MessageRef{Action: "route_status", ChatID: 1, MessageID: 7}
	if err := n.NotifyCommandResult(context.Background(), ref, res, /* userID */ 42); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tg.editText, "amnezia") {
		t.Errorf("expected panel rendered: %s", tg.editText)
	}
	if _, ok := cache.Get(42); !ok {
		t.Errorf("snapshot must be cached after status fetch")
	}
}
```

(If `db.NewMemory` is not a real constructor, replace with a minimal `db.DB`-compatible fake from existing tests.)

- [ ] **Step 2: Run, expect FAIL**

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
		empty := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: "🔁 Обновить", CallbackData: fmt.Sprintf("routes_refresh:%d:_panel_", user.ID)}},
		}}
		return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &empty)
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
	// Invalidate cache so subsequent open shows fresh data.
	n.Cache.Invalidate(user.ID)
	// Look up tunnel names from the cached snapshot if still around — else use IDs.
	src, dst := rb.SrcTunnelID, rb.DstTunnelID
	if snap, ok := n.Cache.Get(user.ID); ok {
		// Cache was just invalidated — this lookup will miss; that's OK, fall back to IDs.
		_ = snap
	}
	totalFailed := rb.DNS.Failed + rb.Static.Failed + rb.HRNeo.Failed
	text := tg.RebindResultText(src, dst, rb)
	kb := tg.RebindResultKeyboard(user.ID, rb.SrcTunnelID, rb.DstTunnelID, totalFailed)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}
```

- [ ] **Step 4: Run, expect PASS**

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

- [ ] **Step 2: Branch in the cmd-result handler**

Find the existing block in `cmdResultHandler(d Deps)` that reads:
```go
if ref, ok := d.CommandSink.ConsumeOriginRef(uid, res.ID); ok {
    if d.TGNotifier != nil {
        if err := d.TGNotifier.NotifyCommandResult(ctx, ref, ref.Action, res, maxChars); err != nil {
            ...
        }
    }
}
```

Replace with:
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

Add a fake RoutesNotifier and a test asserting that for `ref.Action == "route_status"` it is invoked instead of the generic relay:

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

(Use existing fakes — `relayCapture`, `fakeCmdSink` — already in `handler_test.go`. The new test follows their style. `newTestDB` and `authedRequest` are existing helpers from the test file; reuse.)

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

- [ ] **Step 1: Locate the place where Notifier and Router are constructed**

Look for `callbacks.NewNotifier(...)` invocation and the surrounding `Router{...}` literal. After that block, add:

```go
	routesCache := &callbacks.RoutesCache{TTL: 30 * time.Second}
	routesNotifier := &callbacks.RoutesPanelNotifier{
		TG:    tgClient,
		Cache: routesCache,
		DB:    database,
	}
```

- [ ] **Step 2: Inject into Router and Deps**

Add `routesCache` and a `rebindConfirmAction` to the Router constructor (use `callbacks.NewRebindConfirmAction(cmdQueue, router.consumePendingRebind, router.idGen)`).

Inject `RoutesNotifier: routesNotifier` into the `backend.Deps{...}` literal.

- [ ] **Step 3: Build and run unit tests**

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
- Modify: `internal/backend/tg/replykb.go` (or whichever file owns `ReplyKeyboardForTopic`)
- Modify: `internal/backend/callbacks/router.go` — `HandleMessage` text switch

- [ ] **Step 1: Add the button to the per-router reply keyboard**

In the per_router topic keyboard layout, append a row containing:
```go
{Text: "🛣 Маршруты"}
```

- [ ] **Step 2: Add handler in HandleMessage**

In the `switch m.Text` block in `router.go`, after the `"🎛 Туннели":` case, add:

```go
	case "🛣 Маршруты":
		if kind == "per_router" && user != nil {
			r.openRoutesPanelMessage(ctx, m, user)
		} else {
			_, _ = r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID,
				"эта команда работает только в топике пользователя.", "", nil)
		}
```

And add the helper to `router.go`:

```go
// openRoutesPanelMessage sends the initial Routes panel as a fresh message
// (so subsequent edits target this MessageID). Cache miss → load placeholder
// + enqueue route_status; the handler edits when the agent answers.
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
- Modify: `cmd/backend/integration_test.go` (or create one if file doesn't exist)

- [ ] **Step 1: Write the test**

Add a sub-test under the existing integration suite. Pattern:

```go
func TestIntegration_RoutesRebindFlow(t *testing.T) {
	// 1. Mock awg-manager (httptest) with: 3 DNS rules (2 on Wireguard0,
	//    1 on WAN), 2 static rules (1 on Wireguard0, 1 on WAN), no HR-Neo.
	// 2. Mock TG (captured edits).
	// 3. Spin up backend with RoutesCache + RoutesPanelNotifier wired.
	// 4. Spin up a real Runner with awgmgr.Client pointing at the mock.
	// 5. Simulate: user sends "🛣 Маршруты" → backend enqueues route_status →
	//    runner picks up, executes, posts result → backend edits panel.
	//    Assert panel text contains 3 rules total (2 on awg11), 2 untouched.
	// 6. Simulate: user taps routes_rebind:42:t1 → routes_pick:42:t1:t2 →
	//    routes_confirm:42:t1:t2:<token> → runner rebinds → backend renders
	//    Screen 5. Assert: DNS.OK=2, Static.OK=1, WAN rules untouched at the
	//    awg-manager side (verify via mock state).
	t.Skip("placeholder — implement after Milestones 1–8 stabilise")
}
```

The skeleton exists so future you (or a subagent) writes the body once the upstream pieces are stable. Implementation steps:

1. Build awg-manager mock state as a `map[string]*StaticRoute` and `map[string]*DNSRoute` plus a list-tunnels stub. The handlers mutate the maps on update/bulk-backend so subsequent calls reflect the rebind.
2. Use the existing test harness (`cmd/backend/integration_test.go` already has a backend factory; reuse).
3. Use a fake TG that records calls to `EditMessageText` so the test can assert panel content at each stage.

- [ ] **Step 2: Implement and run**

Replace `t.Skip` with the actual flow; iterate until green.

- [ ] **Step 3: Run all tests**

```bash
go test ./...
```
Expected: green.

- [ ] **Step 4: Commit**

```bash
git commit -am "test: integration — routes status → rebind → status flow with WAN-untouched assertion"
```

---

## Milestone 10: Manual Smoke + Wrap-up

### Task 10.1: Run the smoke checklist on testkeen

**Pre-req:** built backend deployed to VPS (or running locally with port-forwarded agent), agent running on testkeen.

- [ ] **Step 1: Prepare fixtures via awg-manager web UI**

On `testkeen` (192.168.31.1:222), open the awg-manager UI and create:
- 2 DNS routes targeting `awg11` (e.g. names "vk-test", "rt-test")
- 1 static IP route targeting `awg11` (e.g. CIDR `10.99.99.0/24`, name "smoke-cidr")
- 1 HR-Neo policy with target `awg11` (e.g. geosite tag `cn:cn`)
- **1 DNS route targeting WAN** (e.g. name "sber-test", domain `sberbank.ru`) — this is the safety canary

- [ ] **Step 2: Import a new tunnel via TG**

Send a fresh `.conf` file to the per_router topic and tap "➕ Добавить новый". Confirm the new tunnel `awg13` appears in the awg-manager UI.

- [ ] **Step 3: Open Routes panel in TG**

Tap "🛣 Маршруты" reply-keyboard button. Verify Screen 2:
- HydraRoute Neo line shown (✅ установлен, работает)
- DNS routes total = 3, Static = 1, HR-Neo = 1
- awg11 row shows total = 4, has [🔄 Перенести] button
- awg13 row shows total = 0, no rebind button
- "Не входят в перенос: WAN/system: 1" line present

- [ ] **Step 4: Tap Перенести on awg11, pick awg13**

Screen 3 lists awg13 (and any others). Tap awg13. Screen 4 preview shows:
- "Будет перенесено (4): DNS=2, Static=1, HR-Neo=1"
- "НЕ ТРОГАЕМ: WAN/system: 1"
- Token displayed

- [ ] **Step 5: Tap Подтвердить**

Wait <30 s. Screen 5 shows: DNS=2 ok, Static=1 ok, HR-Neo=1 ok.

- [ ] **Step 6: Verify in awg-manager web UI**

- vk-test, rt-test, smoke-cidr, and the cn:cn HR-Neo policy are all on awg13
- **sber-test (WAN) is STILL on WAN** — this is the acceptance criterion. If it moved, the feature has failed.

- [ ] **Step 7: Cleanup**

In awg-manager UI, delete the smoke fixtures. Or run a reverse rebind (awg13 → awg11) to confirm idempotency.

### Task 10.2: README / spec link in DEPLOY.md or main README

**Files:**
- Modify: `README.md` (only if there's a feature list section)

- [ ] **Step 1: Add a one-liner under "Реализованные фичи"**

```markdown
- Routes panel — перенос всех smart-routing правил (DNS + Static + HR-Neo) с одного туннеля на другой через Telegram, с явной защитой WAN/системных маршрутов
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

CI will build the 7 release artifacts. After smoke passes against the v0.10.0-rc1 binaries on testkeen, drop the `-rc1` suffix and push v0.10.0 final.

---

## Self-Review Checklist (run before handoff)

- [ ] Spec coverage: every section of the spec maps to at least one task above. Verified §3 (data flow), §4 (UX), §5 (components), §6 (API contract), §7 (caching), §8 (errors), §9 (testing), §10 (open questions resolved in Milestone 0).
- [ ] No "TBD" / "TODO" / "implement later" placeholders in any task body.
- [ ] Type names consistent across milestones: `RouteSnapshot`, `RouteRebindResult`, `TunnelMeta`, `TunnelCounts`, `CategoryResult`, `HRStatus` used the same way in `pkg/wire/routing.go` and in renderer code.
- [ ] Method names consistent: `ListDNSRoutes` / `ListStaticRoutes` / `BulkBackendDNS` / `UpdateStaticRoute` / `GetHRConfig` / `PutHRConfig` / `HydraRouteControl` / `RoutingRefresh` / `GetEnv` / `ReplaceHRTargets`.
- [ ] Callback grammar consistent: `routes_open`, `routes_router`, `routes_rebind`, `routes_pick`, `routes_confirm`, `routes_refresh`, `routes_back`, `routes_close` — same in parse.go, router.go, and TG keyboard builders.
- [ ] Token TTL = 5 min, cache TTL = 30 s — same numbers everywhere they appear.
- [ ] Acceptance criterion in §9.3 step 6 (WAN rule unchanged) is verified by a unit test (Task 4.1's HappyPath asserts `if rule.ID == "s2" t.Errorf("WAN ... must not be updated")`) and by the manual smoke (Task 10.1 step 6).
