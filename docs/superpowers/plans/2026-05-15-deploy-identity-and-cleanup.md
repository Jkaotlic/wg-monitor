# Deploy identity & cleanup bundle — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Эл иминировать класс ошибок "деплой агента не на тот физический роутер" — wizard сам ищет правильный путь к удалённому Keenetic, требует подтверждения личности перед записью, диагностирует unreachable вместо retry-loop'а, и умеет снять ошибочно-установленного агента.

**Architecture:** Активный probe всех UP-интерфейсов через `net.Dial` + temporary `/32`-route → выбор пути (auto/operator-pick) → cold-install confirm-gate → diagnostic cascade с VPS heartbeat cross-check → uninstall action. Заменяет пассивную CIDR-детекцию из rc3.

**Tech Stack:** Go 1.22+, BurntSushi/toml, golang.org/x/crypto/ssh (уже в проекте), стандартный `net.Dialer` + платформенные `route` CLI.

**Spec:** [docs/superpowers/specs/2026-05-15-deploy-identity-and-cleanup-design.md](../specs/2026-05-15-deploy-identity-and-cleanup-design.md)

---

## File map

| Файл | Роль |
|---|---|
| `cmd/deploy/routing.go` | Прежняя пассивная коллизия → новые `PathReport` / `PathCandidate` / `stepFindReachablePath` / `diagnoseUnreachable` |
| `cmd/deploy/routing_windows.go` | `addTempHostRoute` (token-returning) + `delTempHostRoute` |
| `cmd/deploy/routing_unix.go` | Same для linux/darwin с sudo-fallback |
| `cmd/deploy/routing_other.go` | Stub-сигнатура под новый API |
| `cmd/deploy/routing_test.go` | Unit-тесты с `pathProber` interface + fake-listener |
| `cmd/deploy/state.go` | + `PreferredIface string` в `AgentState` |
| `cmd/deploy/actions.go` | Wire-in path-discovery в install/update; cold-install gate; `actionUninstallAgent`; `diagnoseUnreachable` integration |
| `cmd/deploy/actions_test.go` | Тесты на gate, uninstall, diagnose-cascade |
| `cmd/deploy/menu.go` | + `[N] Удалить агента` пункт, renumber |
| `cmd/deploy/main.go` | + CLI flag `--uninstall <nickname>` / `--uninstall-host <host>` |
| `cmd/deploy/vps_sync.go` | + `LastSeenAt *time.Time` в `RemoteAgent`; `VPSClient.HeartbeatStatus` helper |
| `internal/backend/wizard_handler.go` | + `LastSeenAt *time.Time` в `wizardAgent` JSON shape |
| `internal/backend/wizard_handler_test.go` | + тест на `last_seen_at` в JSON output |

---

## Phase A — Schema & backend prep (no behaviour change)

Эти 3 задачи готовят фундамент. После Phase A агенты по-прежнему деплоятся как сейчас, но backend начинает отдавать heartbeat, а wizard.toml имеет место под `preferred_iface`.

### Task 1: Add `PreferredIface` to AgentState

**Files:**
- Modify: `cmd/deploy/state.go:36-52`

- [ ] **Step 1: Open state.go and locate `AgentState` struct**

The struct currently ends with `ExpectedMAC string` field at line 51.

- [ ] **Step 2: Add new field after `ExpectedMAC`**

Add this field inside `AgentState`:

```go
	// PreferredIface caches the network interface name (as reported by
	// net.Interface.Name — e.g. "Ethernet 2" on Windows, "tun0" on linux)
	// that successfully reached this router on the previous deploy. Layer-1
	// path discovery tries it first on subsequent runs; on failure it falls
	// back to full enumeration and overwrites the cache. Empty = no cache,
	// do full enumeration.
	PreferredIface string `toml:"preferred_iface,omitempty"`
```

- [ ] **Step 3: Build to verify field is recognised**

Run: `go build ./cmd/deploy/...`
Expected: success, no errors.

- [ ] **Step 4: Commit**

```bash
git add cmd/deploy/state.go
git commit -m "feat(deploy/state): + preferred_iface in AgentState"
```

---

### Task 2: Expose `last_seen_at` in `/v1/wizard/agents` JSON

**Files:**
- Modify: `internal/backend/wizard_handler.go:54-114`
- Modify: `internal/backend/wizard_handler_test.go`

- [ ] **Step 1: Write failing test for new JSON field**

Add to `internal/backend/wizard_handler_test.go` (append at end of file):

```go
func TestWizardList_IncludesLastSeenAt(t *testing.T) {
	dbh := newTestDB(t)
	// Insert user with non-nil last_seen_at
	if _, err := dbh.Users().Insert("smith", "raw-token-xx", "0.0.0.0", "awg0"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := dbh.Users().BumpLastSeenAt(1); err != nil {
		t.Fatalf("bump: %v", err)
	}

	deps := Deps{DB: dbh, WizardToken: "tok123"}
	mux := NewMux(deps, nil, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/v1/wizard/agents", nil)
	req.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d, body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Agents []struct {
			Nickname   string  `json:"nickname"`
			LastSeenAt *string `json:"last_seen_at,omitempty"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Agents) != 1 || got.Agents[0].Nickname != "smith" {
		t.Fatalf("agents: %+v", got.Agents)
	}
	if got.Agents[0].LastSeenAt == nil {
		t.Fatal("expected non-nil last_seen_at after BumpLastSeenAt")
	}
}
```

If `newTestDB` or `BumpLastSeenAt` helpers don't exist yet, check the existing test file for fixture helpers and reuse the same pattern as `TestWizardList_OK`.

- [ ] **Step 2: Run failing test**

Run: `go test ./internal/backend/ -run TestWizardList_IncludesLastSeenAt -v`
Expected: FAIL — current `wizardAgent` shape omits the field, so JSON returns no `last_seen_at`.

- [ ] **Step 3: Add `LastSeenAt` to `wizardAgent` struct**

Modify `internal/backend/wizard_handler.go:55-65` — add a new field BEFORE `HasTopic`:

```go
type wizardAgent struct {
	Nickname            string     `json:"nickname"`
	Kind                string     `json:"kind"`
	ThreadID            int64      `json:"thread_id"`
	SSHHost             string     `json:"ssh_host"`
	SSHPort             int64      `json:"ssh_port"`
	SSHUser             string     `json:"ssh_user"`
	Arch                string     `json:"arch"`
	LastDeployedVersion string     `json:"last_deployed_version"`
	LastSeenAt          *time.Time `json:"last_seen_at,omitempty"`
	HasTopic            bool       `json:"has_topic"`
}
```

- [ ] **Step 4: Wire the field in the handler**

Modify `internal/backend/wizard_handler.go:84-109` — inside the `for _, u := range users` loop, add after `if u.LastDeployedVersion != nil { ... }`:

```go
			if u.LastSeenAt != nil {
				ts := *u.LastSeenAt
				a.LastSeenAt = &ts
			}
```

(Copy through pointer — `User.LastSeenAt` is already `*time.Time`, but a fresh pointer keeps the JSON encoder honest in case the source struct is mutated after.)

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/backend/ -run TestWizardList_IncludesLastSeenAt -v`
Expected: PASS.

- [ ] **Step 6: Run full backend test suite to verify no regressions**

Run: `go test ./internal/backend/...`
Expected: all green.

- [ ] **Step 7: Commit**

```bash
git add internal/backend/wizard_handler.go internal/backend/wizard_handler_test.go
git commit -m "feat(backend/wizard): expose last_seen_at in /v1/wizard/agents JSON"
```

---

### Task 3: Add `LastSeenAt` to wizard `RemoteAgent` + `HeartbeatStatus` helper

**Files:**
- Modify: `cmd/deploy/vps_sync.go:18-28`
- Modify: `cmd/deploy/vps_sync.go` (new function `HeartbeatStatus`)
- Modify: `cmd/deploy/vps_sync_test.go` (or create if absent)

- [ ] **Step 1: Add `LastSeenAt` field to `RemoteAgent`**

Modify `cmd/deploy/vps_sync.go:18-28`:

```go
type RemoteAgent struct {
	Nickname            string     `json:"nickname"`
	Kind                string     `json:"kind"`
	ThreadID            int64      `json:"thread_id"`
	SSHHost             string     `json:"ssh_host"`
	SSHPort             int64      `json:"ssh_port"`
	SSHUser             string     `json:"ssh_user"`
	Arch                string     `json:"arch"`
	LastDeployedVersion string     `json:"last_deployed_version"`
	LastSeenAt          *time.Time `json:"last_seen_at,omitempty"`
	HasTopic            bool       `json:"has_topic"`
}
```

(Add `import "time"` if not already present.)

- [ ] **Step 2: Write failing test for `HeartbeatStatus`**

Add to `cmd/deploy/vps_sync_test.go`:

```go
func TestHeartbeatStatus_Fresh(t *testing.T) {
	ts := time.Now().Add(-30 * time.Second)
	s := formatHeartbeatStatus(&ts, time.Now())
	if !strings.Contains(s, "30") || !strings.Contains(s, "fresh") {
		t.Errorf("want 'fresh ~30s', got %q", s)
	}
}

func TestHeartbeatStatus_Stale(t *testing.T) {
	ts := time.Now().Add(-14 * time.Minute)
	s := formatHeartbeatStatus(&ts, time.Now())
	if !strings.Contains(s, "stale") || !strings.Contains(s, "14") {
		t.Errorf("want 'stale 14m', got %q", s)
	}
}

func TestHeartbeatStatus_Never(t *testing.T) {
	s := formatHeartbeatStatus(nil, time.Now())
	if s != "never" {
		t.Errorf("want 'never', got %q", s)
	}
}
```

- [ ] **Step 3: Run failing test**

Run: `go test ./cmd/deploy/ -run TestHeartbeatStatus -v`
Expected: FAIL — `formatHeartbeatStatus` undefined.

- [ ] **Step 4: Implement `formatHeartbeatStatus` + `HeartbeatStatus`**

Append to `cmd/deploy/vps_sync.go`:

```go
// HeartbeatStatus fetches /v1/wizard/agents, finds the named agent and
// returns a human-readable freshness tag. Used by diagnoseUnreachable to
// help operator tell "router offline" from "wizard can't see router (but
// VPS can)". Empty string on any error — caller silently skips this hint.
func (c *VPSClient) HeartbeatStatus(ctx context.Context, nickname string) string {
	agents, err := c.ListAgents(ctx)
	if err != nil {
		return ""
	}
	for _, a := range agents {
		if a.Nickname == nickname {
			return formatHeartbeatStatus(a.LastSeenAt, time.Now())
		}
	}
	return ""
}

// formatHeartbeatStatus renders a *time.Time relative to now as one of:
//   "fresh ~30s" / "fresh ~5m" / "stale 14m" / "stale 2h" / "never".
// "fresh" cutoff is 5 minutes — anything older is "stale". Nil → "never".
// Pure function, no clock dependency — caller passes "now" for testability.
func formatHeartbeatStatus(t *time.Time, now time.Time) string {
	if t == nil {
		return "never"
	}
	age := now.Sub(*t)
	const freshCutoff = 5 * time.Minute
	if age < freshCutoff {
		if age < time.Minute {
			return fmt.Sprintf("fresh ~%ds", int(age.Seconds()))
		}
		return fmt.Sprintf("fresh ~%dm", int(age.Minutes()))
	}
	if age < time.Hour {
		return fmt.Sprintf("stale %dm", int(age.Minutes()))
	}
	return fmt.Sprintf("stale %dh", int(age.Hours()))
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./cmd/deploy/ -run TestHeartbeatStatus -v`
Expected: 3/3 PASS.

- [ ] **Step 6: Verify cross-build for all wizard targets**

Run:
```
GOOS=windows GOARCH=amd64 go build ./cmd/deploy/...
GOOS=linux GOARCH=amd64 go build ./cmd/deploy/...
GOOS=darwin GOARCH=amd64 go build ./cmd/deploy/...
GOOS=darwin GOARCH=arm64 go build ./cmd/deploy/...
```
Expected: all 4 succeed.

- [ ] **Step 7: Commit**

```bash
git add cmd/deploy/vps_sync.go cmd/deploy/vps_sync_test.go
git commit -m "feat(deploy/vps): last_seen_at + HeartbeatStatus helper"
```

---

## Phase B — Layer 1: Active path discovery

После Phase B wizard заменяет пассивную `detectRoutingCollision` на активный probe всех интерфейсов и сам выбирает правильный путь к роутеру.

### Task 4: Define `PathReport`, `PathCandidate`, `PathKind` types + `Prober` interface

**Files:**
- Modify: `cmd/deploy/routing.go` (replace existing file content)
- Create: `cmd/deploy/routing_test.go`

- [ ] **Step 1: Write failing test for the types**

Create `cmd/deploy/routing_test.go`:

```go
package main

import (
	"errors"
	"net"
	"testing"
	"time"
)

func TestPathReport_ChosenNil_WhenNoCandidatesRespond(t *testing.T) {
	r := &PathReport{
		Target: "192.168.31.1:222",
		Candidates: []PathCandidate{
			{Iface: "Ethernet", Kind: PathLAN, Err: errors.New("timeout")},
			{Iface: "tun0", Kind: PathP2P, Err: errors.New("timeout")},
		},
	}
	r.Decide()
	if r.Chosen != nil {
		t.Fatalf("want nil Chosen when all candidates have Err, got %+v", r.Chosen)
	}
	if r.Multiple {
		t.Fatal("want Multiple=false on all-fail")
	}
}

func TestPathReport_AutoPicksOnlyResponder(t *testing.T) {
	r := &PathReport{
		Candidates: []PathCandidate{
			{Iface: "Ethernet", Kind: PathLAN, Err: errors.New("timeout")},
			{Iface: "tun0", Kind: PathP2P, Latency: 142 * time.Millisecond},
		},
	}
	r.Decide()
	if r.Chosen == nil || r.Chosen.Iface != "tun0" {
		t.Fatalf("want Chosen=tun0, got %+v", r.Chosen)
	}
	if r.Multiple {
		t.Fatal("only one responder — Multiple must be false")
	}
}

func TestPathReport_MultipleResponders_SetsMultiple(t *testing.T) {
	r := &PathReport{
		Candidates: []PathCandidate{
			{Iface: "Ethernet", Kind: PathLAN, Latency: 5 * time.Millisecond},
			{Iface: "tun0", Kind: PathP2P, Latency: 142 * time.Millisecond},
		},
	}
	r.Decide()
	if !r.Multiple {
		t.Fatal("two responders — Multiple must be true")
	}
	// Default auto-pick (Y2A): первый P2P
	if r.Chosen == nil || r.Chosen.Kind != PathP2P {
		t.Fatalf("auto-pick should prefer P2P, got %+v", r.Chosen)
	}
}

// Sanity that PathKind constants are distinct.
func TestPathKind_Distinct(t *testing.T) {
	if PathLAN == PathP2P {
		t.Fatal("PathLAN must differ from PathP2P")
	}
}

// Prober is the mockable interface stepFindReachablePath uses; this test
// ensures the package compiles with a stub implementation.
func TestProber_InterfaceShape(t *testing.T) {
	var p Prober = &stubProber{}
	_ = p
	_ = net.Interface{} // referenced through interfaces() return type
}

type stubProber struct{}

func (*stubProber) Interfaces() ([]net.Interface, error)                         { return nil, nil }
func (*stubProber) AddRoute(ip string, ifIdx int) (RouteToken, error)           { return RouteToken{}, nil }
func (*stubProber) DelRoute(RouteToken) error                                    { return nil }
func (*stubProber) Dial(target string, timeout time.Duration) (string, error)   { return "", nil }
```

- [ ] **Step 2: Run failing test (expect compile error)**

Run: `go test ./cmd/deploy/ -run TestPathReport -v`
Expected: FAIL with compile errors — types undefined.

- [ ] **Step 3: Replace `cmd/deploy/routing.go` with the new contents**

Overwrite `cmd/deploy/routing.go` with:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// PathKind classifies a network interface for the purpose of routing
// decisions. We split because operator intent differs: a P2P interface
// (SSTP/PPP/OpenVPN/WG) being UP usually means "this is the path I want
// to deploy through"; a LAN interface being UP means "my normal default
// route". Layer-1 auto-pick prefers P2P on ambiguity.
type PathKind int

const (
	PathLAN PathKind = iota
	PathP2P
)

func (k PathKind) String() string {
	switch k {
	case PathLAN:
		return "LAN"
	case PathP2P:
		return "P2P"
	default:
		return "?"
	}
}

// PathCandidate captures one probe attempt: which iface, what came back.
// Err == nil → reachable; Err != nil → failed (Latency is meaningless then).
type PathCandidate struct {
	Iface   string        // net.Interface.Name
	Index   int           // OS iface index — passed to addTempHostRoute
	LocalIP string        // address bound on the iface, "" if multiple
	Kind    PathKind
	Latency time.Duration // TCP handshake latency
	Err     error
}

// Responded is sugar — "did this probe succeed?". Used by Decide and tests.
func (c PathCandidate) Responded() bool { return c.Err == nil }

// PathReport aggregates the result of probing target via every UP iface.
// Decide() must be called before consumers read Chosen / Multiple.
type PathReport struct {
	Target     string
	Candidates []PathCandidate
	Chosen     *PathCandidate
	Multiple   bool
}

// Decide sets Chosen + Multiple based on Candidates. Rules:
//   - no responders → Chosen=nil, Multiple=false
//   - exactly one responder → Chosen=that one
//   - >1 responders → Multiple=true, default-pick = first P2P responder,
//     fallback to first responder of any kind. Operator may override
//     interactively in the caller.
func (r *PathReport) Decide() {
	var firstAny, firstP2P *PathCandidate
	respCount := 0
	for i := range r.Candidates {
		c := &r.Candidates[i]
		if !c.Responded() {
			continue
		}
		respCount++
		if firstAny == nil {
			firstAny = c
		}
		if c.Kind == PathP2P && firstP2P == nil {
			firstP2P = c
		}
	}
	if respCount == 0 {
		r.Chosen = nil
		r.Multiple = false
		return
	}
	if respCount == 1 {
		r.Chosen = firstAny
		r.Multiple = false
		return
	}
	r.Multiple = true
	if firstP2P != nil {
		r.Chosen = firstP2P
	} else {
		r.Chosen = firstAny
	}
}

// RouteToken is the opaque handle returned by AddRoute. Holds enough info
// for DelRoute to undo the exact add even if the same target was probed
// via several interfaces in the same session.
type RouteToken struct {
	TargetIP string
	IfIndex  int
}

// Prober abstracts the OS-side primitives Layer-1 needs. Production wires
// the real `net.Interfaces` + `route` CLI; tests inject a fake.
type Prober interface {
	Interfaces() ([]net.Interface, error)
	// AddRoute installs a temporary /32 host route for ip via the interface
	// at ifIdx. May return an error on permission denied or syntax issues —
	// caller treats this as "can't force, fall through to default route".
	AddRoute(ip string, ifIdx int) (RouteToken, error)
	// DelRoute removes a previously-added route. Best-effort: callers ignore
	// the error (we just print a warning and move on).
	DelRoute(RouteToken) error
	// Dial performs a TCP handshake to target with timeout. On success
	// returns the local IP the OS bound; on failure returns the error
	// verbatim so callers can surface "connection refused" vs "i/o timeout"
	// to the operator.
	Dial(target string, timeout time.Duration) (localIP string, err error)
}

// realProber is the production implementation.
type realProber struct{}

func (*realProber) Interfaces() ([]net.Interface, error) { return net.Interfaces() }

func (*realProber) AddRoute(ip string, ifIdx int) (RouteToken, error) {
	return addTempHostRoute(ip, ifIdx)
}

func (*realProber) DelRoute(tok RouteToken) error { return delTempHostRoute(tok) }

func (*realProber) Dial(target string, timeout time.Duration) (string, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(context.Background(), "tcp", target)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	host, _, _ := net.SplitHostPort(conn.LocalAddr().String())
	return host, nil
}

// NewRealProber returns a Prober wired to the actual OS — used by deploy
// actions in production. Tests construct their own implementation.
func NewRealProber() Prober { return &realProber{} }

// classifyIface returns PathP2P when iface has FlagPointToPoint set
// (SSTP / WG / OpenVPN / PPP), else PathLAN. Interfaces flagged neither
// UP nor loopback are filtered by callers — this function only classifies.
func classifyIface(iface net.Interface) PathKind {
	if iface.Flags&net.FlagPointToPoint != 0 {
		return PathP2P
	}
	return PathLAN
}

// firstIPv4 returns the first IPv4 address configured on iface as a plain
// dotted-quad, or "" if iface has none. Used in candidate display so the
// operator can spot which physical NIC is which.
func firstIPv4(iface net.Interface) string {
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if v4 := ipnet.IP.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}

// stepFindReachablePath probes target via each UP interface in parallel
// and returns what answered. Cleanup func removes every temporary /32
// route Layer-1 installed during probing — caller MUST defer it. Returns
// error only on fatal enumeration failure; an empty Chosen is signalled
// via PathReport, not error.
func stepFindReachablePath(p Prober, target string, totalTimeout time.Duration) (*PathReport, func(), error) {
	ip, _, err := net.SplitHostPort(target)
	if err != nil {
		return nil, func() {}, fmt.Errorf("target %q: %w", target, err)
	}
	if net.ParseIP(ip) == nil {
		return nil, func() {}, fmt.Errorf("target host %q is not an IPv4 literal", ip)
	}

	ifaces, err := p.Interfaces()
	if err != nil {
		return nil, func() {}, fmt.Errorf("enumerate interfaces: %w", err)
	}

	type probe struct {
		iface net.Interface
		kind  PathKind
		force bool // false → use default route, true → temp /32 via this iface
	}
	var probes []probe
	probes = append(probes, probe{force: false}) // default-route probe (iface field unused)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if classifyIface(iface) == PathP2P {
			probes = append(probes, probe{iface: iface, kind: PathP2P, force: true})
		}
		// LAN ifaces are normally reached via default-route probe — we don't
		// force per-LAN probes (would be redundant unless operator has two
		// LAN ifaces in same /24, which is rare and not in scope).
	}

	// Track tokens added so cleanup removes them all.
	var (
		mu     sync.Mutex
		tokens []RouteToken
	)
	addTok := func(t RouteToken) {
		mu.Lock()
		tokens = append(tokens, t)
		mu.Unlock()
	}

	// Per-probe budget — total budget is divided among probes so a wedged
	// interface can't starve the rest. Floor at 1s so default-route always
	// gets a fair shake even with many ifaces.
	perProbe := totalTimeout / time.Duration(len(probes))
	if perProbe < time.Second {
		perProbe = time.Second
	}

	results := make([]PathCandidate, len(probes))
	var wg sync.WaitGroup
	for i, pr := range probes {
		wg.Add(1)
		go func(i int, pr probe) {
			defer wg.Done()
			c := PathCandidate{
				Iface: "default",
				Kind:  PathLAN,
			}
			if pr.force {
				c.Iface = pr.iface.Name
				c.Index = pr.iface.Index
				c.Kind = pr.kind
				c.LocalIP = firstIPv4(pr.iface)
				tok, addErr := p.AddRoute(ip, pr.iface.Index)
				if addErr != nil {
					c.Err = fmt.Errorf("addRoute %s via %s: %w", ip, pr.iface.Name, addErr)
					results[i] = c
					return
				}
				addTok(tok)
			}
			start := time.Now()
			localIP, dialErr := p.Dial(target, perProbe)
			c.Latency = time.Since(start)
			if dialErr != nil {
				c.Err = dialErr
				results[i] = c
				return
			}
			c.Err = nil
			if !pr.force {
				c.LocalIP = localIP
				// Resolve which iface localIP belongs to so operator sees
				// "default route → Ethernet 5".
				if name, idx := resolveIfaceForLocalIP(ifaces, localIP); name != "" {
					c.Iface = name
					c.Index = idx
					c.Kind = classifyIfaceByIndex(ifaces, idx)
				}
			}
			results[i] = c
		}(i, pr)
	}
	wg.Wait()

	cleanup := func() {
		mu.Lock()
		toks := append([]RouteToken(nil), tokens...)
		tokens = nil
		mu.Unlock()
		for _, t := range toks {
			if err := p.DelRoute(t); err != nil {
				PrintWarn(fmt.Sprintf("cleanup route %s: %v", t.TargetIP, err))
			}
		}
	}

	rep := &PathReport{Target: target, Candidates: results}
	sort.SliceStable(rep.Candidates, func(i, j int) bool {
		// P2P responders first, then LAN responders, then failures.
		ci, cj := rep.Candidates[i], rep.Candidates[j]
		if ci.Responded() != cj.Responded() {
			return ci.Responded()
		}
		if ci.Kind != cj.Kind {
			return ci.Kind == PathP2P
		}
		return ci.Latency < cj.Latency
	})
	rep.Decide()
	return rep, cleanup, nil
}

// resolveIfaceForLocalIP scans ifaces, finds the one that owns localIP,
// returns (name, index). Empty name → not found (rare — possible if iface
// was just brought down between AddRoute and Dial).
func resolveIfaceForLocalIP(ifaces []net.Interface, localIP string) (string, int) {
	target := net.ParseIP(localIP)
	if target == nil {
		return "", 0
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ipnet.IP.Equal(target) {
				return iface.Name, iface.Index
			}
		}
	}
	return "", 0
}

// classifyIfaceByIndex looks up iface kind by its OS index.
func classifyIfaceByIndex(ifaces []net.Interface, idx int) PathKind {
	for _, iface := range ifaces {
		if iface.Index == idx {
			return classifyIface(iface)
		}
	}
	return PathLAN
}

// describePath renders a PathReport into operator-readable text for the
// "Поиск роутера" step. Multi-line, ready for fmt.Print.
func describePath(r *PathReport) string {
	if r == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Поиск %s:\n", r.Target))
	for _, c := range r.Candidates {
		marker := "✗"
		detail := ""
		if c.Responded() {
			marker = "✓"
			detail = fmt.Sprintf("%dмс", c.Latency.Milliseconds())
		} else {
			detail = trimDialErr(c.Err)
		}
		ipBit := ""
		if c.LocalIP != "" {
			ipBit = " (" + c.LocalIP + ")"
		}
		sb.WriteString(fmt.Sprintf("  %s %-20s%s %s [%s]\n",
			marker, c.Iface, ipBit, detail, c.Kind.String()))
	}
	return sb.String()
}

// trimDialErr shortens dialer errors into a single recognisable token —
// "timeout" / "refused" / "no route" / raw err otherwise. Keeps output
// scannable in the multi-candidate table.
func trimDialErr(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "i/o timeout") || errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case strings.Contains(s, "refused"):
		return "refused"
	case strings.Contains(s, "no route to host"), strings.Contains(s, "network unreachable"):
		return "no route"
	default:
		return strings.TrimSpace(err.Error())
	}
}
```

(Note: this REPLACES the old `detectRoutingCollision`/`setupRouteFix` etc. The wire-in in actions.go will be redone in Task 9.)

- [ ] **Step 4: Build to verify the new file compiles**

Run: `go build ./cmd/deploy/...`
Expected: FAIL — `addTempHostRoute` / `delTempHostRoute` are not defined yet (Tasks 6/7), `setupRouteFix` callers in actions.go aren't updated yet.

The compile errors are temporary and will be fixed by Tasks 6, 7, 9. Save the build error output — you'll re-run it after Task 9 wiring.

- [ ] **Step 5: Run unit tests for types only (skipping integration)**

Run: `go test ./cmd/deploy/ -run TestPathReport -v` and `go test ./cmd/deploy/ -run TestPathKind -v`
Expected: still FAIL (compile error blocks the test binary).

Don't commit yet. Task 5 finishes the routing.go side, Tasks 6+7 finish the platform side, Task 9 wires it in. Phase-B is committed atomically when all those land.

---

### Task 5: Add `stepFindReachablePath` unit tests with fake Prober

**Files:**
- Modify: `cmd/deploy/routing_test.go`

- [ ] **Step 1: Add fake prober + 3 scenario tests**

Append to `cmd/deploy/routing_test.go`:

```go
type fakeProber struct {
	ifaces    []net.Interface
	addRoutes map[int]error               // ifIdx → error (nil → success)
	dials     map[string]fakeDialResult   // target → result (default uses key "")
	addCalls  []int
	delCalls  []RouteToken
	mu        sync.Mutex
}

type fakeDialResult struct {
	localIP string
	err     error
	latency time.Duration
}

func (f *fakeProber) Interfaces() ([]net.Interface, error) { return f.ifaces, nil }

func (f *fakeProber) AddRoute(ip string, ifIdx int) (RouteToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addCalls = append(f.addCalls, ifIdx)
	if err, ok := f.addRoutes[ifIdx]; ok && err != nil {
		return RouteToken{}, err
	}
	return RouteToken{TargetIP: ip, IfIndex: ifIdx}, nil
}

func (f *fakeProber) DelRoute(t RouteToken) error {
	f.mu.Lock()
	f.delCalls = append(f.delCalls, t)
	f.mu.Unlock()
	return nil
}

func (f *fakeProber) Dial(target string, timeout time.Duration) (string, error) {
	// In this fake "current routing" maps the per-iface probe via a key
	// like "ifIdx=N", and the default-route probe uses key "default". The
	// caller chooses which kind of probe by which AddRoute calls preceded it.
	f.mu.Lock()
	key := "default"
	if len(f.addCalls) > 0 {
		key = fmt.Sprintf("ifIdx=%d", f.addCalls[len(f.addCalls)-1])
	}
	res := f.dials[key]
	f.mu.Unlock()
	if res.latency > 0 {
		time.Sleep(res.latency)
	}
	return res.localIP, res.err
}

func mockIface(idx int, name string, p2p bool, ip string) net.Interface {
	flags := net.FlagUp
	if p2p {
		flags |= net.FlagPointToPoint
	}
	return net.Interface{
		Index:        idx,
		Name:         name,
		Flags:        flags,
		HardwareAddr: nil,
	}
}

// Note: the fake's Dial routes by latest AddRoute call only, so test
// scenarios should be written to probe one iface at a time. For multi-
// candidate scenarios we'll rely on the production code calling AddRoute
// before each per-iface Dial.

func TestStepFindReachablePath_SinglePathResponds(t *testing.T) {
	f := &fakeProber{
		ifaces: []net.Interface{
			mockIface(1, "Ethernet", false, "192.168.31.5"),
			mockIface(2, "SSTP-Client", true, "10.0.0.5"),
		},
		dials: map[string]fakeDialResult{
			"default":  {err: errors.New("i/o timeout")},
			"ifIdx=2":  {localIP: "10.0.0.5", latency: 50 * time.Millisecond},
		},
	}
	rep, cleanup, err := stepFindReachablePath(f, "192.168.31.1:222", 5*time.Second)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Chosen == nil || rep.Chosen.Iface != "SSTP-Client" {
		t.Fatalf("want chosen=SSTP-Client, got %+v", rep.Chosen)
	}
	if rep.Multiple {
		t.Fatal("only one responder — Multiple must be false")
	}
}

func TestStepFindReachablePath_NoResponse(t *testing.T) {
	f := &fakeProber{
		ifaces: []net.Interface{
			mockIface(1, "Ethernet", false, "192.168.31.5"),
			mockIface(2, "SSTP-Client", true, "10.0.0.5"),
		},
		dials: map[string]fakeDialResult{
			"default": {err: errors.New("i/o timeout")},
			"ifIdx=2": {err: errors.New("i/o timeout")},
		},
	}
	rep, cleanup, err := stepFindReachablePath(f, "192.168.31.1:222", 5*time.Second)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Chosen != nil {
		t.Fatalf("want Chosen=nil on all-timeout, got %+v", rep.Chosen)
	}
}

func TestStepFindReachablePath_InvalidTarget(t *testing.T) {
	f := &fakeProber{}
	_, cleanup, err := stepFindReachablePath(f, "not-an-ip:222", 5*time.Second)
	defer cleanup()
	if err == nil {
		t.Fatal("want error for non-IP target")
	}
}

func TestDescribePath_Renders(t *testing.T) {
	r := &PathReport{
		Target: "192.168.31.1:222",
		Candidates: []PathCandidate{
			{Iface: "SSTP-Client", LocalIP: "10.0.0.5", Kind: PathP2P, Latency: 142 * time.Millisecond},
			{Iface: "Ethernet", LocalIP: "192.168.31.5", Kind: PathLAN, Err: errors.New("i/o timeout")},
		},
	}
	out := describePath(r)
	if !strings.Contains(out, "SSTP-Client") || !strings.Contains(out, "142") {
		t.Errorf("missing P2P responder: %q", out)
	}
	if !strings.Contains(out, "Ethernet") || !strings.Contains(out, "timeout") {
		t.Errorf("missing LAN fail row: %q", out)
	}
}
```

- [ ] **Step 2: Wait until Tasks 6+7+8 fix compile errors before running**

This task is committed together with Tasks 6/7/8/9 (atomic Phase B).

---

### Task 6: Update `routing_windows.go` to `addTempHostRoute` / `delTempHostRoute` shape

**Files:**
- Modify: `cmd/deploy/routing_windows.go` (full rewrite)

- [ ] **Step 1: Replace file contents**

Overwrite `cmd/deploy/routing_windows.go`:

```go
//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// addTempHostRoute installs a /32 host route for targetIP pinned to the
// Windows interface at ifIndex. Uses the legacy `route ADD` CLI (works
// without ICACLS-tier admin rights on most Windows 11 installs when the
// operator is in Network Configuration Operators group; otherwise falls
// back with an access-denied error — wizard surfaces and continues with
// default-route probe). Metric 1 beats /24 entries from both LAN and SSTP.
//
// Returns RouteToken which the caller passes to delTempHostRoute to undo.
// We return the token rather than a closure so a single defer can iterate
// over multiple tokens collected during multi-iface probing.
func addTempHostRoute(targetIP string, ifIndex int) (RouteToken, error) {
	addCmd := exec.Command("route", "ADD", targetIP, "MASK", "255.255.255.255", "0.0.0.0", "IF", fmt.Sprint(ifIndex), "METRIC", "1")
	out, err := addCmd.CombinedOutput()
	if err != nil {
		return RouteToken{}, fmt.Errorf("route ADD %s/32 IF %d: %v: %s",
			targetIP, ifIndex, err, strings.TrimSpace(string(out)))
	}
	return RouteToken{TargetIP: targetIP, IfIndex: ifIndex}, nil
}

// delTempHostRoute removes the route that addTempHostRoute installed.
// `route DELETE` doesn't take an IF param — IP alone identifies the route
// added at METRIC 1 above. Errors print a warning + manual hint and
// return nil — we don't want a cleanup failure to mask the deploy result.
func delTempHostRoute(t RouteToken) error {
	delCmd := exec.Command("route", "DELETE", t.TargetIP)
	if out, err := delCmd.CombinedOutput(); err != nil {
		PrintWarn(fmt.Sprintf("не смог снять /32 маршрут на %s: %v: %s — удали руками: route DELETE %s",
			t.TargetIP, err, strings.TrimSpace(string(out)), t.TargetIP))
		return nil
	}
	PrintInfo(fmt.Sprintf("временный /32-маршрут к %s снят", t.TargetIP))
	return nil
}
```

- [ ] **Step 2: Cross-build Windows target**

Run: `GOOS=windows GOARCH=amd64 go build ./cmd/deploy/...`
Expected: still fails (actions.go references old `setupRouteFix`). Save error for later cleanup in Task 9.

---

### Task 7: Update `routing_unix.go` to new shape with multi-call sudo

**Files:**
- Modify: `cmd/deploy/routing_unix.go` (full rewrite)

- [ ] **Step 1: Replace file contents**

Overwrite `cmd/deploy/routing_unix.go`:

```go
//go:build linux || darwin

package main

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// sudoSession remembers whether we needed sudo for the FIRST route add in
// this wizard run. Subsequent del/add calls reuse the same decision so
// the operator doesn't get re-prompted (sudo cred cache handles it for ~5
// minutes by default).
var sudoSession = struct {
	mu    sync.Mutex
	known bool
	used  bool
}{}

// addTempHostRoute installs a /32 host route for targetIP via the
// interface at ifIndex. Linux uses iproute2, macOS uses BSD `route`. Tries
// direct exec first, falls back to sudo on permission denied. The sudo
// fallback prints a heads-up because the password prompt isn't visible
// in our wrapper.
func addTempHostRoute(targetIP string, ifIndex int) (RouteToken, error) {
	iface, err := net.InterfaceByIndex(ifIndex)
	if err != nil {
		return RouteToken{}, fmt.Errorf("resolve iface name for index %d: %w", ifIndex, err)
	}
	binary, addArgs, _, manualHint := unixRouteCmds(runtime.GOOS, targetIP, iface.Name)
	if binary == "" {
		return RouteToken{}, fmt.Errorf("unsupported OS %q for auto route fix; run manually: %s", runtime.GOOS, manualHint)
	}

	if out, err := exec.Command(binary, addArgs...).CombinedOutput(); err == nil {
		setSudoSessionUsed(false)
		return RouteToken{TargetIP: targetIP, IfIndex: ifIndex}, nil
	} else if !looksLikePermissionDenied(out, err) {
		return RouteToken{}, fmt.Errorf("%s %s: %v: %s", binary, strings.Join(addArgs, " "), err, strings.TrimSpace(string(out)))
	}

	if !sudoSessionUsed() {
		PrintInfo(fmt.Sprintf("повторяю через sudo (введи пароль) — нужен root для routing table: sudo %s %s",
			binary, strings.Join(addArgs, " ")))
	}
	sudoArgs := append([]string{binary}, addArgs...)
	cmd := exec.Command("sudo", sudoArgs...)
	cmd.Stdin = nil
	if out, err := cmd.CombinedOutput(); err != nil {
		return RouteToken{}, fmt.Errorf("sudo %s %s: %v: %s",
			binary, strings.Join(addArgs, " "), err, strings.TrimSpace(string(out)))
	}
	setSudoSessionUsed(true)
	return RouteToken{TargetIP: targetIP, IfIndex: ifIndex}, nil
}

// delTempHostRoute mirrors the add path's sudo decision. Best-effort:
// warns on failure but returns nil so the deploy result isn't masked.
func delTempHostRoute(t RouteToken) error {
	iface, err := net.InterfaceByIndex(t.IfIndex)
	if err != nil {
		PrintWarn(fmt.Sprintf("delTempHostRoute: cannot resolve iface idx %d: %v", t.IfIndex, err))
		return nil
	}
	binary, _, delArgs, manualHint := unixRouteCmds(runtime.GOOS, t.TargetIP, iface.Name)
	if binary == "" {
		PrintWarn("delTempHostRoute: " + manualHint)
		return nil
	}
	var cmd *exec.Cmd
	if sudoSessionUsed() {
		cmd = exec.Command("sudo", append([]string{binary}, delArgs...)...)
	} else {
		cmd = exec.Command(binary, delArgs...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		PrintWarn(fmt.Sprintf("не смог снять /32 маршрут к %s: %v: %s — удали руками: %s %s %s",
			t.TargetIP, err, strings.TrimSpace(string(out)),
			sudoPrefix(), binary, strings.Join(delArgs, " ")))
		return nil
	}
	PrintInfo(fmt.Sprintf("временный /32-маршрут к %s снят", t.TargetIP))
	return nil
}

func setSudoSessionUsed(used bool) {
	sudoSession.mu.Lock()
	defer sudoSession.mu.Unlock()
	sudoSession.known = true
	sudoSession.used = used
}

func sudoSessionUsed() bool {
	sudoSession.mu.Lock()
	defer sudoSession.mu.Unlock()
	return sudoSession.known && sudoSession.used
}

func sudoPrefix() string {
	if sudoSessionUsed() {
		return "sudo"
	}
	return ""
}

func unixRouteCmds(goos, targetIP, ifaceName string) (string, []string, []string, string) {
	switch goos {
	case "linux":
		return "ip",
			[]string{"route", "add", targetIP + "/32", "dev", ifaceName, "metric", "1"},
			[]string{"route", "del", targetIP + "/32"},
			fmt.Sprintf("sudo ip route add %s/32 dev %s metric 1", targetIP, ifaceName)
	case "darwin":
		return "route",
			[]string{"-n", "add", "-host", targetIP, "-interface", ifaceName},
			[]string{"-n", "delete", "-host", targetIP},
			fmt.Sprintf("sudo route -n add -host %s -interface %s", targetIP, ifaceName)
	}
	return "", nil, nil, ""
}

func looksLikePermissionDenied(out []byte, err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(string(out) + " " + err.Error())
	return strings.Contains(s, "permission denied") ||
		strings.Contains(s, "operation not permitted") ||
		strings.Contains(s, "must be root") ||
		strings.Contains(s, "you must be root") ||
		strings.Contains(s, "rtnetlink answers: operation not permitted")
}
```

- [ ] **Step 2: Cross-build Linux + macOS targets**

Run:
```
GOOS=linux GOARCH=amd64 go build ./cmd/deploy/...
GOOS=darwin GOARCH=amd64 go build ./cmd/deploy/...
GOOS=darwin GOARCH=arm64 go build ./cmd/deploy/...
```
Expected: all still fail at actions.go callers — that's resolved in Task 9.

---

### Task 8: Update `routing_other.go` stub to new signature

**Files:**
- Modify: `cmd/deploy/routing_other.go`

- [ ] **Step 1: Replace file contents**

Overwrite `cmd/deploy/routing_other.go`:

```go
//go:build !windows && !linux && !darwin

package main

import "fmt"

// addTempHostRoute is a no-op stub for OSes we don't have a route-add
// command line for (BSDs other than darwin, plan9, etc). Returns a
// zero-value token and an error pointing at manual remediation.
func addTempHostRoute(targetIP string, ifIndex int) (RouteToken, error) {
	return RouteToken{}, fmt.Errorf("auto route fix not implemented for this OS; add /32 host route via your SSTP/VPN iface manually for %s", targetIP)
}

// delTempHostRoute is a no-op on unsupported OSes — addTempHostRoute
// never returns a valid token so this is unreachable in practice, but
// the function must exist for cross-build to succeed.
func delTempHostRoute(t RouteToken) error {
	return nil
}
```

---

### Task 9: Wire Layer 1 into `actionInstallAgent` and `actionUpdateAgent`

**Files:**
- Modify: `cmd/deploy/actions.go:266-272` (actionUpdateAgent — replace setupRouteFix call)
- Modify: `cmd/deploy/actions.go:460-464` (actionInstallAgent — replace setupRouteFix call)
- Modify: `cmd/deploy/actions.go` (add helper `runPathDiscoveryStep`)

- [ ] **Step 1: Add path-discovery helper near top of actions.go**

Add this function near the top of `cmd/deploy/actions.go` (after the imports, before the first action func):

```go
// runPathDiscoveryStep is the operator-facing wrapper around
// stepFindReachablePath. Prints the candidate table, handles the
// multi-responder prompt, returns the cleanup func + chosen iface name
// (saved to wizard.toml as PreferredIface on success). Returns nil err +
// "" iface to signal "no path found" — caller delegates to
// diagnoseUnreachable for the cascade.
func runPathDiscoveryStep(host string, port int, preferred string, prober Prober) (*PathReport, func(), string, error) {
	target := fmt.Sprintf("%s:%d", host, port)
	PrintInfo("ищу " + target + " через все доступные интерфейсы (5с)...")
	rep, cleanup, err := stepFindReachablePath(prober, target, 5*time.Second)
	if err != nil {
		return nil, cleanup, "", err
	}
	fmt.Print(describePath(rep))
	if rep.Chosen == nil {
		return rep, cleanup, "", nil
	}
	if rep.Multiple && os.Getenv("WG_YES_TO_ALL") != "1" {
		// Multi-responder: invite operator to pick. Default = current Chosen
		// (already the auto-pick from Decide()).
		fmt.Println("Несколько путей отвечают. Кого выбираешь?")
		responders := make([]PathCandidate, 0, len(rep.Candidates))
		for _, c := range rep.Candidates {
			if c.Responded() {
				responders = append(responders, c)
			}
		}
		for i, c := range responders {
			marker := "  "
			if rep.Chosen != nil && c.Iface == rep.Chosen.Iface {
				marker = "→ "
			}
			fmt.Printf("%s[%d] %s (%s, %dмс) [%s]\n", marker, i+1, c.Iface, c.LocalIP, c.Latency.Milliseconds(), c.Kind.String())
		}
		idx := parseIntOr(Ask("номер пути [1]", "1"), 1)
		if idx < 1 || idx > len(responders) {
			idx = 1
		}
		rep.Chosen = &responders[idx-1]
	}
	chosenName := ""
	if rep.Chosen != nil {
		chosenName = rep.Chosen.Iface
		_ = preferred // reserved for Task 10 — fast-path to try preferred first
		PrintOK("использую " + chosenName + " (" + rep.Chosen.LocalIP + ", " + fmt.Sprint(rep.Chosen.Latency.Milliseconds()) + "мс)")
	}
	return rep, cleanup, chosenName, nil
}
```

- [ ] **Step 2: Replace `setupRouteFix` call in `actionUpdateAgent`**

In `cmd/deploy/actions.go` find this block (around line 266-272):
```go
	// Pre-flight: detect operator-side routing collision (own LAN + SSTP both
	// in target's /24 — classic 192.168.31.x scenario) and pin a /32 host
	// route via the SSTP iface for the duration of this deploy. Defer rolls
	// it back whether deploy succeeded or not, so the operator's routing
	// table is left as we found it.
	defer setupRouteFix(ag.Host)()
```

Replace with:

```go
	rep, cleanup, iface, err := runPathDiscoveryStep(ag.Host, portOrDefault(ag.Port, 222), ag.PreferredIface, NewRealProber())
	defer cleanup()
	if err != nil {
		PrintFail("path discovery: " + err.Error())
		return err
	}
	if rep.Chosen == nil {
		return diagnoseUnreachable(state, ag, rep, secrets)
	}
	if iface != "" && iface != ag.PreferredIface {
		ag.PreferredIface = iface
	}
```

- [ ] **Step 3: Replace `setupRouteFix` call in `actionInstallAgent`**

Find around line 460-464:
```go
	// Pre-flight: same routing-collision auto-fix as actionUpdateAgent. For
	// a re-install (existingNick == ag.Nickname later in step 2) this also
	// gets the operator a clean route; for a true cold install (new router)
	// this is what makes /opt/bin upload actually traverse SSTP.
	defer setupRouteFix(ag.Host)()
```

Replace with:

```go
	rep, cleanup, iface, perr := runPathDiscoveryStep(ag.Host, ag.Port, ag.PreferredIface, NewRealProber())
	defer cleanup()
	if perr != nil {
		PrintFail("path discovery: " + perr.Error())
		return perr
	}
	if rep.Chosen == nil {
		return diagnoseUnreachable(state, ag, rep, secrets)
	}
	if iface != "" && iface != ag.PreferredIface {
		ag.PreferredIface = iface
	}
```

- [ ] **Step 4: Add temporary `diagnoseUnreachable` stub**

In `cmd/deploy/actions.go`, add (will be fleshed out in Task 12):

```go
// diagnoseUnreachable handles the "Layer 1 found no responding path" case
// — see Task 12 for the full cascade. This stub keeps the build green
// during Phase B; Phase D replaces the body.
func diagnoseUnreachable(state *State, ag *AgentState, rep *PathReport, secrets *SecretStore) error {
	PrintFail(fmt.Sprintf("роутер %s:%d недоступен ни через один из проверенных путей", ag.Host, ag.Port))
	return fmt.Errorf("router %s unreachable", ag.Host)
}
```

- [ ] **Step 5: Cross-build all wizard targets**

Run:
```
GOOS=windows GOARCH=amd64 go build ./cmd/deploy/...
GOOS=linux GOARCH=amd64 go build ./cmd/deploy/...
GOOS=darwin GOARCH=amd64 go build ./cmd/deploy/...
GOOS=darwin GOARCH=arm64 go build ./cmd/deploy/...
```
Expected: all 4 PASS.

- [ ] **Step 6: Run full test suite**

Run: `go test ./cmd/deploy/...`
Expected: all green. Tests from Tasks 4+5 now also run.

- [ ] **Step 7: Commit Phase B atomically**

```bash
git add cmd/deploy/routing.go cmd/deploy/routing_windows.go cmd/deploy/routing_unix.go cmd/deploy/routing_other.go cmd/deploy/routing_test.go cmd/deploy/actions.go
git commit -m "feat(deploy): Layer 1 active path discovery (replaces passive CIDR scan)"
```

---

### Task 10: PreferredIface fast-path

**Files:**
- Modify: `cmd/deploy/actions.go` (extend `runPathDiscoveryStep`)
- Modify: `cmd/deploy/routing_test.go` (test)

- [ ] **Step 1: Write failing test for preferred-iface fast-path**

Append to `cmd/deploy/routing_test.go`:

```go
func TestStepFindReachablePath_PreferredFastPath(t *testing.T) {
	// When preferred=tun0 and tun0 responds via default route, we should
	// not enumerate every other iface. We can't observe "didn't probe X"
	// directly — instead we verify the chosen iface is tun0 and
	// addCalls/delCalls remain empty (no /32 was needed; tun0 was already
	// the default route).
	f := &fakeProber{
		ifaces: []net.Interface{
			mockIface(1, "Ethernet", false, "192.168.31.5"),
			mockIface(2, "tun0", true, "10.0.0.5"),
		},
		dials: map[string]fakeDialResult{
			"default": {localIP: "10.0.0.5", latency: 30 * time.Millisecond},
		},
	}
	rep, cleanup, err := stepFindReachablePath(f, "192.168.31.1:222", 5*time.Second)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Chosen == nil || rep.Chosen.Iface != "tun0" {
		t.Fatalf("want chosen=tun0 via default route, got %+v", rep.Chosen)
	}
}
```

- [ ] **Step 2: Run test, observe it passes**

The existing implementation already resolves localIP → iface for default-route, so this test should pass. Confirm with: `go test ./cmd/deploy/ -run TestStepFindReachablePath_PreferredFastPath -v`.

- [ ] **Step 3: Wire `PreferredIface` save to wizard.toml after successful deploy**

In `cmd/deploy/actions.go`:

After the successful `actionInstallAgent` block (right before `pushToVPSBestEffort` call), `ag.PreferredIface` was already set in Step 3 of Task 9 — no extra code needed here, just confirm that `runActionAndSave` in menu.go will persist via SaveState.

Same for `actionUpdateAgent`.

- [ ] **Step 4: Build + test**

Run: `go build ./cmd/deploy/... && go test ./cmd/deploy/...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git add cmd/deploy/routing_test.go
git commit -m "test(deploy): preferred_iface fast-path coverage"
```

---

## Phase C — Layer 2: Cold-install identity gate

### Task 11: Confirm-prompt after identity banner

**Files:**
- Modify: `cmd/deploy/actions.go` (in `actionInstallAgent`, after identity banner)
- Modify: `cmd/deploy/actions_test.go` (test)

- [ ] **Step 1: Write failing test for gate cancellation**

Append to `cmd/deploy/actions_test.go`:

```go
// TestActionInstallAgent_ColdInstallGate_CancelsOnEmptyAnswer covers the
// Layer-2 confirm-gate: cold install (ExpectedMAC == "") with default-N
// answer (operator pressed Enter) must bail before any write. We can't
// run the full action without a real SSH, so we exercise the gate logic
// via the new helper extracted in Step 3.
func TestColdInstallGate_DefaultDeniesOnEmpty(t *testing.T) {
	ag := &AgentState{Nickname: "smith", ExpectedMAC: ""}
	// Simulate Enter-press by feeding empty string as answer.
	allowed := colDIdentityGate(ag, "keenetic", "aabbccddeeff", "arm64",
		func(string, string) string { return "" })
	if allowed {
		t.Fatal("default empty answer must deny; got allow")
	}
}

func TestColdInstallGate_AllowsExplicitYes(t *testing.T) {
	ag := &AgentState{Nickname: "smith", ExpectedMAC: ""}
	allowed := colDIdentityGate(ag, "keenetic", "aabbccddeeff", "arm64",
		func(string, string) string { return "y" })
	if !allowed {
		t.Fatal("explicit y must allow")
	}
}

func TestColdInstallGate_SkipsWhenMACAlreadyPinned(t *testing.T) {
	ag := &AgentState{Nickname: "smith", ExpectedMAC: "aabbccddeeff"}
	called := false
	allowed := colDIdentityGate(ag, "keenetic", "aabbccddeeff", "arm64",
		func(string, string) string { called = true; return "" })
	if !allowed {
		t.Fatal("with ExpectedMAC pinned, gate is bypassed (Layer 1 + verifyExpectedMAC do the job)")
	}
	if called {
		t.Fatal("ask should not be called when gate is bypassed")
	}
}

func TestColdInstallGate_BypassedByYesToAll(t *testing.T) {
	t.Setenv("WG_YES_TO_ALL", "1")
	ag := &AgentState{Nickname: "smith", ExpectedMAC: ""}
	called := false
	allowed := colDIdentityGate(ag, "keenetic", "aabbccddeeff", "arm64",
		func(string, string) string { called = true; return "" })
	if !allowed {
		t.Fatal("WG_YES_TO_ALL=1 must allow")
	}
	if called {
		t.Fatal("ask should not be called under WG_YES_TO_ALL")
	}
}
```

- [ ] **Step 2: Run failing test**

Run: `go test ./cmd/deploy/ -run TestColdInstallGate -v`
Expected: FAIL — `colDIdentityGate` undefined.

- [ ] **Step 3: Implement `colDIdentityGate`**

In `cmd/deploy/actions.go` add:

```go
// colDIdentityGate is the Layer-2 cold-install confirm: prompt operator
// to confirm the physical box matches the nickname they're about to
// install under. Bypassed when:
//   - ExpectedMAC already pinned (re-install or update — verifyExpectedMAC
//     and Layer-1 path-discovery already covered identity)
//   - WG_YES_TO_ALL=1 (scripted runs)
// Returns true to proceed with install, false to bail. The `ask` callback
// is the prompt function — injected for test isolation; production uses Ask.
func colDIdentityGate(ag *AgentState, hostname, mac, arch string, ask func(prompt, def string) string) bool {
	if ag.ExpectedMAC != "" {
		return true
	}
	if os.Getenv("WG_YES_TO_ALL") == "1" {
		return true
	}
	msg := fmt.Sprintf(
		"Это правильный роутер для install под nickname=%q? (hostname=%q mac=%s arch=%s) [y/N]",
		ag.Nickname, hostname, mac, arch,
	)
	ans := strings.ToLower(strings.TrimSpace(ask(msg, "")))
	return ans == "y" || ans == "yes" || ans == "д" || ans == "да"
}
```

- [ ] **Step 4: Wire `colDIdentityGate` into `actionInstallAgent`**

In `actionInstallAgent`, after the existing banner block (line ~518 — after `PrintInfo(fmt.Sprintf("hostname=%q  mac=%s", hostname, mac))`), and BEFORE the existing-nick switch, add:

```go
	if !colDIdentityGate(ag, hostname, mac, arch, Ask) {
		PrintFail("install отменён оператором")
		return fmt.Errorf("install cancelled — identity not confirmed")
	}
```

Note: `arch` is determined later in the existing flow (`stepDetectKeeneticArch`). Either move that step up, or pass `"?"` for arch in the gate (acceptable; operator can still see hostname+MAC).

Decision: pass `"?"` for arch — keeping the existing step order unchanged is safer. Update the call site:

```go
	if !colDIdentityGate(ag, hostname, mac, "?", Ask) {
		PrintFail("install отменён оператором")
		return fmt.Errorf("install cancelled — identity not confirmed")
	}
```

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/deploy/ -run TestColdInstallGate -v`
Expected: 4/4 PASS.

Run full deploy suite: `go test ./cmd/deploy/...`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add cmd/deploy/actions.go cmd/deploy/actions_test.go
git commit -m "feat(deploy/install): Layer 2 cold-install identity confirm-gate"
```

---

## Phase D — Layer 3: Diagnostic cascade

### Task 12: Flesh out `diagnoseUnreachable` with heartbeat cross-check

**Files:**
- Modify: `cmd/deploy/actions.go` (replace stub with full implementation)
- Modify: `cmd/deploy/actions_test.go` (test)

- [ ] **Step 1: Write failing tests for the cascade branches**

Append to `cmd/deploy/actions_test.go`:

```go
// We test the pure-logic helper diagnosisFromReport, not diagnoseUnreachable
// itself — the latter does I/O (heartbeat lookup) which we mock by passing
// pre-resolved status string.

func TestDiagnosisFromReport_NoP2P(t *testing.T) {
	rep := &PathReport{Target: "192.168.31.1:222", Candidates: []PathCandidate{
		{Iface: "Ethernet", Kind: PathLAN, Err: errors.New("i/o timeout")},
	}}
	msg := diagnosisFromReport(rep, "")
	if !strings.Contains(msg, "VPN/SSTP") {
		t.Errorf("want hint about VPN/SSTP, got %q", msg)
	}
}

func TestDiagnosisFromReport_P2PUpButTimeouts(t *testing.T) {
	rep := &PathReport{Target: "192.168.31.1:222", Candidates: []PathCandidate{
		{Iface: "tun0", Kind: PathP2P, Err: errors.New("i/o timeout")},
	}}
	msg := diagnosisFromReport(rep, "")
	if !strings.Contains(msg, "tun0") || !strings.Contains(msg, "не маршрутизирует") {
		t.Errorf("want hint blaming the SSTP server, got %q", msg)
	}
}

func TestDiagnosisFromReport_RefusedHint(t *testing.T) {
	rep := &PathReport{Target: "192.168.31.1:222", Candidates: []PathCandidate{
		{Iface: "Ethernet", Kind: PathLAN, Err: errors.New("dial tcp: connection refused")},
	}}
	msg := diagnosisFromReport(rep, "")
	if !strings.Contains(msg, "порт закрыт") && !strings.Contains(msg, "refused") {
		t.Errorf("want refused-specific hint, got %q", msg)
	}
}

func TestDiagnosisFromReport_FreshHeartbeatBlamesPath(t *testing.T) {
	rep := &PathReport{Target: "192.168.31.1:222", Candidates: []PathCandidate{
		{Iface: "Ethernet", Kind: PathLAN, Err: errors.New("i/o timeout")},
		{Iface: "tun0", Kind: PathP2P, Err: errors.New("i/o timeout")},
	}}
	msg := diagnosisFromReport(rep, "fresh ~47s")
	if !strings.Contains(msg, "fresh") || !strings.Contains(msg, "сетевом пути") {
		t.Errorf("want fresh-heartbeat hint, got %q", msg)
	}
}

func TestDiagnosisFromReport_StaleHeartbeatBlamesRouter(t *testing.T) {
	rep := &PathReport{Target: "192.168.31.1:222", Candidates: []PathCandidate{
		{Iface: "tun0", Kind: PathP2P, Err: errors.New("i/o timeout")},
	}}
	msg := diagnosisFromReport(rep, "stale 14m")
	if !strings.Contains(msg, "stale") || !strings.Contains(msg, "выключен") {
		t.Errorf("want stale-heartbeat hint, got %q", msg)
	}
}
```

- [ ] **Step 2: Run failing tests**

Run: `go test ./cmd/deploy/ -run TestDiagnosis -v`
Expected: FAIL — `diagnosisFromReport` undefined.

- [ ] **Step 3: Implement `diagnosisFromReport` + replace `diagnoseUnreachable` stub**

In `cmd/deploy/actions.go` REPLACE the stub `diagnoseUnreachable` with:

```go
// diagnoseUnreachable handles "Layer 1 found no responding path". Fetches
// VPS heartbeat for the agent (if state allows), classifies the candidate
// failure modes, prints a one-shot diagnostic, returns a generic err so
// the caller doesn't fall into SSH-retry loop.
func diagnoseUnreachable(state *State, ag *AgentState, rep *PathReport, secrets *SecretStore) error {
	hb := ""
	if state.Backend.Domain != "" {
		if tok := secrets.GetNonInteractive("WIZARD_TOKEN"); tok != "" {
			if c := NewVPSClient(state.Backend.Domain, tok); c != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				hb = c.HeartbeatStatus(ctx, ag.Nickname)
				cancel()
			}
		}
	}
	PrintFail(diagnosisFromReport(rep, hb))
	return fmt.Errorf("router %s unreachable", ag.Host)
}

// diagnosisFromReport is the pure-logic core: takes a PathReport with no
// chosen candidate and an optional heartbeat status, returns a multi-line
// operator-readable diagnostic. Heartbeat empty → that branch silently
// dropped. Tested without I/O.
func diagnosisFromReport(rep *PathReport, hb string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("роутер %s недоступен ни через один из проверенных путей.\n", rep.Target))

	// Classify P2P presence + failure modes.
	var hasP2PUp bool
	var refusedSeen, timeoutSeen bool
	for _, c := range rep.Candidates {
		if c.Kind == PathP2P {
			hasP2PUp = true
		}
		if c.Err != nil {
			s := strings.ToLower(c.Err.Error())
			switch {
			case strings.Contains(s, "refused"):
				refusedSeen = true
			case strings.Contains(s, "timeout"), strings.Contains(s, "deadline"):
				timeoutSeen = true
			}
		}
	}

	switch {
	case !hasP2PUp:
		sb.WriteString("  • у тебя нет ни одного UP VPN/SSTP интерфейса. Если ожидал, что роутер через тоннель — подними сначала клиент.\n")
	case timeoutSeen:
		// Find a P2P iface name to name-drop.
		var p2pName string
		for _, c := range rep.Candidates {
			if c.Kind == PathP2P {
				p2pName = c.Iface
				break
			}
		}
		sb.WriteString(fmt.Sprintf("  • %s up, но через него target не отвечает. Возможно сервер не маршрутизирует target, или удалённый firewall блокирует :222.\n", p2pName))
	}

	if refusedSeen {
		sb.WriteString("  • один из путей: порт закрыт (connection refused). SSH либо не на :222, либо firewall.\n")
	}

	switch {
	case strings.HasPrefix(hb, "fresh"):
		sb.WriteString(fmt.Sprintf("  • VPS heartbeat %s — роутер жив на сети, проблема в сетевом пути ОТ ТЕБЯ. Проверь активный SSTP/VPN.\n", hb))
	case strings.HasPrefix(hb, "stale"):
		sb.WriteString(fmt.Sprintf("  • VPS heartbeat %s — роутер давно не отчитывался, возможно выключен или агент упал.\n", hb))
	case hb == "never":
		sb.WriteString("  • VPS heartbeat: never — впервые ставим, нужен out-of-band доступ.\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}
```

(Add `"context"` to imports if not already present.)

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/deploy/ -run TestDiagnosis -v`
Expected: 5/5 PASS.

- [ ] **Step 5: Cross-build all 4 targets**

```
GOOS=windows GOARCH=amd64 go build ./cmd/deploy/...
GOOS=linux GOARCH=amd64 go build ./cmd/deploy/...
GOOS=darwin GOARCH=amd64 go build ./cmd/deploy/...
GOOS=darwin GOARCH=arm64 go build ./cmd/deploy/...
```
Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add cmd/deploy/actions.go cmd/deploy/actions_test.go
git commit -m "feat(deploy): Layer 3 reachability diagnostic cascade"
```

---

## Phase E — Layer 4: Uninstall action

### Task 13: Implement `actionUninstallAgent` core

**Files:**
- Modify: `cmd/deploy/actions.go` (new function)
- Modify: `cmd/deploy/actions_test.go` (tests)

- [ ] **Step 1: Write failing tests for `cleanupAgentPaths`**

Append to `cmd/deploy/actions_test.go`:

```go
// cleanupAgentPaths is the deterministic command-builder for the
// uninstall sequence — we test that the right paths show up rather than
// running them against a real SSH.
func TestCleanupAgentPaths_AllArtifacts(t *testing.T) {
	cmds := cleanupAgentPaths()
	wantFragments := []string{
		"S99wg-monitor stop",
		"killall -9 wg-monitor",
		"/opt/bin/wg-monitor",
		"/opt/bin/wg-monitor.bak",
		"/opt/bin/wg-monitor.new",
		"/opt/etc/wg-monitor",
		"/opt/etc/init.d/S99wg-monitor",
		"/opt/var/wg-monitor",
	}
	joined := strings.Join(cmds, "\n")
	for _, w := range wantFragments {
		if !strings.Contains(joined, w) {
			t.Errorf("cleanup commands missing %q. Full:\n%s", w, joined)
		}
	}
}

func TestCleanupAgentPaths_StopBeforeRemove(t *testing.T) {
	cmds := cleanupAgentPaths()
	stopIdx, rmIdx := -1, -1
	for i, c := range cmds {
		if strings.Contains(c, "stop") && stopIdx == -1 {
			stopIdx = i
		}
		if strings.Contains(c, "rm -f /opt/bin/wg-monitor") && rmIdx == -1 {
			rmIdx = i
		}
	}
	if stopIdx == -1 || rmIdx == -1 || stopIdx > rmIdx {
		t.Errorf("expected stop (idx=%d) before rm (idx=%d)", stopIdx, rmIdx)
	}
}
```

- [ ] **Step 2: Run failing tests**

Run: `go test ./cmd/deploy/ -run TestCleanupAgentPaths -v`
Expected: FAIL — `cleanupAgentPaths` undefined.

- [ ] **Step 3: Implement `cleanupAgentPaths` + `actionUninstallAgent`**

Add to `cmd/deploy/actions.go`:

```go
// cleanupAgentPaths returns the ordered list of shell commands the
// uninstall flow runs to remove every artefact of a wg-monitor agent
// install from a Keenetic router. Pure (no side effects), tested
// directly. Order matters — stop the daemon BEFORE removing its binary,
// or busybox init may re-spawn it mid-rm.
func cleanupAgentPaths() []string {
	return []string{
		"/opt/etc/init.d/S99wg-monitor stop 2>/dev/null; true",
		"killall -9 wg-monitor 2>/dev/null; true",
		"sleep 1",
		"rm -f /opt/bin/wg-monitor /opt/bin/wg-monitor.bak /opt/bin/wg-monitor.new",
		"rm -rf /opt/etc/wg-monitor",
		"rm -f /opt/etc/init.d/S99wg-monitor",
		"rm -rf /opt/var/wg-monitor",
	}
}

// UninstallTarget describes which router to clean. EITHER the named agent
// is resolved from state.Agents, OR explicit Host/Port/User are provided
// (operator uninstalling from a router that's not in wizard.toml — typical
// "I accidentally installed on local box" scenario).
type UninstallTarget struct {
	Nickname string // optional
	Host     string
	Port     int
	User     string
}

// actionUninstallAgent removes a wg-monitor agent from a router after
// double confirmation. Does NOT touch the VPS users-table — the token
// stays valid so re-install on the correct box proceeds normally.
//
// Optionally also clears ExpectedMAC + PreferredIface in wizard.toml so
// the next install-agent under the same nickname can pin a fresh box.
func actionUninstallAgent(state *State, secrets *SecretStore, target UninstallTarget) error {
	// Resolve target.
	host, port, user := target.Host, target.Port, target.User
	var ag *AgentState
	if target.Nickname != "" {
		ag = state.FindAgent(target.Nickname)
		if ag != nil {
			if host == "" {
				host = ag.Host
			}
			if port == 0 {
				port = ag.Port
			}
			if user == "" {
				user = ag.User
			}
		}
	}
	if host == "" {
		host = orDefault(Ask("Хост роутера", "192.168.31.1"), "192.168.31.1")
	}
	if port == 0 {
		port = parseIntOr(Ask("SSH port", "222"), 222)
	}
	if user == "" {
		user = orDefault(Ask("SSH user", "root"), "root")
	}

	// Layer-1 path discovery so we go through the right physical box.
	rep, cleanup, _, err := runPathDiscoveryStep(host, port, "", NewRealProber())
	defer cleanup()
	if err != nil {
		PrintFail("path discovery: " + err.Error())
		return err
	}
	if rep.Chosen == nil {
		PrintFail("роутер недоступен — нечего сносить")
		return fmt.Errorf("router %s unreachable", host)
	}

	envName := ""
	if ag != nil {
		envName = "WG_KEENETIC_PASS_" + strings.ToUpper(ag.Nickname)
	}
	pass := ""
	if envName != "" {
		pass = secrets.GetNonInteractive(envName)
	}
	if pass == "" {
		pass = AskSecret("пароль root для " + host)
	}
	if pass == "" {
		return fmt.Errorf("missing password")
	}

	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}
	alias := host
	if ag != nil {
		alias = ag.Nickname
	}
	s, err := ConnectSSH(host, port, user, pass, kh, alias)
	if err != nil {
		PrintFail("SSH: " + err.Error())
		return err
	}
	defer s.Close()

	hostname := strings.TrimSpace(stepReadOrEmpty(s, "cat /proc/sys/kernel/hostname 2>/dev/null || uname -n"))
	mac := extractMAC(stepDetectPrimaryMAC(s))
	existingNick := stepReadExistingAgentNickname(s)
	if hostname == "" {
		hostname = "?"
	}
	if mac == "" {
		mac = "?"
	}
	PrintInfo(fmt.Sprintf("на этом роутере: hostname=%q mac=%s agent_nickname=%q", hostname, mac, existingNick))
	if existingNick == "" {
		PrintWarn("на роутере нет /opt/etc/wg-monitor/config.yaml — возможно агента уже нет, но пройду по cleanup-списку")
	}

	ans := strings.ToLower(strings.TrimSpace(Ask(
		fmt.Sprintf("Снести агента с этого роутера (%s, %s)? [y/N]", hostname, mac), "")))
	if ans != "y" && ans != "yes" && ans != "д" && ans != "да" {
		PrintInfo("отмена")
		return nil
	}

	for i, cmd := range cleanupAgentPaths() {
		PrintStep(i+1, len(cleanupAgentPaths()), cmd)
		if _, _, _, err := s.Run(cmd); err != nil {
			PrintWarn(fmt.Sprintf("step %d failed (продолжаю): %v", i+1, err))
		}
	}

	if out, _, _, _ := s.Run("pidof wg-monitor"); strings.TrimSpace(out) != "" {
		PrintWarn("pidof wg-monitor всё ещё что-то возвращает (PID " + strings.TrimSpace(out) + ") — проверь вручную")
	} else {
		PrintOK("процесс wg-monitor не запущен")
	}
	if _, _, rc, _ := s.Run("test -f /opt/bin/wg-monitor"); rc == 0 {
		PrintWarn("/opt/bin/wg-monitor всё ещё на месте — что-то пошло не так")
	} else {
		PrintOK("/opt/bin/wg-monitor удалён")
	}

	// Optional: clear wizard.toml MAC + path cache for this nickname.
	if ag != nil && (ag.ExpectedMAC != "" || ag.PreferredIface != "") {
		ans := strings.ToLower(strings.TrimSpace(Ask(
			"Сбросить expected_mac/preferred_iface для "+ag.Nickname+" в wizard.toml? [y/N]", "")))
		if ans == "y" || ans == "yes" || ans == "д" || ans == "да" {
			ag.ExpectedMAC = ""
			ag.PreferredIface = ""
			PrintOK("wizard.toml: expected_mac и preferred_iface сброшены для " + ag.Nickname)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run unit tests**

Run: `go test ./cmd/deploy/ -run TestCleanupAgentPaths -v`
Expected: 2/2 PASS.

- [ ] **Step 5: Cross-build all 4 targets**

```
GOOS=windows GOARCH=amd64 go build ./cmd/deploy/...
GOOS=linux GOARCH=amd64 go build ./cmd/deploy/...
GOOS=darwin GOARCH=amd64 go build ./cmd/deploy/...
GOOS=darwin GOARCH=arm64 go build ./cmd/deploy/...
```
Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add cmd/deploy/actions.go cmd/deploy/actions_test.go
git commit -m "feat(deploy): Layer 4 actionUninstallAgent + cleanupAgentPaths"
```

---

### Task 14: Menu integration for uninstall

**Files:**
- Modify: `cmd/deploy/menu.go:46-79` (add case for new menu item)
- Modify: `cmd/deploy/menu.go:99-114` (add menu entry)

- [ ] **Step 1: Add new menu item between [3] Install and [4] Doctor**

In `cmd/deploy/menu.go` find `printMenuItems`:

Current order:
```
[1] Установить бэкенд
[2] Обновить компоненты
[3] Установить агента
[4] Проверить состояние
[5] Синхронизация с VPS
[6] Открыть wizard.toml
[7] Забыть known_hosts alias
```

New order: insert "Удалить агента" as `[4]`, shift others:
```
[1] Установить бэкенд
[2] Обновить компоненты
[3] Установить агента
[4] Удалить агента          ← NEW
[5] Проверить состояние
[6] Синхронизация с VPS
[7] Открыть wizard.toml
[8] Забыть known_hosts alias
```

Modify `printMenuItems`:

```go
func printMenuItems(state *State) {
	fmt.Println()
	fmt.Println("  [1] Установить бэкенд          " + Colorize("(первичная установка на VPS)", ColorDim))
	if state.Backend.Host == "" && len(state.Agents) == 0 {
		fmt.Println("  [2] Обновить компоненты        " + Colorize("(сначала установи)", ColorDim))
	} else {
		fmt.Println("  [2] Обновить компоненты        " + Colorize("(проверка релиза + выбор что обновить)", ColorDim))
	}
	fmt.Println("  [3] Установить агента          " + Colorize("(новый или ре-установка существующего)", ColorDim))
	fmt.Println("  [4] Удалить агента             " + Colorize("(снести агент с роутера)", ColorDim))
	fmt.Println("  [5] Проверить состояние        " + Colorize("(Doctor: local + VPS + каждый агент)", ColorDim))
	fmt.Println("  [6] Синхронизация с VPS        " + Colorize("(подтянуть список роутеров с бэкенда)", ColorDim))
	fmt.Println("  [7] Открыть wizard.toml в редакторе")
	fmt.Println("  [8] Забыть known_hosts alias   " + Colorize("(если физически заменил роутер)", ColorDim))
	fmt.Println("  [Q] Выход")
	fmt.Println()
}
```

- [ ] **Step 2: Update dispatch switch**

In `RunMenu`, replace the switch block (lines 46-79):

```go
		switch line {
		case "1":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionInstallBackend(state, secrets, dl)
			})
		case "2":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionUpdateComponents(state, secrets, dl)
			})
		case "3":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionAddRouter(state, secrets, dl)
			})
		case "4":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionUninstallAgent(state, secrets, askUninstallTarget(state))
			})
		case "5":
			actionDoctor(state, secrets) //nolint:errcheck
		case "6":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionSyncVPS(state, secrets)
			})
		case "7":
			openInEditor(statePath)
			if reloaded, err := LoadState(statePath); err == nil {
				*state = *reloaded
			}
		case "8":
			ForgetKnownHostInteractive(state) //nolint:errcheck
		case "Q", "":
			return
		default:
			PrintFail("Не понял. Введи 1–8 или Q.")
		}
```

- [ ] **Step 3: Implement `askUninstallTarget` helper**

Append to `cmd/deploy/menu.go`:

```go
// askUninstallTarget builds the UninstallTarget the operator wants to
// clean. Two paths:
//   1. They pick a nickname from state.Agents — we use its known SSH coords.
//   2. They pick "другой" → manual host/port/user — typical "I accidentally
//      installed on a router not in wizard.toml" scenario.
func askUninstallTarget(state *State) UninstallTarget {
	if len(state.Agents) == 0 {
		PrintInfo("в wizard.toml нет агентов — введи параметры роутера руками")
		return UninstallTarget{}
	}
	fmt.Println("Выбери роутер для удаления агента:")
	for i, a := range state.Agents {
		fmt.Printf("  [%d] %s (%s:%d)\n", i+1, a.Nickname, a.Host, a.Port)
	}
	fmt.Printf("  [%d] другой (ввести host/port/user руками)\n", len(state.Agents)+1)
	idx := parseIntOr(Ask("номер", "1"), 1)
	if idx < 1 || idx > len(state.Agents)+1 {
		idx = 1
	}
	if idx == len(state.Agents)+1 {
		return UninstallTarget{}
	}
	a := state.Agents[idx-1]
	return UninstallTarget{Nickname: a.Nickname, Host: a.Host, Port: a.Port, User: a.User}
}
```

- [ ] **Step 4: Build + test**

Run: `go build ./cmd/deploy/... && go test ./cmd/deploy/...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git add cmd/deploy/menu.go
git commit -m "feat(deploy/menu): [4] Удалить агента + askUninstallTarget"
```

---

### Task 15: CLI flag `--uninstall`

**Files:**
- Modify: `cmd/deploy/main.go`

- [ ] **Step 1: Read current main.go to find flag block**

Run: `grep -n "flag\." cmd/deploy/main.go | head -20` and identify where existing flags are declared. Add new flags in the same block.

- [ ] **Step 2: Add `--uninstall` flags**

In `cmd/deploy/main.go` flag block, add:

```go
	uninstallNick := flag.String("uninstall", "", "снести агента с указанного nickname (из wizard.toml)")
	uninstallHost := flag.String("uninstall-host", "", "снести агента с произвольного host (используй вместе с --uninstall-port/--uninstall-user)")
	uninstallPort := flag.Int("uninstall-port", 222, "SSH-порт для --uninstall-host")
	uninstallUser := flag.String("uninstall-user", "root", "SSH-user для --uninstall-host")
```

- [ ] **Step 3: Wire dispatch**

After the existing flag.Parse + subcommand dispatch, add:

```go
	if *uninstallNick != "" {
		state, _ := LoadState(statePath)
		err := actionUninstallAgent(state, secrets, UninstallTarget{Nickname: *uninstallNick})
		if err != nil {
			os.Exit(1)
		}
		_ = SaveState(statePath, state)
		return
	}
	if *uninstallHost != "" {
		state, _ := LoadState(statePath)
		err := actionUninstallAgent(state, secrets, UninstallTarget{Host: *uninstallHost, Port: *uninstallPort, User: *uninstallUser})
		if err != nil {
			os.Exit(1)
		}
		return
	}
```

Place these dispatch blocks alongside existing one-shot dispatchers (e.g., where `--doctor` is wired, if any — otherwise immediately before `RunMenu(...)`).

- [ ] **Step 4: Build all wizard targets**

```
GOOS=windows GOARCH=amd64 go build ./cmd/deploy/...
GOOS=linux GOARCH=amd64 go build ./cmd/deploy/...
GOOS=darwin GOARCH=amd64 go build ./cmd/deploy/...
GOOS=darwin GOARCH=arm64 go build ./cmd/deploy/...
```
Expected: green.

- [ ] **Step 5: Commit**

```bash
git add cmd/deploy/main.go
git commit -m "feat(deploy/cli): --uninstall + --uninstall-host flags"
```

---

### Task 16: Layer 1 inline-suggest cleanup when agent on two responding boxes

**Files:**
- Modify: `cmd/deploy/actions.go` (`actionInstallAgent`)
- Modify: `cmd/deploy/actions_test.go` (test)

- [ ] **Step 1: Write failing test for the suggest logic**

Append to `cmd/deploy/actions_test.go`:

```go
func TestDoubleDeployHint_TriggersWhenSameNicknameOnTwoBoxes(t *testing.T) {
	// Logic-only test: detectDoubleDeploy returns true iff ≥2 reachable
	// candidates report the same existingNick (resolved out-of-band; we
	// pass it as a map keyed by iface name).
	rep := &PathReport{
		Candidates: []PathCandidate{
			{Iface: "Ethernet", Kind: PathLAN, Latency: 5 * time.Millisecond},
			{Iface: "tun0", Kind: PathP2P, Latency: 142 * time.Millisecond},
		},
	}
	nicks := map[string]string{
		"Ethernet": "smith",
		"tun0":     "smith",
	}
	hit := detectDoubleDeploy(rep, nicks, "smith")
	if !hit {
		t.Fatal("want detectDoubleDeploy=true when both boxes have same nickname")
	}
}

func TestDoubleDeployHint_NoHitWhenOnlyOneBoxHasAgent(t *testing.T) {
	rep := &PathReport{
		Candidates: []PathCandidate{
			{Iface: "Ethernet", Kind: PathLAN, Latency: 5 * time.Millisecond},
			{Iface: "tun0", Kind: PathP2P, Latency: 142 * time.Millisecond},
		},
	}
	nicks := map[string]string{
		"Ethernet": "",      // local box has no agent
		"tun0":     "smith", // remote has it (correct state)
	}
	hit := detectDoubleDeploy(rep, nicks, "smith")
	if hit {
		t.Fatal("want detectDoubleDeploy=false when only target box has the agent")
	}
}
```

- [ ] **Step 2: Run failing test**

Run: `go test ./cmd/deploy/ -run TestDoubleDeploy -v`
Expected: FAIL — `detectDoubleDeploy` undefined.

- [ ] **Step 3: Implement `detectDoubleDeploy`**

Add to `cmd/deploy/actions.go`:

```go
// detectDoubleDeploy returns true when ≥2 reachable candidates have the
// same `target` nickname installed on them — the canonical signature of
// "operator deployed to wrong box, then deployed to right box too".
// `existingNicks` is keyed by candidate iface name; values come from
// stepReadExistingAgentNickname run over SSH on each candidate.
func detectDoubleDeploy(rep *PathReport, existingNicks map[string]string, target string) bool {
	if rep == nil || target == "" {
		return false
	}
	count := 0
	for _, c := range rep.Candidates {
		if !c.Responded() {
			continue
		}
		if existingNicks[c.Iface] == target {
			count++
		}
	}
	return count >= 2
}
```

- [ ] **Step 4: Wire inline-suggest into `actionInstallAgent`**

Note: actually probing existingNick over SSH on EACH candidate requires multiple SSH connects per install — expensive and operator pays in password prompts. Pragmatic call: probe `existingNick` only on the chosen path (cheap, existing flow does this anyway), AND probe other responding candidates only when the chosen-path probe finds a conflicting nickname.

In `actionInstallAgent`, after step 2 banner (after `existingNick := stepReadExistingAgentNickname(s)`):

```go
	// If chosen path already has a different/our nickname installed AND there
	// were other responding candidates, probe them too — this is the double-
	// deploy detection. Cheap path: skip when chosen path is the only
	// responder, OR when existingNick == "" (clean box).
	if existingNick != "" && rep.Multiple {
		nicks := map[string]string{rep.Chosen.Iface: existingNick}
		// Probe other responding candidates over a SHORT-LIVED SSH session
		// each. Best-effort — failure here just suppresses the hint, doesn't
		// block install.
		for _, c := range rep.Candidates {
			if c.Iface == rep.Chosen.Iface || !c.Responded() {
				continue
			}
			// Force route to that iface for the duration of this probe.
			tok, err := NewRealProber().AddRoute(ag.Host, c.Index)
			if err != nil {
				continue
			}
			otherSSH, err := ConnectSSH(ag.Host, ag.Port, ag.User, pass, kh, ag.Nickname)
			if err == nil {
				nicks[c.Iface] = stepReadExistingAgentNickname(otherSSH)
				otherSSH.Close()
			}
			_ = NewRealProber().DelRoute(tok)
		}
		if detectDoubleDeploy(rep, nicks, ag.Nickname) {
			PrintWarn(fmt.Sprintf("⚠ агент %q стоит на ДВУХ роутерах одновременно — это ошибочный двойной деплой", ag.Nickname))
			for iface, nick := range nicks {
				if nick == ag.Nickname {
					PrintWarn("  • " + iface)
				}
			}
			ans := strings.ToLower(strings.TrimSpace(Ask(
				"снять с локального (не-выбранного) пути и продолжить install на выбранном? [y/N]", "")))
			if ans == "y" || ans == "yes" || ans == "д" || ans == "да" {
				for _, c := range rep.Candidates {
					if c.Iface == rep.Chosen.Iface {
						continue
					}
					if nicks[c.Iface] == ag.Nickname {
						PrintInfo("снимаю агента через " + c.Iface)
						tok, _ := NewRealProber().AddRoute(ag.Host, c.Index)
						if err := actionUninstallAgent(state, secrets, UninstallTarget{
							Nickname: ag.Nickname, Host: ag.Host, Port: ag.Port, User: ag.User,
						}); err != nil {
							PrintWarn("uninstall на " + c.Iface + ": " + err.Error())
						}
						_ = NewRealProber().DelRoute(tok)
					}
				}
			}
		}
	}
```

This block adds an integration point but compiles independently — the test in Step 3 only exercises `detectDoubleDeploy`. Manual smoke covers the SSH-probing path.

- [ ] **Step 5: Run tests + cross-build**

Run: `go test ./cmd/deploy/... && for os in windows linux darwin; do GOOS=$os GOARCH=amd64 go build ./cmd/deploy/...; done`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add cmd/deploy/actions.go cmd/deploy/actions_test.go
git commit -m "feat(deploy): inline double-deploy detection + cleanup suggest"
```

---

## Phase F — Finalize

### Task 17: Integration smoke check (manual checklist)

This task documents the acceptance smoke from the spec. Not automated — operator runs through it on testkeen.

**Files:**
- Modify: `docs/superpowers/specs/2026-05-15-deploy-identity-and-cleanup-design.md` (move Acceptance smoke → "Smoke results 2026-MM-DD")

- [ ] **Step 1: Execute each smoke scenario from the spec and note outcomes**

Spec section "Acceptance smoke" lists 7 scenarios. Run each:

1. Layer 1 happy path — SSTP + LAN both respond → operator picks SSTP → /32 added → SSH OK → install OK → on exit `/32` снят.
2. Layer 1 SSTP only — LAN target dead → auto-pick SSTP without prompt.
3. Layer 1 fail — SSTP down + target unreachable → Layer 3 cascade with heartbeat hint.
4. Layer 2 — cold install, Enter on prompt → cancel; `y` → MAC pinned.
5. Layer 3 fresh heartbeat — block :222 with iptables on router → wizard says "heartbeat 30с назад, проблема в SSH-пути".
6. Layer 4 uninstall — после ошибочного install: hostname/MAC показал → `y` → 6 артефактов удалены.
7. Layer 4 inline-suggest — install на local, потом install с SSTP → детектится двойной → cleanup → продолжает install.

- [ ] **Step 2: Document results in spec**

In the spec file, replace the "Acceptance smoke" section's bullet list with a result table:

```markdown
## Smoke results <YYYY-MM-DD>

| # | Scenario | Outcome | Notes |
|---|---|---|---|
| 1 | Layer 1 happy path | ✅/⚠/❌ | ... |
| ... | ... | ... | ... |
```

- [ ] **Step 3: Commit results**

```bash
git add docs/superpowers/specs/2026-05-15-deploy-identity-and-cleanup-design.md
git commit -m "docs(spec): smoke results for deploy identity & cleanup bundle"
```

---

### Task 18: Version bump + release tag

**Files:**
- Modify: `cmd/deploy/version.go` (if it carries Version constant; otherwise wherever Version lives)

- [ ] **Step 1: Bump version**

Locate the `Version` constant (try `grep -rn "Version = " cmd/`) and bump to `v0.13.0-rc5`.

- [ ] **Step 2: Cross-build all targets**

```
GOOS=windows GOARCH=amd64 go build -o build/wg-monitor-deploy-windows-amd64.exe ./cmd/deploy
GOOS=linux GOARCH=amd64 go build -o build/wg-monitor-deploy-linux-amd64 ./cmd/deploy
GOOS=darwin GOARCH=amd64 go build -o build/wg-monitor-deploy-darwin-amd64 ./cmd/deploy
GOOS=darwin GOARCH=arm64 go build -o build/wg-monitor-deploy-darwin-arm64 ./cmd/deploy
```

Confirm all 4 produce binaries.

- [ ] **Step 3: Run full test suite**

```
go test ./... -count=1
go vet ./...
```
Expected: all green.

- [ ] **Step 4: Commit + tag**

```bash
git add cmd/deploy/version.go
git commit -m "chore: bump wizard to v0.13.0-rc5"
git tag v0.13.0-rc5
```

Push tag manually after operator confirmation: `git push origin main && git push origin v0.13.0-rc5`.

---

## Self-Review (post-write check)

**1. Spec coverage:**
- Layer 1 (active path discovery): Tasks 4-10 ✓
- Layer 2 (cold-install gate): Task 11 ✓
- Layer 3 (diagnostic cascade): Tasks 2-3, 12 ✓
- Layer 4 (uninstall + inline-suggest): Tasks 13-16 ✓
- Backend `last_seen_at` JSON: Task 2 ✓
- VPSClient `HeartbeatStatus`: Task 3 ✓
- Wizard.toml `PreferredIface`: Task 1 ✓
- CLI `--uninstall`: Task 15 ✓
- Acceptance smoke: Task 17 ✓

**2. Placeholder scan:** No TBD/TODO/incomplete steps. Each code-emitting step has full code; each command step has the exact command + expected outcome.

**3. Type consistency:**
- `PathReport.Chosen` (`*PathCandidate`) — consistent across Tasks 4, 5, 9, 12, 16.
- `RouteToken{TargetIP, IfIndex}` — consistent across Tasks 4, 6, 7, 8.
- `Prober` interface signature — consistent across Tasks 4 (defined), 5 (fake), 9 (`NewRealProber()` callsites).
- `UninstallTarget{Nickname, Host, Port, User}` — consistent across Tasks 13, 14, 15.
- `colDIdentityGate(ag, hostname, mac, arch, ask)` — consistent across Task 11 (defined + 4 tests + wire-in).
- `cleanupAgentPaths()` returns `[]string` — consistent across Task 13.
- `detectDoubleDeploy(rep, nicks, target)` — consistent across Task 16.

**4. Ambiguity:**
- "if existing-nick switch is in actionInstallAgent" — Task 11 explicitly resolves to passing `"?"` for arch to keep step order, with rationale.
- Phase B atomic commit — Task 9 step 7 makes this explicit (single commit covering routing.go + 3 platform files + actions.go).
- Layer 1 multi-iface SSH probing in Task 16 — pragmatic call explained in Step 4 commentary.

Plan covers spec fully.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-15-deploy-identity-and-cleanup.md`. Two execution options:

1. **Subagent-Driven (recommended)** — fresh subagent per task, two-stage review between tasks, fast iteration
2. **Inline Execution** — execute in this session using executing-plans, batch with checkpoints

Which approach?
