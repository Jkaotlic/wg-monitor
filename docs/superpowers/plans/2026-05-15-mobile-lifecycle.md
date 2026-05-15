# Mobile Router Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace HARD-OFFLINE alert spam for `kind=mobile` routers with a wake/sleep lifecycle: adaptive wake-card on rejoin, single info-card on silence, no renotify, no FSM Hard.

**Architecture:** Reuse existing `Report.Resumed=true` agent signal as wake trigger. Branch the heartbeat watcher's `scan()` so mobile users with `MobileLifecycle=true` get a short threshold (`MobileSleepAfter`, 5min) → one-shot `SleepNotifier.SendSleeping`, while static/legacy-mobile keep `SendOffline` HARD path. Two new alert renderers (`wake_report.go`, `sleep_info.go`); no DB or wire-protocol changes.

**Tech Stack:** Go 1.22+, SQLite via existing `internal/backend/db`, Telegram Bot API via `internal/backend/tg`, YAML config (gopkg.in/yaml.v3).

**Spec:** [docs/superpowers/specs/2026-05-15-mobile-lifecycle-design.md](../specs/2026-05-15-mobile-lifecycle-design.md)

---

## File Map

**Create:**
- `internal/backend/alerts/wake_report.go` — pure renderer: `RenderWakeReport(nick, checks) → Card`
- `internal/backend/alerts/wake_report_test.go`
- `internal/backend/alerts/sleep_info.go` — pure renderer: `RenderSleepInfo(nick, lastSeen) → Card`
- `internal/backend/alerts/sleep_info_test.go`
- `internal/backend/alerts/lifecycle_notifier.go` — `WakeNotifier` + `SleepNotifier` impls; topic resolution + `tg.Client.SendMessageWithKeyboard`
- `internal/backend/alerts/lifecycle_notifier_test.go`
- `cmd/backend/backend_mobile_lifecycle_integration_test.go`

**Modify:**
- `internal/backend/config.go` — `HeartbeatConfig.MobileLifecycle *bool` + `MobileSleepAfterSec int` + defaults
- `internal/backend/config_test.go` — defaults coverage
- `internal/backend/heartbeat/watcher.go` — `Config.MobileLifecycle bool` + `MobileSleepAfter time.Duration`; `SleepNotifier` interface + `SetSleepNotifier`; branch in `scan()`; new `sleepNotified` map
- `internal/backend/heartbeat/watcher_test.go` — pin two legacy tests to `MobileLifecycle: false`; 5 new tests
- `internal/backend/handler.go` — `Deps.WakeNotifier WakeNotifier`; hook after `MarkResumed`
- `internal/backend/handler_test.go` — `fakeWakeNotifier`; 3 new tests
- `cmd/backend/main.go` — wire `WakeNotifier`/`SleepNotifier` + pass new Config fields
- `cmd/deploy/templates/backend.yaml.tmpl` — add `mobile_lifecycle: true` + `mobile_sleep_after_sec: 300`

---

## Task 1: Config — `MobileLifecycle` + `MobileSleepAfterSec`

**Files:**
- Modify: `internal/backend/config.go`
- Test: `internal/backend/config_test.go`

- [ ] **Step 1: Write failing test for defaults**

Append to `internal/backend/config_test.go`:

```go
func TestLoadConfig_MobileLifecycleDefaults(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	botPath := filepath.Join(tmp, "bot.txt")
	if err := os.WriteFile(botPath, []byte("TOKEN"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "db_path: /tmp/x.db\n" +
		"telegram:\n  bot_token_file: " + botPath + "\n  chat_id: 1\n  admin_user_id: 2\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Heartbeat.MobileLifecycle == nil || *cfg.Heartbeat.MobileLifecycle != true {
		t.Errorf("MobileLifecycle: want true, got %v", cfg.Heartbeat.MobileLifecycle)
	}
	if cfg.Heartbeat.MobileSleepAfterSec != 300 {
		t.Errorf("MobileSleepAfterSec: want 300, got %d", cfg.Heartbeat.MobileSleepAfterSec)
	}
}

func TestLoadConfig_MobileLifecycleExplicitFalse(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "cfg.yaml")
	botPath := filepath.Join(tmp, "bot.txt")
	os.WriteFile(botPath, []byte("TOKEN"), 0o600)
	body := "db_path: /tmp/x.db\n" +
		"telegram:\n  bot_token_file: " + botPath + "\n  chat_id: 1\n  admin_user_id: 2\n" +
		"heartbeat:\n  mobile_lifecycle: false\n  mobile_sleep_after_sec: 600\n"
	os.WriteFile(cfgPath, []byte(body), 0o600)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Heartbeat.MobileLifecycle == nil || *cfg.Heartbeat.MobileLifecycle != false {
		t.Errorf("explicit false: want false, got %v", cfg.Heartbeat.MobileLifecycle)
	}
	if cfg.Heartbeat.MobileSleepAfterSec != 600 {
		t.Errorf("MobileSleepAfterSec: want 600, got %d", cfg.Heartbeat.MobileSleepAfterSec)
	}
}
```

If imports `os`, `path/filepath`, `testing` are not already imported in the file, add them.

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/backend/ -run TestLoadConfig_MobileLifecycle -v
```
Expected: FAIL — `MobileLifecycle` / `MobileSleepAfterSec` undefined.

- [ ] **Step 3: Add fields to `HeartbeatConfig`**

In `internal/backend/config.go`, locate `type HeartbeatConfig struct` (around line 77) and replace with:

```go
type HeartbeatConfig struct {
	StaleAfterSec       int   `yaml:"stale_after_sec"`         // legacy, applied to static if mobile_sec absent
	StaleAfterStaticSec int   `yaml:"stale_after_static_sec"`  // override for static (home/office) routers
	StaleAfterMobileSec int   `yaml:"stale_after_mobile_sec"`  // legacy: applied only if mobile_lifecycle=false
	ResumeGraceSec      int   `yaml:"resume_grace_sec"`        // suppress OFFLINE this long after Report.Resumed=true
	ScanIntervalSec     int   `yaml:"scan_interval_sec"`
	MobileLifecycle     *bool `yaml:"mobile_lifecycle"`        // NEW: default true. Wake-card on Resumed=true + one-shot sleep-info instead of HARD-OFFLINE.
	MobileSleepAfterSec int   `yaml:"mobile_sleep_after_sec"`  // NEW: default 300. After this many seconds of mobile silence, send one "🌙 вышел из сети" info-card.
}
```

- [ ] **Step 4: Add defaults in `LoadConfig`**

In `internal/backend/config.go::LoadConfig`, locate the block of `if cfg.Heartbeat.XxxSec == 0 { ... }` defaults (around line 160). Append after the `ScanIntervalSec` default:

```go
	if cfg.Heartbeat.MobileSleepAfterSec == 0 {
		cfg.Heartbeat.MobileSleepAfterSec = 300
	}
	if cfg.Heartbeat.MobileLifecycle == nil {
		t := true
		cfg.Heartbeat.MobileLifecycle = &t
	}
```

- [ ] **Step 5: Run tests to verify they pass**

```
go test ./internal/backend/ -run TestLoadConfig -v
```
Expected: PASS (both new tests + any existing config tests).

- [ ] **Step 6: Commit**

```
git add internal/backend/config.go internal/backend/config_test.go
git commit -m "feat(config): mobile_lifecycle + mobile_sleep_after_sec heartbeat knobs"
```

---

## Task 2: Watcher Config — new fields + `staleFor` branch

**Files:**
- Modify: `internal/backend/heartbeat/watcher.go`
- Modify: `internal/backend/heartbeat/watcher_test.go`

- [ ] **Step 1: Pin existing legacy tests to `MobileLifecycle: false`**

In `internal/backend/heartbeat/watcher_test.go`, modify `TestWatcherMobileUsesLongerGrace`. Find the line constructing `Config{...}` (around line 108):

```go
	w := NewWatcher(d, off, Config{
		StaleAfterStatic: 5 * time.Minute,
		StaleAfterMobile: 60 * time.Minute,
		ScanEvery:        time.Hour,
	})
```

Replace with:

```go
	w := NewWatcher(d, off, Config{
		StaleAfterStatic: 5 * time.Minute,
		StaleAfterMobile: 60 * time.Minute,
		MobileLifecycle:  false, // explicitly legacy: this test asserts the StaleAfterMobile path
		ScanEvery:        time.Hour,
	})
```

Apply the same edit to `TestWatcherMobileFiresAfterMobileThreshold` (around line 131).

- [ ] **Step 2: Add new fields to `Config`**

In `internal/backend/heartbeat/watcher.go`, locate `type Config struct` (around line 37) and replace with:

```go
type Config struct {
	StaleAfter       time.Duration // deprecated, see StaleAfter{Static,Mobile}
	StaleAfterStatic time.Duration
	StaleAfterMobile time.Duration // applied only when MobileLifecycle == false
	MobileSleepAfter time.Duration // NEW: threshold for mobile sleep-info when MobileLifecycle == true
	MobileLifecycle  bool          // NEW: true = wake/sleep flow, false = legacy HARD-OFFLINE
	ResumeGrace      time.Duration
	ScanEvery        time.Duration
	RenotifyEvery    time.Duration
}
```

Add a default constant near the existing `defaultStaleAfter*` block:

```go
const (
	defaultStaleAfterStatic = 5 * time.Minute
	defaultStaleAfterMobile = 60 * time.Minute
	defaultMobileSleepAfter = 5 * time.Minute // NEW
	defaultResumeGrace      = 90 * time.Second
	defaultScanEvery        = 30 * time.Second
	defaultRenotifyEvery    = 6 * time.Hour
)
```

- [ ] **Step 3: Update `staleFor` to branch on `MobileLifecycle`**

Replace the existing `staleFor` (around line 54) with:

```go
func (c Config) staleFor(u db.User) time.Duration {
	if u.IsMobile() {
		if c.MobileLifecycle {
			switch {
			case c.MobileSleepAfter > 0:
				return c.MobileSleepAfter
			default:
				return defaultMobileSleepAfter
			}
		}
		switch {
		case c.StaleAfterMobile > 0:
			return c.StaleAfterMobile
		case c.StaleAfter > 0:
			return c.StaleAfter
		default:
			return defaultStaleAfterMobile
		}
	}
	switch {
	case c.StaleAfterStatic > 0:
		return c.StaleAfterStatic
	case c.StaleAfter > 0:
		return c.StaleAfter
	default:
		return defaultStaleAfterStatic
	}
}
```

- [ ] **Step 4: Run watcher tests**

```
go test ./internal/backend/heartbeat/ -v
```
Expected: PASS (legacy tests still green with `MobileLifecycle: false`).

- [ ] **Step 5: Commit**

```
git add internal/backend/heartbeat/watcher.go internal/backend/heartbeat/watcher_test.go
git commit -m "feat(heartbeat): Config.MobileLifecycle + MobileSleepAfter, branch staleFor"
```

---

## Task 3: Watcher — `SleepNotifier` + mobile branch in `scan()`

**Files:**
- Modify: `internal/backend/heartbeat/watcher.go`
- Modify: `internal/backend/heartbeat/watcher_test.go`

- [ ] **Step 1: Write failing tests for sleep-flow**

Append to `internal/backend/heartbeat/watcher_test.go`:

```go
type fakeSleep struct {
	mu    sync.Mutex
	calls []sleepRec
}

type sleepRec struct {
	userID   int64
	nick     string
	lastSeen time.Time
}

func (f *fakeSleep) SendSleeping(_ context.Context, uid int64, nick string, ls time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, sleepRec{uid, nick, ls})
	return nil
}

func (f *fakeSleep) snapshot() []sleepRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sleepRec, len(f.calls))
	copy(out, f.calls)
	return out
}

func TestWatcherMobileLifecycle_SleepInfoAfter5Min(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00aa00"
	uid, _ := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-6*time.Minute))

	off := &fakeOffline{}
	sleep := &fakeSleep{}
	w := NewWatcher(d, off, Config{
		MobileLifecycle:  true,
		MobileSleepAfter: 5 * time.Minute,
		ScanEvery:        time.Hour,
	})
	w.SetSleepNotifier(sleep)

	driveScan(w, now)

	if got := len(sleep.snapshot()); got != 1 {
		t.Fatalf("sleep notifier: want 1 call, got %d", got)
	}
	if got := len(off.snapshot()); got != 0 {
		t.Fatalf("offline notifier: want 0 calls, got %d", got)
	}
}

func TestWatcherMobileLifecycle_NoRenotifyOnRepeatScan(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00bb00"
	uid, _ := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-6*time.Minute))

	sleep := &fakeSleep{}
	w := NewWatcher(d, &fakeOffline{}, Config{
		MobileLifecycle:  true,
		MobileSleepAfter: 5 * time.Minute,
		ScanEvery:        time.Hour,
	})
	w.SetSleepNotifier(sleep)

	driveScan(w, now)
	driveScan(w, now.Add(30*time.Second))
	driveScan(w, now.Add(2*time.Minute))

	if got := len(sleep.snapshot()); got != 1 {
		t.Fatalf("repeat scans must not refire sleep notification; got %d", got)
	}
}

func TestWatcherMobileLifecycle_ResumeClearsSleepFlag(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "cc00cc00cc00cc00cc00cc00cc00cc00cc00cc00cc00cc00cc00cc00cc00cc00"
	uid, _ := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-6*time.Minute))

	sleep := &fakeSleep{}
	w := NewWatcher(d, &fakeOffline{}, Config{
		MobileLifecycle:  true,
		MobileSleepAfter: 5 * time.Minute,
		ScanEvery:        time.Hour,
	})
	w.SetSleepNotifier(sleep)

	driveScan(w, now) // fires sleep #1
	// agent comes back: fresh heartbeat
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(time.Minute))
	driveScan(w, now.Add(time.Minute+1*time.Second)) // fresh, clears sleepNotified
	// goes silent again
	driveScan(w, now.Add(7*time.Minute)) // should fire sleep #2

	if got := len(sleep.snapshot()); got != 2 {
		t.Fatalf("expected sleep to refire after a fresh report round; got %d", got)
	}
}

func TestWatcherMobileLifecycle_FreshUserNoSleepInfo(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "dd00dd00dd00dd00dd00dd00dd00dd00dd00dd00dd00dd00dd00dd00dd00dd00"
	uid, _ := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)
	now := time.Now().UTC()
	// Fresh user inserted 2min ago, no events: latest will fall back to CreatedAt.
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-2*time.Minute))

	sleep := &fakeSleep{}
	w := NewWatcher(d, &fakeOffline{}, Config{
		MobileLifecycle:  true,
		MobileSleepAfter: 5 * time.Minute,
		ScanEvery:        time.Hour,
	})
	w.SetSleepNotifier(sleep)

	driveScan(w, now)

	if got := len(sleep.snapshot()); got != 0 {
		t.Fatalf("fresh mobile user must not fire sleep info; got %d", got)
	}
}

func TestWatcherStaticUnaffectedByMobileLifecycle(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "ee00ee00ee00ee00ee00ee00ee00ee00ee00ee00ee00ee00ee00ee00ee00ee00"
	uid, _ := d.Users().Insert("homerouter", tok, "1.1.1.1", "awg0")
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-10*time.Minute))

	off := &fakeOffline{}
	sleep := &fakeSleep{}
	w := NewWatcher(d, off, Config{
		MobileLifecycle:  true,
		MobileSleepAfter: 5 * time.Minute,
		StaleAfterStatic: 5 * time.Minute,
		ScanEvery:        time.Hour,
	})
	w.SetSleepNotifier(sleep)

	driveScan(w, now)

	if got := len(off.snapshot()); got != 1 {
		t.Fatalf("static must hit SendOffline once; got %d", got)
	}
	if got := len(sleep.snapshot()); got != 0 {
		t.Fatalf("static must NOT hit SendSleeping; got %d", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/backend/heartbeat/ -v
```
Expected: FAIL — `SetSleepNotifier` undefined, `MobileLifecycle` branch absent.

- [ ] **Step 3: Define `SleepNotifier` interface + `sleepNotified` map + `SetSleepNotifier`**

In `internal/backend/heartbeat/watcher.go`, after the `OfflineSender` interface (around line 13), add:

```go
// SleepNotifier is called once when a mobile user crosses MobileSleepAfter
// without heartbeats and MobileLifecycle is enabled. Unlike OfflineSender,
// it must NOT renotify — the watcher self-dedupes via sleepNotified.
type SleepNotifier interface {
	SendSleeping(ctx context.Context, userID int64, nickname string, lastSeen time.Time) error
}
```

Modify `Watcher` struct (around line 75) to add `sleep` field and `sleepNotified` map:

```go
type Watcher struct {
	d              *db.DB
	off            OfflineSender
	sleep          SleepNotifier
	cfg            Config
	notified       map[int64]time.Time
	sleepNotified  map[int64]time.Time
	resumed        map[int64]time.Time
	mu             sync.Mutex
	wg             sync.WaitGroup
	now            func() time.Time
}
```

In `NewWatcher`, initialise the new map:

```go
	return &Watcher{
		d: d, off: off, cfg: cfg,
		notified:      map[int64]time.Time{},
		sleepNotified: map[int64]time.Time{},
		resumed:       map[int64]time.Time{},
		now:           time.Now,
	}
```

After `NewWatcher`, add the setter and a test-only scan exposer:

```go
// SetSleepNotifier wires the mobile-lifecycle one-shot sleep notifier. Must
// be called BEFORE Run. Nil disables the sleep-info path even if MobileLifecycle
// is true; the watcher then silently drops mobile staleness in that mode
// (acceptable: static users still get full coverage).
func (w *Watcher) SetSleepNotifier(s SleepNotifier) {
	w.sleep = s
}

// ScanForTest exposes scan() to integration tests in sibling packages
// (cmd/backend/...). Not used by production code.
func (w *Watcher) ScanForTest(ctx context.Context) { w.scan(ctx) }
```

- [ ] **Step 4: Branch `scan()` to handle mobile-lifecycle separately**

In `internal/backend/heartbeat/watcher.go::scan`, replace the body of the staleness branch (the part after `if stale < threshold { ... continue }`, around line 202-224) with:

```go
		now := w.now()
		stale := now.Sub(latest)
		threshold := w.cfg.staleFor(u)
		if stale < threshold {
			w.mu.Lock()
			delete(w.notified, u.ID)
			delete(w.sleepNotified, u.ID)
			if rt, ok := w.resumed[u.ID]; ok && now.Sub(rt) >= w.cfg.ResumeGrace {
				delete(w.resumed, u.ID)
			}
			w.mu.Unlock()
			continue
		}
		w.mu.Lock()
		if rt, ok := w.resumed[u.ID]; ok {
			if now.Sub(rt) < w.cfg.ResumeGrace {
				w.mu.Unlock()
				continue
			}
			delete(w.resumed, u.ID)
		}
		w.mu.Unlock()

		// Mobile lifecycle: one-shot sleep info, no HARD, no renotify.
		if u.IsMobile() && w.cfg.MobileLifecycle {
			w.mu.Lock()
			_, already := w.sleepNotified[u.ID]
			if !already {
				w.sleepNotified[u.ID] = now
			}
			w.mu.Unlock()
			if already || w.sleep == nil {
				continue
			}
			if err := w.sleep.SendSleeping(ctx, u.ID, u.Nickname, latest); err != nil {
				slog.Warn("heartbeat: send sleeping failed", "user_id", u.ID, "nickname", u.Nickname, "err", err)
				// Reset so a future scan retries the one-shot.
				w.mu.Lock()
				delete(w.sleepNotified, u.ID)
				w.mu.Unlock()
			}
			continue
		}

		// Legacy HARD-OFFLINE path (static + mobile with MobileLifecycle=false).
		w.mu.Lock()
		last, sent := w.notified[u.ID]
		notify := !sent || now.Sub(last) > w.cfg.RenotifyEvery
		if notify {
			w.notified[u.ID] = now
		}
		w.mu.Unlock()
		if !notify {
			continue
		}
		if err := w.off.SendOffline(ctx, u.ID, u.Nickname, stale); err != nil {
			slog.Warn("heartbeat: send offline failed", "user_id", u.ID, "nickname", u.Nickname, "err", err)
		}
```

- [ ] **Step 5: Run watcher tests to verify all pass**

```
go test ./internal/backend/heartbeat/ -v
```
Expected: PASS for all (existing + 5 new + 2 legacy with MobileLifecycle:false).

- [ ] **Step 6: Commit**

```
git add internal/backend/heartbeat/watcher.go internal/backend/heartbeat/watcher_test.go
git commit -m "feat(heartbeat): mobile-lifecycle one-shot SleepNotifier branch in scan"
```

---

## Task 4: Wake-report renderer

**Files:**
- Create: `internal/backend/alerts/wake_report.go`
- Create: `internal/backend/alerts/wake_report_test.go`

- [ ] **Step 1: Write failing renderer tests**

Create `internal/backend/alerts/wake_report_test.go`:

```go
package alerts

import (
	"strings"
	"testing"

	"github.com/anex/wg-monitor/pkg/wire"
)

func TestRenderWakeReport_AllOk_SingleLineCard(t *testing.T) {
	checks := []wire.Check{
		{Name: "tunnels", Status: "ok"},
		{Name: "dns_via_tunnel", Status: "ok"},
		{Name: "agent_heartbeat", Status: "ok"},
	}
	card := RenderWakeReport("carvan", checks)
	if card.Badge != "🚗" {
		t.Errorf("badge: want 🚗, got %q", card.Badge)
	}
	if !strings.Contains(card.Summary, "carvan") {
		t.Errorf("summary missing nick: %q", card.Summary)
	}
	if !strings.Contains(card.Summary, "всё ок") {
		t.Errorf("summary missing OK marker: %q", card.Summary)
	}
	if card.Details != "" {
		t.Errorf("all-ok must omit details, got %q", card.Details)
	}
}

func TestRenderWakeReport_WithFailures_BulletDetails(t *testing.T) {
	checks := []wire.Check{
		{Name: "tunnels", Status: "fail"},
		{Name: "dns_via_tunnel", Status: "fail"},
		{Name: "external_reach", Status: "ok"},
		{Name: "agent_heartbeat", Status: "ok"},
	}
	card := RenderWakeReport("carvan", checks)
	if card.Badge != "🚗⚠" {
		t.Errorf("badge: want 🚗⚠, got %q", card.Badge)
	}
	if !strings.Contains(card.Summary, "проблемы") {
		t.Errorf("summary must mention проблемы, got %q", card.Summary)
	}
	if !strings.Contains(card.Details, "tunnels") || !strings.Contains(card.Details, "dns_via_tunnel") {
		t.Errorf("details must list failing checks, got %q", card.Details)
	}
	if strings.Contains(card.Details, "external_reach") {
		t.Errorf("details must NOT list ok checks, got %q", card.Details)
	}
}

func TestRenderWakeReport_SkipsAgentHeartbeat(t *testing.T) {
	checks := []wire.Check{
		{Name: "agent_heartbeat", Status: "fail"}, // pathological but should be ignored
	}
	card := RenderWakeReport("carvan", checks)
	if card.Badge != "🚗" {
		t.Errorf("agent_heartbeat fail must not flip badge; got %q", card.Badge)
	}
	if card.Details != "" {
		t.Errorf("agent_heartbeat fail must not enter details; got %q", card.Details)
	}
}
```

- [ ] **Step 2: Run to verify fail**

```
go test ./internal/backend/alerts/ -run TestRenderWakeReport -v
```
Expected: FAIL — `RenderWakeReport` undefined.

- [ ] **Step 3: Implement renderer**

Create `internal/backend/alerts/wake_report.go`:

```go
package alerts

import (
	"fmt"
	"strings"

	"github.com/anex/wg-monitor/pkg/wire"
)

// RenderWakeReport produces the adaptive wake-card for a mobile router that
// just rejoined (Report.Resumed=true). All checks green → one-line "🚗 в
// сети — всё ок". Any failures → "🚗⚠ есть проблемы" with bullet list of
// failing check names. agent_heartbeat is always excluded from the failure
// tally — it's a transport check, not a router-health signal.
func RenderWakeReport(nickname string, checks []wire.Check) Card {
	var failed []string
	for _, c := range checks {
		if c.Name == "agent_heartbeat" {
			continue
		}
		if c.Status != "ok" {
			failed = append(failed, c.Name)
		}
	}
	if len(failed) == 0 {
		return Card{
			Badge:   "🚗",
			Summary: fmt.Sprintf("%s в сети — всё ок", nickname),
		}
	}
	var b strings.Builder
	for i, name := range failed {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "• %s", name)
	}
	return Card{
		Badge:   "🚗⚠",
		Summary: fmt.Sprintf("%s в сети, есть проблемы", nickname),
		Details: b.String(),
		Hint:    "Открой /panel или нажми 📊 Что происходит?",
	}
}
```

- [ ] **Step 4: Run tests to verify pass**

```
go test ./internal/backend/alerts/ -run TestRenderWakeReport -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/backend/alerts/wake_report.go internal/backend/alerts/wake_report_test.go
git commit -m "feat(alerts): RenderWakeReport adaptive card for mobile rejoin"
```

---

## Task 5: Sleep-info renderer

**Files:**
- Create: `internal/backend/alerts/sleep_info.go`
- Create: `internal/backend/alerts/sleep_info_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/backend/alerts/sleep_info_test.go`:

```go
package alerts

import (
	"strings"
	"testing"
	"time"
)

func TestRenderSleepInfo(t *testing.T) {
	last := time.Date(2026, 5, 15, 14, 32, 7, 0, time.UTC)
	card := RenderSleepInfo("carvan", last)
	if card.Badge != "🌙" {
		t.Errorf("badge: want 🌙, got %q", card.Badge)
	}
	if !strings.Contains(card.Summary, "carvan") {
		t.Errorf("summary missing nick: %q", card.Summary)
	}
	if !strings.Contains(card.Summary, "14:32") {
		t.Errorf("summary must include HH:MM of lastSeen, got %q", card.Summary)
	}
	if card.Details != "" || card.Hint != "" {
		t.Errorf("sleep info must be one-liner; details=%q hint=%q", card.Details, card.Hint)
	}
}
```

- [ ] **Step 2: Run to verify fail**

```
go test ./internal/backend/alerts/ -run TestRenderSleepInfo -v
```
Expected: FAIL.

- [ ] **Step 3: Implement renderer**

Create `internal/backend/alerts/sleep_info.go`:

```go
package alerts

import (
	"fmt"
	"time"
)

// RenderSleepInfo produces the one-shot "🌙 router went offline" info-card
// emitted by the heartbeat watcher when a mobile-lifecycle user crosses
// MobileSleepAfter without heartbeats. Local-time HH:MM only — the topic
// already carries the date context for the operator.
func RenderSleepInfo(nickname string, lastSeen time.Time) Card {
	return Card{
		Badge:   "🌙",
		Summary: fmt.Sprintf("%s вышел из сети (последний heartbeat %s)", nickname, lastSeen.Local().Format("15:04")),
	}
}
```

- [ ] **Step 4: Run to verify pass**

```
go test ./internal/backend/alerts/ -run TestRenderSleepInfo -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```
git add internal/backend/alerts/sleep_info.go internal/backend/alerts/sleep_info_test.go
git commit -m "feat(alerts): RenderSleepInfo one-shot mobile sleep-card"
```

---

## Task 6: Lifecycle notifier — Wake/Sleep TG senders

**Files:**
- Create: `internal/backend/alerts/lifecycle_notifier.go`
- Create: `internal/backend/alerts/lifecycle_notifier_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/backend/alerts/lifecycle_notifier_test.go`:

```go
package alerts

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/pkg/wire"
)

type fakeSendTG struct {
	mu       sync.Mutex
	chatID   int64
	threadID *int64
	text     string
}

func (f *fakeSendTG) SendMessage(_ context.Context, chatID int64, threadID *int64, text, _ string, _ *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chatID = chatID
	f.threadID = threadID
	f.text = text
	return 100, nil
}

func TestWakeNotifier_SendsToRouterTopic(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1100110011001100110011001100110011001100110011001100110011001100"
	uid, _ := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)
	if err := d.Users().UpdateThreadID(uid, 555); err != nil {
		t.Fatal(err)
	}

	tg := &fakeSendTG{}
	wn := NewWakeNotifier(d, tg, -100)
	checks := []wire.Check{{Name: "tunnels", Status: "ok"}}
	if err := wn.SendWake(context.Background(), uid, "carvan", checks); err != nil {
		t.Fatal(err)
	}
	if tg.chatID != -100 {
		t.Errorf("chatID: want -100, got %d", tg.chatID)
	}
	if tg.threadID == nil || *tg.threadID != 555 {
		t.Errorf("threadID: want 555, got %v", tg.threadID)
	}
	if !strings.Contains(tg.text, "🚗") || !strings.Contains(tg.text, "carvan") {
		t.Errorf("text missing wake markers: %q", tg.text)
	}
}

func TestSleepNotifier_SendsToRouterTopic(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "2200220022002200220022002200220022002200220022002200220022002200"
	uid, _ := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)
	d.Users().UpdateThreadID(uid, 777)

	tg := &fakeSendTG{}
	sn := NewSleepNotifier(d, tg, -200)
	when := time.Date(2026, 5, 15, 14, 32, 0, 0, time.Local)
	if err := sn.SendSleeping(context.Background(), uid, "carvan", when); err != nil {
		t.Fatal(err)
	}
	if tg.chatID != -200 {
		t.Errorf("chatID: want -200, got %d", tg.chatID)
	}
	if tg.threadID == nil || *tg.threadID != 777 {
		t.Errorf("threadID: want 777, got %v", tg.threadID)
	}
	if !strings.Contains(tg.text, "🌙") || !strings.Contains(tg.text, "carvan") {
		t.Errorf("text missing sleep markers: %q", tg.text)
	}
}

func TestWakeNotifier_NoThreadID_SkipsSend(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "3300330033003300330033003300330033003300330033003300330033003300"
	uid, _ := d.Users().InsertWithKind("orphan", tok, "1.1.1.1", "nwg0", db.KindMobile)
	// no UpdateThreadID — TelegramThreadID stays NULL

	tg := &fakeSendTG{}
	wn := NewWakeNotifier(d, tg, -100)
	if err := wn.SendWake(context.Background(), uid, "orphan", nil); err != nil {
		t.Fatal(err)
	}
	if tg.text != "" {
		t.Errorf("send must be skipped when topic missing; sent %q", tg.text)
	}
}
```

- [ ] **Step 2: Run to verify fail**

```
go test ./internal/backend/alerts/ -run "WakeNotifier|SleepNotifier" -v
```
Expected: FAIL — `NewWakeNotifier`, `NewSleepNotifier` undefined.

- [ ] **Step 3: Implement notifiers**

Create `internal/backend/alerts/lifecycle_notifier.go`:

```go
package alerts

import (
	"context"
	"log/slog"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/pkg/wire"
)

// LifecycleSendTG is the minimal tg.Client surface used by the wake/sleep
// notifiers. Decoupled so tests can fake it.
type LifecycleSendTG interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
}

// WakeNotifier sends an adaptive 🚗 card to the router's TG-topic when a
// mobile agent's Report carries Resumed=true.
type WakeNotifier struct {
	db     *db.DB
	tg     LifecycleSendTG
	chatID int64
}

func NewWakeNotifier(d *db.DB, tg LifecycleSendTG, chatID int64) *WakeNotifier {
	return &WakeNotifier{db: d, tg: tg, chatID: chatID}
}

func (n *WakeNotifier) SendWake(ctx context.Context, userID int64, nickname string, checks []wire.Check) error {
	user, err := n.db.Users().GetByID(userID)
	if err != nil || user == nil {
		slog.Warn("wake notifier: user lookup", "user_id", userID, "err", err)
		return nil
	}
	if user.TelegramThreadID == nil {
		slog.Debug("wake notifier: no thread, skipping", "user_id", userID, "nickname", nickname)
		return nil
	}
	card := RenderWakeReport(nickname, checks)
	text := card.Render(CardOpts{MaxBytes: 3500})
	_, err = n.tg.SendMessage(ctx, n.chatID, user.TelegramThreadID, text, "", nil)
	if err != nil {
		slog.Warn("wake notifier: send failed", "user_id", userID, "nickname", nickname, "err", err)
	}
	return err
}

// SleepNotifier sends a one-shot 🌙 info-card to the router's TG-topic when
// the heartbeat watcher detects MobileSleepAfter silence on a mobile-lifecycle
// user.
type SleepNotifier struct {
	db     *db.DB
	tg     LifecycleSendTG
	chatID int64
}

func NewSleepNotifier(d *db.DB, tg LifecycleSendTG, chatID int64) *SleepNotifier {
	return &SleepNotifier{db: d, tg: tg, chatID: chatID}
}

func (n *SleepNotifier) SendSleeping(ctx context.Context, userID int64, nickname string, lastSeen time.Time) error {
	user, err := n.db.Users().GetByID(userID)
	if err != nil || user == nil {
		slog.Warn("sleep notifier: user lookup", "user_id", userID, "err", err)
		return nil
	}
	if user.TelegramThreadID == nil {
		slog.Debug("sleep notifier: no thread, skipping", "user_id", userID, "nickname", nickname)
		return nil
	}
	card := RenderSleepInfo(nickname, lastSeen)
	text := card.Render(CardOpts{MaxBytes: 800})
	_, err = n.tg.SendMessage(ctx, n.chatID, user.TelegramThreadID, text, "", nil)
	if err != nil {
		slog.Warn("sleep notifier: send failed", "user_id", userID, "nickname", nickname, "err", err)
	}
	return err
}
```

- [ ] **Step 4: Run tests to verify pass**

```
go test ./internal/backend/alerts/ -v
```
Expected: PASS for all tests in the package.

- [ ] **Step 5: Commit**

```
git add internal/backend/alerts/lifecycle_notifier.go internal/backend/alerts/lifecycle_notifier_test.go
git commit -m "feat(alerts): WakeNotifier + SleepNotifier (TG senders for mobile lifecycle)"
```

---

## Task 7: Handler — `WakeNotifier` in Deps + hook

**Files:**
- Modify: `internal/backend/handler.go`
- Modify: `internal/backend/handler_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/backend/handler_test.go`. (You may need to import `github.com/anex/wg-monitor/pkg/wire` if not already imported.)

```go
type fakeWakeNotifier struct {
	mu     sync.Mutex
	calls  []wakeRec
}

type wakeRec struct {
	userID   int64
	nickname string
	checks   []wire.Check
}

func (f *fakeWakeNotifier) SendWake(_ context.Context, uid int64, nick string, checks []wire.Check) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, wakeRec{uid, nick, append([]wire.Check(nil), checks...)})
	return nil
}

func (f *fakeWakeNotifier) snapshot() []wakeRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]wakeRec, len(f.calls))
	copy(out, f.calls)
	return out
}

func TestHandleReport_MobileResumed_TriggersWakeCard(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "h.db"))
	defer d.Close()
	tok := "abab00abab00abab00abab00abab00abab00abab00abab00abab00abab00abab"
	uid, _ := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)

	wake := &fakeWakeNotifier{}
	disp := &fakeDispatcher{}
	resumer := &fakeResumer{}
	h := NewMux(Deps{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:           d,
		Dispatcher:   disp,
		Resumer:      resumer,
		WakeNotifier: wake,
	})

	body := []byte(`{"ts":"2026-05-15T14:30:00Z","agent_version":"t","resumed":true,"checks":[{"name":"tunnels","status":"ok"}]}`)
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}

	// SendWake is fired in a goroutine; give it a moment.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(wake.snapshot()) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := wake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("want 1 wake call, got %d", len(calls))
	}
	if calls[0].userID != uid || calls[0].nickname != "carvan" {
		t.Errorf("call mismatch: %+v", calls[0])
	}
}

func TestHandleReport_StaticResumed_NoWakeCard(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "h.db"))
	defer d.Close()
	tok := "bcbc00bcbc00bcbc00bcbc00bcbc00bcbc00bcbc00bcbc00bcbc00bcbc00bcbc"
	d.Users().Insert("homestat", tok, "1.1.1.1", "awg0")

	wake := &fakeWakeNotifier{}
	h := NewMux(Deps{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:           d,
		Dispatcher:   &fakeDispatcher{},
		Resumer:      &fakeResumer{},
		WakeNotifier: wake,
	})
	body := []byte(`{"ts":"2026-05-15T14:30:00Z","agent_version":"t","resumed":true,"checks":[]}`)
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	time.Sleep(150 * time.Millisecond) // wake-fire would have fired by now
	if got := len(wake.snapshot()); got != 0 {
		t.Errorf("static user must not fire wake card, got %d", got)
	}
}

func TestHandleReport_MobileNotResumed_NoWakeCard(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "h.db"))
	defer d.Close()
	tok := "cdcd00cdcd00cdcd00cdcd00cdcd00cdcd00cdcd00cdcd00cdcd00cdcd00cdcd"
	d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)

	wake := &fakeWakeNotifier{}
	h := NewMux(Deps{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:           d,
		Dispatcher:   &fakeDispatcher{},
		Resumer:      &fakeResumer{},
		WakeNotifier: wake,
	})
	body := []byte(`{"ts":"2026-05-15T14:30:00Z","agent_version":"t","resumed":false,"checks":[]}`)
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	time.Sleep(150 * time.Millisecond)
	if got := len(wake.snapshot()); got != 0 {
		t.Errorf("non-resumed mobile must not fire wake card, got %d", got)
	}
}
```

Imports likely needed (add to existing import block if absent): `"bytes"`, `"io"`, `"net/http"`, `"net/http/httptest"`, `"time"`, `"path/filepath"`, `"log/slog"`, `"github.com/anex/wg-monitor/pkg/wire"`, `"github.com/anex/wg-monitor/internal/backend/db"`.

(`fakeDispatcher` already exists in `handler_test.go` — reuse it. If it requires a constructor, follow the pattern at the top of the existing file.)

- [ ] **Step 2: Run to verify failure**

```
go test ./internal/backend/ -run "MobileResumed|StaticResumed|MobileNotResumed" -v
```
Expected: FAIL — `Deps.WakeNotifier` undefined.

- [ ] **Step 3: Add WakeNotifier interface + Deps field**

In `internal/backend/handler.go`, after the `Resumer` interface (around line 134), add:

```go
// WakeNotifier is fired from /v1/report when an agent's Report carries
// Resumed=true AND the user is kind=mobile. It renders the adaptive 🚗
// card and posts to the router's TG-topic. nil-safe (handler skips).
type WakeNotifier interface {
	SendWake(ctx context.Context, userID int64, nickname string, checks []wire.Check) error
}
```

In `type Deps struct` (around line 180), add after `Resumer`:

```go
	WakeNotifier      WakeNotifier      // nil-safe (handler skips if nil or user is static)
```

- [ ] **Step 4: Wire hook in `/v1/report` handler**

In `internal/backend/handler.go`, find the block (around line 296-298):

```go
		if rep.Resumed && d.Resumer != nil {
			d.Resumer.MarkResumed(uid)
		}
```

Replace with:

```go
		if rep.Resumed {
			if d.Resumer != nil {
				d.Resumer.MarkResumed(uid)
			}
			if d.WakeNotifier != nil {
				if u, err := d.DB.Users().GetByID(uid); err == nil && u != nil && u.IsMobile() {
					checks := append([]wire.Check(nil), rep.Checks...)
					nickname := nick
					go func() {
						bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						if err := d.WakeNotifier.SendWake(bg, uid, nickname, checks); err != nil {
							d.Logger.Warn("wake notifier", "nickname", nickname, "err", err)
						}
					}()
				}
			}
		}
```

(`wire` is already imported. `context`, `time` already imported.)

- [ ] **Step 5: Run tests to verify pass**

```
go test ./internal/backend/ -v
```
Expected: PASS for the three new tests + all existing handler tests.

- [ ] **Step 6: Commit**

```
git add internal/backend/handler.go internal/backend/handler_test.go
git commit -m "feat(handler): trigger WakeNotifier on Resumed=true for mobile users"
```

---

## Task 8: Wiring in `cmd/backend/main.go` + template

**Files:**
- Modify: `cmd/backend/main.go`
- Modify: `cmd/deploy/templates/backend.yaml.tmpl`

- [ ] **Step 1: Add yaml keys to template**

In `cmd/deploy/templates/backend.yaml.tmpl`, locate the `heartbeat:` block and replace with:

```yaml
heartbeat:
  stale_after_sec: 120
  stale_after_static_sec: 180
  stale_after_mobile_sec: 3600   # legacy HARD-OFFLINE threshold; applied only if mobile_lifecycle=false
  mobile_lifecycle: true         # wake-card (🚗) on Resumed=true + one-shot sleep-info (🌙) after mobile_sleep_after_sec
  mobile_sleep_after_sec: 300    # mobile silence threshold for the one-shot sleep info-card
  resume_grace_sec: 30
  scan_interval_sec: 30
```

- [ ] **Step 2: Wire watcher Config fields in `cmd/backend/main.go`**

Locate the `watcher := heartbeat.NewWatcher(...)` block (around line 82). Replace with:

```go
	mobileLifecycle := cfg.Heartbeat.MobileLifecycle == nil || *cfg.Heartbeat.MobileLifecycle
	watcher := heartbeat.NewWatcher(d, disp, heartbeat.Config{
		StaleAfter:       time.Duration(cfg.Heartbeat.StaleAfterSec) * time.Second,
		StaleAfterStatic: time.Duration(cfg.Heartbeat.StaleAfterStaticSec) * time.Second,
		StaleAfterMobile: time.Duration(cfg.Heartbeat.StaleAfterMobileSec) * time.Second,
		MobileSleepAfter: time.Duration(cfg.Heartbeat.MobileSleepAfterSec) * time.Second,
		MobileLifecycle:  mobileLifecycle,
		ResumeGrace:      time.Duration(cfg.Heartbeat.ResumeGraceSec) * time.Second,
		ScanEvery:        time.Duration(cfg.Heartbeat.ScanIntervalSec) * time.Second,
		RenotifyEvery:    time.Duration(cfg.State.RealertEverySec) * time.Second,
	})
```

- [ ] **Step 3: Build + wire WakeNotifier / SleepNotifier**

Still in `cmd/backend/main.go`, immediately after the `watcher := ...` block, add:

```go
	// Mobile-lifecycle notifiers: wake-card on Resumed=true, one-shot sleep-info
	// after MobileSleepAfter silence. Both no-op for static users / when
	// telegram_thread_id NULL.
	wakeNotifier := alerts.NewWakeNotifier(d, tgClient, cfg.Telegram.ChatID)
	sleepNotifier := alerts.NewSleepNotifier(d, tgClient, cfg.Telegram.ChatID)
	watcher.SetSleepNotifier(sleepNotifier)
```

- [ ] **Step 4: Pass WakeNotifier into Deps**

Locate the `Deps{...}` construction passed to `NewMux` (search for `mux := backend.NewMux(backend.Deps{` or similar). Add `WakeNotifier: wakeNotifier,` alongside the other Notifier fields.

- [ ] **Step 5: Run full build + unit tests**

```
go build ./...
go test ./...
```
Expected: PASS for all packages.

- [ ] **Step 6: Commit**

```
git add cmd/backend/main.go cmd/deploy/templates/backend.yaml.tmpl
git commit -m "feat(backend): wire WakeNotifier + SleepNotifier; template yaml keys"
```

---

## Task 9: Integration test

**Files:**
- Create: `cmd/backend/backend_mobile_lifecycle_integration_test.go`

- [ ] **Step 1: Write integration test**

Create `cmd/backend/backend_mobile_lifecycle_integration_test.go`:

```go
package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/alerts"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/heartbeat"
	"github.com/anex/wg-monitor/pkg/wire"
)

type captureTG struct {
	mu    sync.Mutex
	sends []capSend
}

type capSend struct {
	chatID   int64
	threadID *int64
	text     string
}

func (c *captureTG) SendMessage(_ context.Context, chatID int64, threadID *int64, text, _ string, _ *int64) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sends = append(c.sends, capSend{chatID, threadID, text})
	return int64(len(c.sends)), nil
}

func (c *captureTG) snapshot() []capSend {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]capSend, len(c.sends))
	copy(out, c.sends)
	return out
}

func TestIntegration_MobileLifecycle_WakeAndSleep(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "i.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tok := "ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00"
	uid, _ := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)
	if err := d.Users().UpdateThreadID(uid, 555); err != nil {
		t.Fatal(err)
	}

	cap := &captureTG{}
	const chatID = -100

	// Wire the wake notifier exactly as cmd/backend/main.go would.
	wake := alerts.NewWakeNotifier(d, cap, chatID)

	// Simulate the handler's hook: Resumed=true on a mobile user → SendWake.
	checks := []wire.Check{{Name: "tunnels", Status: "ok"}, {Name: "dns_via_tunnel", Status: "ok"}}
	if err := wake.SendWake(context.Background(), uid, "carvan", checks); err != nil {
		t.Fatal(err)
	}

	// Wire watcher + sleep notifier; trigger a stale scan.
	sleep := alerts.NewSleepNotifier(d, cap, chatID)
	w := heartbeat.NewWatcher(d, &noopOffline{}, heartbeat.Config{
		MobileLifecycle:  true,
		MobileSleepAfter: time.Second,
		ScanEvery:        time.Hour,
	})
	w.SetSleepNotifier(sleep)

	// Last heartbeat 2s ago — should trip MobileSleepAfter (1s).
	now := time.Now().UTC()
	d.Events().Insert(uid, "agent_heartbeat", "ok", "", now.Add(-2*time.Second))
	w.SetNow(func() time.Time { return now })
	w.ScanForTest(context.Background()) // tests call exported helper if scan is unexported

	sends := cap.snapshot()
	if len(sends) != 2 {
		t.Fatalf("want 2 sends (wake + sleep), got %d", len(sends))
	}
	if !strings.Contains(sends[0].text, "🚗") || !strings.Contains(sends[0].text, "всё ок") {
		t.Errorf("first send must be wake-card all-ok, got %q", sends[0].text)
	}
	if !strings.Contains(sends[1].text, "🌙") || !strings.Contains(sends[1].text, "carvan") {
		t.Errorf("second send must be sleep-info, got %q", sends[1].text)
	}
	for _, s := range sends {
		if s.threadID == nil || *s.threadID != 555 {
			t.Errorf("send threadID: want 555, got %v", s.threadID)
		}
		if s.chatID != chatID {
			t.Errorf("send chatID: want %d, got %d", chatID, s.chatID)
		}
	}
}

type noopOffline struct{}

func (noopOffline) SendOffline(_ context.Context, _ int64, _ string, _ time.Duration) error {
	return nil
}
```

`ScanForTest` was added during Task 3 (Step 3) — no separate step needed here.

- [ ] **Step 2: Run the integration test**

```
go test ./cmd/backend/ -run TestIntegration_MobileLifecycle_WakeAndSleep -v
```
Expected: PASS.

- [ ] **Step 3: Run full suite**

```
go build ./...
go test ./...
go vet ./...
```
Expected: PASS, clean vet.

- [ ] **Step 4: Commit**

```
git add cmd/backend/backend_mobile_lifecycle_integration_test.go
git commit -m "test(integration): mobile lifecycle wake + sleep roundtrip"
```

---

## Self-review checklist (executor)

Before declaring the plan done:

- [ ] All 9 tasks committed, branch builds clean (`go build ./...`).
- [ ] `go test ./...` green — 21+ packages, no flakes.
- [ ] `go vet ./...` clean.
- [ ] Spec section "Out-of-scope" was NOT implemented (no trip history, no GPIO, no per-user override).
- [ ] `backend.yaml.tmpl` has both `mobile_lifecycle` and `mobile_sleep_after_sec` lines.
- [ ] Manual smoke checklist (deferred to operator — do NOT execute as part of plan):
  - Mobile testkeen wakes after ignition → `🚗 testkeen в сети — всё ок` in router topic
  - Stop the agent → wait MobileSleepAfter → single `🌙 testkeen вышел из сети`
  - Restart agent → wake-card again, no duplicate sleep-info on next scan
  - Static router untouched (HARD-OFFLINE still works if you stop its agent)
