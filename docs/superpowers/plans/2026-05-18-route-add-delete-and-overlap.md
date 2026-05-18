# Route Add/Delete and Overlap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add safe Telegram add/delete route workflows with live overlap detection and HR-Neo-first route inventory.

**Architecture:** The agent is the safety boundary: it fetches live awg-manager/HR-Neo data for preview commands and fetches it again before mutating commands. The backend owns Telegram wizard state, short tokens, rendering, and command enqueueing. Route inventory prefers HR-Neo-backed DNS rules when HR-Neo is available and falls back to awg-manager DNS/static routes.

**Tech Stack:** Go, awg-manager HTTP API, Telegram callback_data, existing wire.Command queue, standard `net/netip`, `golang.org/x/net/idna` if already available or added through Go modules.

---

## Parallel Workstreams

Run these as four independent worker agents, then integrate in the parent session:

1. Agent 1: overlap engine + tests for domain/CIDR normalization.
2. Agent 2: awg-manager create/delete wrappers + `route_add_plan`/`route_add`/`route_delete_plan`/`route_delete`.
3. Agent 3: Telegram wizard/callbacks/render preview.
4. Agent 4: HR-Neo inventory/drilldown + service buttons.

Parent integration responsibilities:

- Review worker changes for conflicting wire names and callback token shapes.
- Wire missing imports or interfaces.
- Run `go test ./...`, `go vet ./...`, and `git diff --check`.
- Keep existing uncommitted work intact.

## File Map

Create:

- `internal/agent/actions/route_overlap.go` - target normalization and overlap classification.
- `internal/agent/actions/route_overlap_test.go` - focused overlap tests.
- `internal/agent/actions/route_add_delete.go` - plan/apply actions for add/delete.
- `internal/agent/actions/route_add_delete_test.go` - action-level tests with fake awg-manager client.
- `internal/backend/callbacks/routes_wizard.go` - add/delete draft state and callbacks.
- `internal/backend/callbacks/routes_wizard_test.go` - wizard state tests.
- `internal/backend/tg/routes_add_delete.go` - preview/result renderers.
- `internal/backend/tg/routes_add_delete_test.go` - Telegram text/keyboard tests.
- `internal/agent/actions/hrneo_inventory.go` - HR-Neo rule inventory and service operation helpers if they do not fit existing maintenance code.
- `internal/agent/actions/hrneo_inventory_test.go` - HR-Neo inventory/service tests.

Modify:

- `pkg/wire/routing.go` - add route target, overlap, plan, apply, delete, and HR-Neo inventory payloads.
- `pkg/wire/types.go` - allow new command actions.
- `internal/agent/awgmgr/routing.go` - add create/delete wrappers.
- `internal/agent/awgmgr/types_routing.go` - add request structs only if existing route structs cannot be safely reused.
- `internal/agent/actions/runner.go` - dispatch new commands and keep route mutations under `routeMu`.
- `internal/agent/actions/runner_routes_test.go` - runner allowlist/serialization tests.
- `internal/backend/callbacks/parse.go` - parse new route and HR-Neo callbacks.
- `internal/backend/callbacks/parse_test.go` - callback parse tests.
- `internal/backend/callbacks/router.go` - route wizard dispatch and command enqueueing.
- `internal/backend/callbacks/routes_notifier.go` - render route add/delete command results.
- `internal/backend/callbacks/routes_notifier_test.go` - notifier tests.
- `internal/backend/tg/routes_panel.go` - add buttons for add/delete/inventory.
- `internal/backend/tg/routes_panel_test.go` - panel tests.

## Task 1: Agent 1 - Overlap Engine

**Files:**
- Create: `internal/agent/actions/route_overlap.go`
- Create: `internal/agent/actions/route_overlap_test.go`
- Modify: `pkg/wire/routing.go`

- [ ] **Step 1: Add failing overlap tests**

Cover exact domains, parent/child domains, IDNA/trailing-dot normalization, CIDR containment, single IP normalization, same-bind warning, different-bind block, and opaque info.

Run:

```powershell
go test ./internal/agent/actions -run RouteOverlap -count=1
```

Expected: FAIL because helpers do not exist.

- [ ] **Step 2: Add wire payload structs**

Add these structs to `pkg/wire/routing.go`:

```go
type RouteTarget struct {
	Type  string `json:"type"`  // domain | cidr | opaque
	Value string `json:"value"` // canonical domain or netip prefix
}

type RouteRuleSummary struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Kind    string        `json:"kind"` // dns | static
	Backend string        `json:"backend,omitempty"`
	Enabled bool          `json:"enabled"`
	Bind    string        `json:"bind,omitempty"`
	Targets []RouteTarget `json:"targets,omitempty"`
}

type RouteOverlap struct {
	Severity string           `json:"severity"` // block | warn | info
	Reason   string           `json:"reason"`
	Existing RouteRuleSummary `json:"existing"`
	Target   RouteTarget      `json:"target"`
}
```

- [ ] **Step 3: Implement normalization and classifier**

In `route_overlap.go`, implement pure helpers:

```go
func normalizeRouteTarget(raw string) wire.RouteTarget
func classifyRouteOverlaps(candidate wire.RouteRuleSummary, existing []wire.RouteRuleSummary) []wire.RouteOverlap
```

Use `net/netip` for prefixes and `strings` suffix checks for domains. If `golang.org/x/net/idna` is added, run `go mod tidy` after integration.

- [ ] **Step 4: Verify Agent 1 tests**

Run:

```powershell
go test ./internal/agent/actions -run RouteOverlap -count=1
```

Expected: PASS.

## Task 2: Agent 2 - awg-manager Wrappers and Agent Actions

**Files:**
- Modify: `internal/agent/awgmgr/routing.go`
- Modify: `pkg/wire/routing.go`
- Modify: `pkg/wire/types.go`
- Create: `internal/agent/actions/route_add_delete.go`
- Create: `internal/agent/actions/route_add_delete_test.go`
- Modify: `internal/agent/actions/runner.go`
- Modify: `internal/agent/actions/runner_routes_test.go`

- [ ] **Step 1: Write failing awg-manager wrapper tests**

Add tests for:

- `POST /api/dns-routes/create`
- `POST /api/dns-routes/delete?id=<escaped id>`
- `POST /api/static-routes/create`
- `POST /api/static-routes/delete`

Run:

```powershell
go test ./internal/agent/awgmgr -run 'DNSRoute|StaticRoute' -count=1
```

Expected: FAIL for missing methods.

- [ ] **Step 2: Implement create/delete wrappers**

Add methods:

```go
func (c *Client) CreateDNSRoute(ctx context.Context, rule DNSRoute) error
func (c *Client) DeleteDNSRoute(ctx context.Context, id string) error
func (c *Client) CreateStaticRoute(ctx context.Context, rule StaticRoute) error
func (c *Client) DeleteStaticRoute(ctx context.Context, id string) error
```

DNS delete must URL-escape IDs. Static delete should follow the API shape verified by tests or existing probe notes.

- [ ] **Step 3: Add wire plan/result payloads**

Add payloads for add/delete plan/apply:

```go
type RouteAddRequest struct {
	Kind      string   `json:"kind"` // dns | static
	Name      string   `json:"name"`
	TunnelID  string   `json:"tunnel_id"`
	Targets   []string `json:"targets"`
	UseHRNeo  bool     `json:"use_hr_neo,omitempty"`
	DraftHash string   `json:"draft_hash,omitempty"`
}

type RouteAddPlan struct {
	Request  RouteAddRequest    `json:"request"`
	Route    RouteRuleSummary   `json:"route"`
	Overlaps []RouteOverlap     `json:"overlaps,omitempty"`
	CanApply bool               `json:"can_apply"`
	Hash     string             `json:"hash"`
}

type RouteDeleteRequest struct {
	Kind        string `json:"kind"`
	RouteID     string `json:"route_id"`
	PreviewHash string `json:"preview_hash,omitempty"`
}

type RouteDeletePlan struct {
	Route    RouteRuleSummary `json:"route"`
	Warnings []RouteOverlap  `json:"warnings,omitempty"`
	CanApply bool            `json:"can_apply"`
	Hash     string          `json:"hash"`
}

type RouteApplyResult struct {
	Action    string `json:"action"` // add | delete
	Kind      string `json:"kind"`
	RouteID   string `json:"route_id"`
	RouteName string `json:"route_name"`
	HRNeoRestarted bool `json:"hr_neo_restarted,omitempty"`
}
```

- [ ] **Step 4: Implement plan/apply actions**

`route_add_plan` and `route_delete_plan` must be read-only. `route_add` and `route_delete` must re-fetch live routes and run under `routeMu` in `Runner`.

Return JSON in `CommandResult.Output` for these route commands, matching existing `route_status`/`route_rebind` style.

- [ ] **Step 5: Verify Agent 2 tests**

Run:

```powershell
go test ./internal/agent/awgmgr ./internal/agent/actions -run 'Route(Add|Delete)|RunnerRoutes|DNSRoute|StaticRoute' -count=1
```

Expected: PASS.

## Task 3: Agent 3 - Telegram Wizard, Callbacks, and Rendering

**Files:**
- Modify: `internal/backend/callbacks/parse.go`
- Modify: `internal/backend/callbacks/parse_test.go`
- Create: `internal/backend/callbacks/routes_wizard.go`
- Create: `internal/backend/callbacks/routes_wizard_test.go`
- Modify: `internal/backend/callbacks/router.go`
- Modify: `internal/backend/callbacks/routes_notifier.go`
- Modify: `internal/backend/callbacks/routes_notifier_test.go`
- Modify: `internal/backend/tg/routes_panel.go`
- Create: `internal/backend/tg/routes_add_delete.go`
- Create: `internal/backend/tg/routes_add_delete_test.go`

- [ ] **Step 1: Add callback parse tests**

Cover:

- `routes_add:<uid>:_panel_`
- `routes_add_type:<uid>:dns`
- `routes_add_type:<uid>:static`
- `routes_add_tunnel:<uid>:<draft>:<tunnel>`
- `routes_add_confirm:<uid>:<draft>:<confirm>`
- `routes_add_cancel:<uid>:<draft>`
- `routes_del:<uid>:<route_token>`
- `routes_del_confirm:<uid>:<draft>:<confirm>`
- `routes_del_cancel:<uid>:<draft>`

Run:

```powershell
go test ./internal/backend/callbacks -run 'Parse.*Routes' -count=1
```

Expected: FAIL before parser support.

- [ ] **Step 2: Add wizard state tests**

Test short-lived drafts are scoped by user/topic/router and wrong user cannot confirm.

- [ ] **Step 3: Add Telegram renderers**

Render add/delete previews with stable lines, no column-width alignment:

```text
🛣 Preview
Type: DNS / HR-Neo
Name: example
Target: awg10

Targets:
• example.com
• 10.10.0.0/16

Blocking overlaps:
• example.com already goes to awg11

Next: choose another tunnel or cancel.
```

Confirm button is hidden for add previews with blocking overlaps.

- [ ] **Step 4: Wire callbacks to command enqueueing**

Use existing router patterns for `routes_rebind`: create draft, enqueue plan command, render result in notifier, then enqueue apply command on confirm.

- [ ] **Step 5: Verify Agent 3 tests**

Run:

```powershell
go test ./internal/backend/callbacks ./internal/backend/tg -run 'Routes(Add|Delete|Wizard|Parse)' -count=1
```

Expected: PASS.

## Task 4: Agent 4 - HR-Neo Inventory and Service Buttons

**Files:**
- Modify: `pkg/wire/routing.go`
- Create: `internal/agent/actions/hrneo_inventory.go`
- Create: `internal/agent/actions/hrneo_inventory_test.go`
- Modify: `internal/agent/actions/runner.go`
- Modify: `pkg/wire/types.go`
- Modify: `internal/backend/callbacks/parse.go`
- Modify: `internal/backend/callbacks/router.go`
- Modify: `internal/backend/tg/routes_panel.go`
- Modify: `internal/backend/tg/routes_panel_test.go`

- [ ] **Step 1: Add failing inventory tests**

Test that HR-Neo inventory lists `backend=hydraroute` DNS rules with name, domains, manual domains, CIDR-like routes, bind, policy, and enabled state.

Run:

```powershell
go test ./internal/agent/actions -run HRNeo -count=1
```

Expected: FAIL before implementation.

- [ ] **Step 2: Add HR-Neo inventory payload**

Add:

```go
type HRNeoRule struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Enabled       bool     `json:"enabled"`
	Bind          string   `json:"bind,omitempty"`
	Mode          string   `json:"mode,omitempty"`
	PolicyName    string   `json:"policy_name,omitempty"`
	Domains       []string `json:"domains,omitempty"`
	ManualDomains []string `json:"manual_domains,omitempty"`
	Routes        []string `json:"routes,omitempty"`
}

type HRNeoInventory struct {
	Status HRStatus    `json:"status"`
	Rules  []HRNeoRule `json:"rules,omitempty"`
}
```

- [ ] **Step 3: Implement `hrneo_inventory` action**

Use `HydraRouteStatus` and `ListDNSRoutes`, filter `backend=="hydraroute"`, return JSON.

- [ ] **Step 4: Add service buttons**

Add safe callbacks for `hrneo_start`, `hrneo_stop`, and `hrneo_restart`, reusing existing maintenance confirmation style where possible. These should call existing service restart/control mechanics rather than inventing another execution path.

- [ ] **Step 5: Verify Agent 4 tests**

Run:

```powershell
go test ./internal/agent/actions ./internal/backend/callbacks ./internal/backend/tg -run 'HRNeo|Maint|RoutesPanel' -count=1
```

Expected: PASS.

## Task 5: Parent Integration

**Files:** all changed files from Tasks 1-4.

- [ ] **Step 1: Review worker diffs**

Run:

```powershell
git diff --stat
git diff -- pkg/wire/routing.go pkg/wire/types.go internal/agent/actions/runner.go internal/backend/callbacks/parse.go
```

Expected: No duplicate type names, no conflicting callback names, no route mutation outside `routeMu`.

- [ ] **Step 2: Run focused test suites**

Run:

```powershell
go test ./internal/agent/awgmgr ./internal/agent/actions ./internal/backend/callbacks ./internal/backend/tg -count=1
```

Expected: PASS.

- [ ] **Step 3: Run full verification**

Run:

```powershell
go test ./...
go vet ./...
git diff --check
```

Expected: all pass.

- [ ] **Step 4: Final review**

Confirm:

- Add preview is read-only.
- Delete preview is read-only.
- Add apply revalidates live overlaps.
- Delete apply revalidates preview hash.
- HR-Neo is preferred when available.
- awg-manager fallback still works when HR-Neo is absent.
- Telegram messages stay readable without spacing drift.

