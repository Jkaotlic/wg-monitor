# Alerts Readability + Decode-fix + External-reach Marker — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Перестать орать в Telegram сырыми Go-ошибками и машинными именами. Починить root-cause бага с пустым `lastHandshake` от awg-manager (cryptic `parsing time ""` в чате), отрендерить synthetic `tunnels`-check человечески, убрать дубль строки в DNS-теле, добавить новый check «иностранные сервисы через WG» (YouTube/Telegram/Instagram).

**Architecture:** Три независимые фазы, каждая → отдельный коммит, безопасно деплоится отдельно.
- **Phase A** — agent-side: custom `nullableTime` для empty-string awg-manager polей.
- **Phase B** — backend-side: pretty labels + категория `awgmgr_api` + DNS-dedup в `format.go`.
- **Phase C** — agent+backend: новый `external_reach` check с параллельными HTTP-пробами через `defaultRoute=true` туннель.

**Tech Stack:** Go 1.22, `encoding/json` UnmarshalJSON, `net/http` с iface-bound dialer, `crypto/tls` через DefaultTransport, `gopkg.in/yaml.v3`.

---

## File Structure

**Modified:**
- `internal/agent/awgmgr/types.go` — поля `*time.Time` → `nullableTime` в `Tunnel.LastHandshake`, `Tunnel.StartedAt`, `PingCheckTunnel.LastCheck`.
- `internal/agent/checks/tunnels.go` — `evalTunnel` использует `.Time()` accessor.
- `internal/backend/alerts/format.go` — `prettyCheckLabel` расширен; `checkCategory` ловит `tunnels`; новый `writeAwgmgrAPIBody`; `writeDNSBody` без дубля.
- `cmd/agent/main.go` — регистрация `ExternalReachCheck`.
- `internal/agent/config.go` — новая структура `ExternalReachConfig`.

**Created:**
- `internal/agent/awgmgr/nullable_time.go` (+ `_test.go`) — custom JSON unmarshaller.
- `internal/agent/checks/external_reach.go` (+ `_test.go`) — multi-target HTTP probe.

---

## Phase A — awg-manager decode-fix

### Task 1: nullableTime — type + tests

**Files:**
- Create: `internal/agent/awgmgr/nullable_time.go`
- Test: `internal/agent/awgmgr/nullable_time_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/agent/awgmgr/nullable_time_test.go`:

```go
package awgmgr

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNullableTime_UnmarshalJSON(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantNil bool
		wantErr bool
	}{
		{"null literal", `null`, true, false},
		{"empty string", `""`, true, false},
		{"valid RFC3339", `"2026-05-05T13:23:45Z"`, false, false},
		{"valid RFC3339 with offset", `"2026-05-05T16:23:45+03:00"`, false, false},
		{"garbage", `"not a date"`, false, true},
		{"zero year", `"0001-01-01T00:00:00Z"`, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var nt nullableTime
			err := json.Unmarshal([]byte(c.in), &nt)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := nt.Time()
			if c.wantNil && got != nil {
				t.Fatalf("want nil, got %v", got)
			}
			if !c.wantNil && got == nil {
				t.Fatalf("want non-nil, got nil")
			}
		})
	}
}

func TestNullableTime_StructDecode(t *testing.T) {
	// Real awg-manager body shape: lastHandshake="" when never handshook.
	body := `{"lastHandshake":"","startedAt":"2026-05-05T10:00:00Z"}`
	var s struct {
		LastHandshake nullableTime `json:"lastHandshake"`
		StartedAt     nullableTime `json:"startedAt"`
	}
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.LastHandshake.Time() != nil {
		t.Fatalf("LastHandshake: want nil for empty string, got %v", s.LastHandshake.Time())
	}
	if s.StartedAt.Time() == nil {
		t.Fatalf("StartedAt: want non-nil")
	}
	want := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
	if !s.StartedAt.Time().Equal(want) {
		t.Fatalf("StartedAt: got %v, want %v", s.StartedAt.Time(), want)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd .worktrees/stage-2 && go test ./internal/agent/awgmgr/ -run TestNullableTime -v`
Expected: FAIL with "undefined: nullableTime"

- [ ] **Step 3: Implement nullableTime**

Create `internal/agent/awgmgr/nullable_time.go`:

```go
package awgmgr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// nullableTime parses awg-manager's RFC3339 time fields, accepting both
// JSON `null` and the empty string `""` as sentinels for "no value".
// awg-manager's /api/tunnels/all emits `"lastHandshake":""` for tunnels
// that have never handshaken, which would otherwise crash json.Unmarshal
// into *time.Time with `parsing time "" as "2006-01-02T..."`.
type nullableTime struct {
	t *time.Time
}

func (n *nullableTime) UnmarshalJSON(b []byte) error {
	if bytes.Equal(b, []byte("null")) {
		n.t = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("nullableTime: expected string or null, got %s: %w", string(b), err)
	}
	if s == "" {
		n.t = nil
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fmt.Errorf("nullableTime: parse %q: %w", s, err)
	}
	if parsed.Year() <= 1 {
		n.t = nil
		return nil
	}
	n.t = &parsed
	return nil
}

func (n nullableTime) MarshalJSON() ([]byte, error) {
	if n.t == nil {
		return []byte("null"), nil
	}
	return json.Marshal(n.t.UTC().Format(time.RFC3339))
}

// Time returns the underlying *time.Time (nil if the field was empty/null).
func (n nullableTime) Time() *time.Time { return n.t }
```

- [ ] **Step 4: Run to verify pass**

Run: `cd .worktrees/stage-2 && go test ./internal/agent/awgmgr/ -run TestNullableTime -v`
Expected: PASS, all 7 subcases green.

- [ ] **Step 5: Commit**

```bash
cd .worktrees/stage-2
git add internal/agent/awgmgr/nullable_time.go internal/agent/awgmgr/nullable_time_test.go
git commit -m "feat(awgmgr): nullableTime — accept empty string as nil"
```

---

### Task 2: Apply nullableTime to Tunnel + PingCheckTunnel

**Files:**
- Modify: `internal/agent/awgmgr/types.go:30,35,60`
- Modify: `internal/agent/checks/tunnels.go:90,94`

- [ ] **Step 1: Edit types.go**

Replace three field types (no other changes):

```
Line 30: LastHandshake        *time.Time     `json:"lastHandshake"`
   →     LastHandshake        nullableTime   `json:"lastHandshake"`

Line 35: StartedAt            *time.Time     `json:"startedAt"`
   →     StartedAt            nullableTime   `json:"startedAt"`

Line 60: LastCheck      *time.Time `json:"lastCheck"`
   →     LastCheck      nullableTime `json:"lastCheck"`
```

After all three edits, remove the `"time"` import if no other field uses it (run `go build` to check; in current code, default values still need `time`, so likely keep).

- [ ] **Step 2: Update tunnels.go evalTunnel**

In `internal/agent/checks/tunnels.go`, replace the `if tu.LastHandshake != nil { ... }` block (around line 90):

```go
if t := tu.LastHandshake.Time(); t != nil {
    details["last_handshake"] = t.UTC().Format(time.RFC3339)
    details["handshake_age_sec"] = int(time.Since(*t).Seconds())
}
if t := tu.StartedAt.Time(); t != nil {
    details["started_at"] = t.UTC().Format(time.RFC3339)
}
```

Also update `tunnelFailReasons` (around line 132):

```go
if t := tu.LastHandshake.Time(); t == nil {
    reasons = append(reasons, "no handshake ever")
} else if age := time.Since(*t); age > maxAge {
    reasons = append(reasons, fmt.Sprintf("handshake stale (%ds > %ds)", int(age.Seconds()), int(maxAge.Seconds())))
}
```

- [ ] **Step 3: Build & run all awgmgr/checks tests**

Run: `cd .worktrees/stage-2 && go build ./... && go test ./internal/agent/awgmgr/... ./internal/agent/checks/...`
Expected: PASS. Existing TunnelsCheck tests should still work (they pass `nil` for `LastHandshake`, which now becomes the zero value `nullableTime{}` — `.Time()` returns nil same as before).

If a test fails because it constructs `awgmgr.Tunnel{LastHandshake: &t}`, replace with `awgmgr.Tunnel{LastHandshake: nullableTime{t: &t}}` or add a constructor helper. Hunt them via grep.

- [ ] **Step 4: Add live-shape regression test**

Append to `internal/agent/awgmgr/client_test.go` (or create a new test if file lacks one) — a test that hits the unmarshaller with the exact body shape user reported in the screenshot:

```go
func TestTunnelsAll_EmptyLastHandshake(t *testing.T) {
	body := `{"success":true,"data":{"external":[],"system":[],"tunnels":[
		{"id":"awg11","name":"amnezia_for_awg","type":"awg","status":"running","enabled":true,
		 "defaultRoute":true,"resolvedIspInterface":"eth3","interfaceName":"nwg1","ndmsName":"Wireguard1",
		 "lastHandshake":"","startedAt":"","rxBytes":0,"txBytes":0}
	]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	c := New(srv.URL)
	got, err := c.TunnelsAll(context.Background())
	if err != nil {
		t.Fatalf("TunnelsAll: %v", err)
	}
	if len(got.Tunnels) != 1 {
		t.Fatalf("len(tunnels)=%d", len(got.Tunnels))
	}
	if got.Tunnels[0].LastHandshake.Time() != nil {
		t.Fatalf("LastHandshake: want nil for empty string")
	}
	if got.Tunnels[0].StartedAt.Time() != nil {
		t.Fatalf("StartedAt: want nil for empty string")
	}
}
```

(imports: `context`, `io`, `net/http`, `net/http/httptest`, `testing`)

Run: `cd .worktrees/stage-2 && go test ./internal/agent/awgmgr/ -run TestTunnelsAll_EmptyLastHandshake -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd .worktrees/stage-2
git add internal/agent/awgmgr/types.go internal/agent/awgmgr/client_test.go internal/agent/checks/tunnels.go
git commit -m "fix(awgmgr): empty lastHandshake no longer crashes JSON decode

awg-manager returns lastHandshake='' for tunnels that have never
handshaken, which made json.Unmarshal into *time.Time fail with a
cryptic 'parsing time \"\" as \"2006-01-02T...\"' error. Switch the
three time-pointer fields to nullableTime, which treats empty string
as nil. Fixes the [testkeen] tunnels — DOWN spam in admin chat."
```

---

## Phase B — Backend renderer cleanup

### Task 3: Pretty labels for top-level checks

**Files:**
- Modify: `internal/backend/alerts/format.go:171-185` (`prettyCheckLabel`)
- Test: `internal/backend/alerts/format_test.go` (extend)

- [ ] **Step 1: Write failing tests**

Append to `internal/backend/alerts/format_test.go`:

```go
func TestPrettyCheckLabel_TopLevel(t *testing.T) {
	cases := []struct {
		name    string
		check   string
		details map[string]any
		want    string
	}{
		{"dns", "dns", nil, "DNS"},
		{"hydraroute", "hydraroute", nil, "HydraRoute"},
		{"awg_manager", "awg_manager", nil, "awg-manager"},
		{"tunnels synthetic", "tunnels", nil, "awg-manager API"},
		{"external_reach", "external_reach", nil, "Иностранные сервисы"},
		{"tunnel_ with details", "tunnel_awg11",
			map[string]any{"tunnel_name": "amnezia_for_awg", "interface": "nwg1"},
			"amnezia_for_awg (nwg1)"},
		{"unknown falls through", "weird_check", nil, "weird_check"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := prettyCheckLabel(c.check, c.details)
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd .worktrees/stage-2 && go test ./internal/backend/alerts/ -run TestPrettyCheckLabel_TopLevel -v`
Expected: FAIL — current code returns raw name for everything except `tunnel_*`.

- [ ] **Step 3: Update prettyCheckLabel**

Replace the current `prettyCheckLabel` in `internal/backend/alerts/format.go` (lines 171-185):

```go
// prettyCheckLabel turns the agent's machine-readable check name into a
// label fit for Telegram. Tunnel rows are rendered from Details; all
// other categories use a fixed table.
func prettyCheckLabel(name string, details map[string]any) string {
	if checkCategory(name) == "tunnel" {
		tname, _ := details["tunnel_name"].(string)
		iface, _ := details["interface"].(string)
		switch {
		case tname != "" && iface != "":
			return fmt.Sprintf("%s (%s)", tname, iface)
		case tname != "":
			return tname
		case iface != "":
			return iface
		}
		return name
	}
	switch name {
	case "dns":
		return "DNS"
	case "hydraroute":
		return "HydraRoute"
	case "awg_manager":
		return "awg-manager"
	case "tunnels":
		return "awg-manager API"
	case "external_reach":
		return "Иностранные сервисы"
	}
	return name
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd .worktrees/stage-2 && go test ./internal/backend/alerts/ -run TestPrettyCheckLabel_TopLevel -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd .worktrees/stage-2
git add internal/backend/alerts/format.go internal/backend/alerts/format_test.go
git commit -m "feat(alerts): human-readable labels for top-level checks"
```

---

### Task 4: New `awgmgr_api` category + writer

**Files:**
- Modify: `internal/backend/alerts/format.go` — `checkCategory`, `FormatHard` switch, new `writeAwgmgrAPIBody`.
- Test: `internal/backend/alerts/format_test.go`.

- [ ] **Step 1: Write failing test**

Append to `internal/backend/alerts/format_test.go`:

```go
func TestFormatHard_TunnelsSynthetic(t *testing.T) {
	args := HardArgs{
		Nickname:    "testkeen",
		CheckName:   "tunnels",
		ConsecFails: 3,
		HardSince:   time.Date(2026, 5, 5, 10, 23, 0, 0, time.UTC),
		Check: wire.Check{
			Name:   "tunnels",
			Status: "fail",
			Details: map[string]any{
				"error": `awgmgr GET /api/tunnels/all: connection refused`,
			},
		},
	}
	got := FormatHard(args)
	// Must NOT contain raw decode dump or full URL noise.
	if strings.Contains(got, "decode:") {
		t.Errorf("decode: should not leak into rendered alert: %s", got)
	}
	// Must contain the human-friendly title.
	if !strings.Contains(got, "awg-manager API") {
		t.Errorf("missing pretty title: %s", got)
	}
	// Must contain the trimmed reason line.
	if !strings.Contains(got, "connection refused") {
		t.Errorf("missing reason: %s", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd .worktrees/stage-2 && go test ./internal/backend/alerts/ -run TestFormatHard_TunnelsSynthetic -v`
Expected: FAIL — currently routes to `writeGenericBody` which dumps the full error verbatim.

- [ ] **Step 3: Add category branch + writer**

In `internal/backend/alerts/format.go`:

Update `checkCategory` (line 157-169):

```go
func checkCategory(name string) string {
	switch {
	case strings.HasPrefix(name, "tunnel_"):
		return "tunnel"
	case name == "dns":
		return "dns"
	case name == "hydraroute":
		return "hydraroute"
	case name == "awg_manager":
		return "awg_manager"
	case name == "tunnels":
		return "awgmgr_api"
	case name == "external_reach":
		return "external_reach"
	}
	return "generic"
}
```

Update both switches in `FormatHard` and `FormatRealert` to add the new case:

```go
case "awgmgr_api":
    writeAwgmgrAPIBody(&b, a.Check.Details)   // or args.Check.Details in FormatRealert
case "external_reach":
    writeExternalReachBody(&b, a.Check.Details)
```

Add the new writer functions (place after `writeAwgManagerBody`):

```go
// writeAwgmgrAPIBody renders the synthetic `tunnels` check — it's about
// the awg-manager local API itself, not an individual tunnel.
func writeAwgmgrAPIBody(b *strings.Builder, d map[string]any) {
	if errStr := strOrEmpty(d, "error"); errStr != "" {
		// Surface the high-level failure mode, not the body dump. A
		// typical error looks like "awgmgr GET /api/tunnels/all: ..." —
		// keep the URL prefix (it tells the operator which endpoint is
		// down) but truncate any body-trailing JSON, since users have
		// reported it as unreadable noise.
		fmt.Fprintf(b, "❓ %s\n", trimBodyDump(errStr))
	}
	if cnt, ok := intOrZero(d, "tunnel_count"); ok && cnt > 0 {
		fmt.Fprintf(b, "📊 туннелей видно: %d\n", cnt)
	}
}

// writeExternalReachBody renders the multi-target external reachability
// probe (YouTube/Telegram/Instagram). See checks.ExternalReachCheck.
func writeExternalReachBody(b *strings.Builder, d map[string]any) {
	failed := strSliceOfMaps(d, "targets_failed")
	okList := strSlice(d, "targets_ok")
	total, _ := intOrZero(d, "targets_total")
	if total > 0 {
		fmt.Fprintf(b, "🎯 целей: %d · недоступно: %d\n", total, len(failed))
	}
	for _, t := range failed {
		name, _ := t["name"].(string)
		errStr, _ := t["err"].(string)
		fmt.Fprintf(b, "  ✗ %s — %s\n", name, errStr)
	}
	if len(okList) > 0 {
		fmt.Fprintf(b, "  ✓ работает: %s\n", strings.Join(okList, ", "))
	}
	if iface, _ := d["via_interface"].(string); iface != "" {
		fmt.Fprintf(b, "🛣 через %s\n", iface)
	}
}

// trimBodyDump strips a trailing "(body=…)" segment from awgmgr error
// messages — those segments are useful in agent logs but are visual
// noise in Telegram alerts.
func trimBodyDump(s string) string {
	if i := strings.Index(s, " (body="); i >= 0 {
		return s[:i]
	}
	return s
}

// strSlice returns a []string from a `[]any` or `[]string` map value.
func strSlice(d map[string]any, key string) []string {
	v, ok := d[key]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// strSliceOfMaps returns a []map[string]any from a `[]any` of maps —
// the shape JSON unmarshal produces for "[]struct{...}" payloads.
func strSliceOfMaps(d map[string]any, key string) []map[string]any {
	v, ok := d[key]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case []map[string]any:
		return x
	case []any:
		out := make([]map[string]any, 0, len(x))
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd .worktrees/stage-2 && go test ./internal/backend/alerts/ -v`
Expected: All tests PASS, including new `TestFormatHard_TunnelsSynthetic`.

- [ ] **Step 5: Commit**

```bash
cd .worktrees/stage-2
git add internal/backend/alerts/format.go internal/backend/alerts/format_test.go
git commit -m "feat(alerts): tunnels-synthetic + external_reach categories

The synthetic 'tunnels' check (awg-manager API health) used to fall
through to the generic writer that dumps the entire error verbatim.
Operators reported this as unreadable. Add a dedicated awgmgr_api
category that surfaces only the high-level reason and trims any
trailing '(body=…)' JSON dump.

Also stub out external_reach renderer for the upcoming
YouTube/Telegram/Instagram probe (Phase C)."
```

---

### Task 5: Drop duplicated `❓ <error>` in DNS body

**Files:**
- Modify: `internal/backend/alerts/format.go:246-267` (`writeDNSBody`).
- Test: `internal/backend/alerts/format_test.go`.

- [ ] **Step 1: Write failing test**

Append to `internal/backend/alerts/format_test.go`:

```go
func TestWriteDNSBody_NoDuplicateUnreachable(t *testing.T) {
	d := map[string]any{
		"endpoints":    5,
		"failed_count": 2,
		"rkn_probed":   3,
		"rkn_suspect":  0,
		"error":        "2/5 endpoints unreachable",
	}
	var b strings.Builder
	writeDNSBody(&b, d)
	got := b.String()
	// Expected: ONE mention of the failure count, not two.
	count := strings.Count(got, "2 unreachable") + strings.Count(got, "2/5 endpoints unreachable")
	if count > 1 {
		t.Errorf("duplicate unreachable line: %q", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd .worktrees/stage-2 && go test ./internal/backend/alerts/ -run TestWriteDNSBody_NoDuplicateUnreachable -v`
Expected: FAIL — body emits both `5 total · 2 unreachable` AND `❓ 2/5 endpoints unreachable`.

- [ ] **Step 3: Edit writeDNSBody**

In `internal/backend/alerts/format.go`, drop the trailing `❓ <error>` line — it duplicates info we already rendered as metrics. Keep error rendering ONLY when the error doesn't echo metric content (rare):

```go
func writeDNSBody(b *strings.Builder, d map[string]any) {
	total, _ := intOrZero(d, "endpoints")
	failed, _ := intOrZero(d, "failed_count")
	rknSus, _ := intOrZero(d, "rkn_suspect")
	rknProbed, _ := intOrZero(d, "rkn_probed")
	if total > 0 {
		fmt.Fprintf(b, "🌐 endpoints: %d total · %d unreachable\n", total, failed)
	}
	if rknProbed > 0 {
		switch {
		case rknSus == 0:
			fmt.Fprintf(b, "🚫 RKN probe: ✅ clean (%d probed)\n", rknProbed)
		case rknSus == rknProbed:
			b.WriteString("🚫 RKN probe: ⚠ suspected block on every endpoint\n")
		default:
			fmt.Fprintf(b, "🚫 RKN probe: ⚠ %d/%d suspect\n", rknSus, rknProbed)
		}
	}
	// Surface per-endpoint failures so the operator sees WHICH resolver is dead.
	if failed > 0 {
		for _, ep := range strSliceOfMaps(d, "endpoints_detail") {
			reachable, _ := ep["reachable"].(bool)
			if reachable {
				continue
			}
			tp, _ := ep["type"].(string)
			tg, _ := ep["target"].(string)
			ndms, _ := ep["ndms_name"].(string)
			errStr, _ := ep["err"].(string)
			label := tg
			if ndms != "" {
				label = fmt.Sprintf("%s (%s)", tg, ndms)
			}
			if tp != "" {
				label = tp + " " + label
			}
			fmt.Fprintf(b, "  ✗ %s — %s\n", label, trimErr(errStr))
		}
	}
}

// trimErr crops over-long network errors so the alert stays under TG's
// 4096-byte limit when many endpoints fail at once.
func trimErr(s string) string {
	const max = 80
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
```

- [ ] **Step 4: Run all alerts tests**

Run: `cd .worktrees/stage-2 && go test ./internal/backend/alerts/ -v`
Expected: PASS, including `TestWriteDNSBody_NoDuplicateUnreachable` and any pre-existing DNS body tests (which may need their expected strings updated — fix in place).

- [ ] **Step 5: Commit**

```bash
cd .worktrees/stage-2
git add internal/backend/alerts/format.go internal/backend/alerts/format_test.go
git commit -m "fix(alerts): drop duplicate unreachable line in DNS body

writeDNSBody used to emit '5 total · 2 unreachable' and then echo the
fail reason '2/5 endpoints unreachable' as a free-form '❓ <error>'.
The reason was already in the metric line. Replace the redundant echo
with per-endpoint detail rows so operators see WHICH resolver died."
```

---

## Phase C — External reach (YouTube / Telegram / Instagram)

### Task 6: ExternalReachCheck — type & probe loop

**Files:**
- Create: `internal/agent/checks/external_reach.go`
- Test: `internal/agent/checks/external_reach_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/agent/checks/external_reach_test.go`:

```go
package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExternalReach_AllOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := ExternalReachCheck{
		Targets: []ExternalReachTarget{
			{Name: "T1", URL: srv.URL},
			{Name: "T2", URL: srv.URL},
			{Name: "T3", URL: srv.URL},
		},
		FailThreshold: 2,
		HTTPClient:    srv.Client(),
		PerProbeTimeout: 2 * time.Second,
	}
	got := c.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("status=%s details=%v", got.Status, got.Details)
	}
	if total, _ := got.Details["targets_total"].(int); total != 3 {
		t.Fatalf("targets_total=%v", got.Details["targets_total"])
	}
}

func TestExternalReach_BelowThreshold(t *testing.T) {
	srvOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srvOK.Close()
	c := ExternalReachCheck{
		Targets: []ExternalReachTarget{
			{Name: "Live", URL: srvOK.URL},
			{Name: "Live2", URL: srvOK.URL},
			{Name: "Dead", URL: "http://127.0.0.1:1/"},
		},
		FailThreshold:   2,
		HTTPClient:      srvOK.Client(),
		PerProbeTimeout: 500 * time.Millisecond,
	}
	got := c.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("status=%s details=%v", got.Status, got.Details)
	}
}

func TestExternalReach_AboveThreshold(t *testing.T) {
	c := ExternalReachCheck{
		Targets: []ExternalReachTarget{
			{Name: "Dead1", URL: "http://127.0.0.1:1/"},
			{Name: "Dead2", URL: "http://127.0.0.1:1/"},
			{Name: "Dead3", URL: "http://127.0.0.1:1/"},
		},
		FailThreshold:   2,
		HTTPClient:      &http.Client{Timeout: 500 * time.Millisecond},
		PerProbeTimeout: 500 * time.Millisecond,
	}
	got := c.Run(context.Background(), Deps{})
	if got.Status != "fail" {
		t.Fatalf("status=%s details=%v", got.Status, got.Details)
	}
	failed, _ := got.Details["targets_failed"].([]map[string]any)
	if len(failed) != 3 {
		t.Fatalf("len(failed)=%d", len(failed))
	}
}

func TestExternalReach_ParallelTiming(t *testing.T) {
	// Three slow servers — each holds 200ms. Sequential would be 600ms;
	// parallel should be ~200ms. Allow generous slack for CI jitter.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()
	c := ExternalReachCheck{
		Targets: []ExternalReachTarget{
			{Name: "A", URL: slow.URL},
			{Name: "B", URL: slow.URL},
			{Name: "C", URL: slow.URL},
		},
		FailThreshold:   2,
		HTTPClient:      slow.Client(),
		PerProbeTimeout: 1 * time.Second,
	}
	start := time.Now()
	c.Run(context.Background(), Deps{})
	took := time.Since(start)
	if took > 500*time.Millisecond {
		t.Errorf("parallel probe took %v, expected ~200ms", took)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd .worktrees/stage-2 && go test ./internal/agent/checks/ -run TestExternalReach -v`
Expected: FAIL with "undefined: ExternalReachCheck".

- [ ] **Step 3: Implement**

Create `internal/agent/checks/external_reach.go`:

```go
package checks

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

// ExternalReachTarget — one HTTP probe target. URL must be a full HTTP/HTTPS
// URL; HEAD/GET semantics handled by the check.
type ExternalReachTarget struct {
	Name string
	URL  string
}

// ExternalReachCheck verifies that "blocked-in-RU" services are reachable
// via the WG path that owns the default route. It's the practical answer
// to "did anything actually break for the user?" — pingCheck on tunnel
// peers can be green while YouTube/Telegram/Instagram are dead, e.g. when
// RKN flips a regional filter or a CDN edge gets blackholed.
//
// Probes run in parallel (one HTTP client, multiple goroutines), each with
// its own per-probe timeout. FAIL when len(failed) >= FailThreshold.
type ExternalReachCheck struct {
	Targets         []ExternalReachTarget
	FailThreshold   int           // FAIL if failed >= threshold; 0 → ceil(N*2/3)
	HTTPClient      *http.Client  // iface-bound by caller; fallback to http.DefaultClient
	PerProbeTimeout time.Duration // default 5s
	ViaInterface    string        // informational; surfaced in Details for the renderer
}

func (ExternalReachCheck) Name() string { return "external_reach" }

func (c ExternalReachCheck) Run(ctx context.Context, _ Deps) wire.Check {
	start := time.Now()
	if c.PerProbeTimeout <= 0 {
		c.PerProbeTimeout = 5 * time.Second
	}
	httpc := c.HTTPClient
	if httpc == nil {
		httpc = http.DefaultClient
	}
	threshold := c.FailThreshold
	if threshold <= 0 {
		// ceil(N * 2 / 3) — for N=3 → 2, N=4 → 3, N=2 → 2.
		threshold = (len(c.Targets)*2 + 2) / 3
		if threshold < 1 {
			threshold = 1
		}
	}

	type result struct {
		idx int
		ok  bool
		err string
	}
	results := make([]result, len(c.Targets))
	var wg sync.WaitGroup
	for i, t := range c.Targets {
		wg.Add(1)
		go func(i int, t ExternalReachTarget) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, c.PerProbeTimeout)
			defer cancel()
			req, err := http.NewRequestWithContext(cctx, http.MethodGet, t.URL, nil)
			if err != nil {
				results[i] = result{i, false, err.Error()}
				return
			}
			req.Header.Set("User-Agent", "wg-monitor/external-reach")
			resp, err := httpc.Do(req)
			if err != nil {
				results[i] = result{i, false, err.Error()}
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode/100 != 2 && resp.StatusCode/100 != 3 {
				results[i] = result{i, false, fmt.Sprintf("HTTP %d", resp.StatusCode)}
				return
			}
			results[i] = result{i, true, ""}
		}(i, t)
	}
	wg.Wait()

	var failed []map[string]any
	var okNames []string
	for i, r := range results {
		t := c.Targets[i]
		if r.ok {
			okNames = append(okNames, t.Name)
		} else {
			failed = append(failed, map[string]any{
				"name": t.Name,
				"url":  t.URL,
				"err":  r.err,
			})
		}
	}
	details := map[string]any{
		"targets_total":  len(c.Targets),
		"targets_failed": failed,
		"targets_ok":     okNames,
		"threshold":      threshold,
	}
	if c.ViaInterface != "" {
		details["via_interface"] = c.ViaInterface
	}
	if len(failed) >= threshold {
		return Fail(c.Name(), start,
			fmt.Sprintf("%d/%d targets unreachable", len(failed), len(c.Targets)),
			details)
	}
	return OK(c.Name(), start, details)
}
```

- [ ] **Step 4: Run tests**

Run: `cd .worktrees/stage-2 && go test ./internal/agent/checks/ -run TestExternalReach -v`
Expected: PASS, all 4 subtests including parallel-timing.

- [ ] **Step 5: Commit**

```bash
cd .worktrees/stage-2
git add internal/agent/checks/external_reach.go internal/agent/checks/external_reach_test.go
git commit -m "feat(checks): ExternalReachCheck — multi-target HTTP probe

Probes blocked-in-RU services (YouTube/Telegram/Instagram by default
via wiring) in parallel through whichever HTTP client the caller
hands in. The intended client is iface-bound to the defaultRoute=true
WG tunnel, so the check answers 'can the user actually reach the
services WG was set up for?'."
```

---

### Task 7: Config schema for external_reach

**Files:**
- Modify: `internal/agent/config.go`
- Test: `internal/agent/config_test.go` (if exists; otherwise create)

- [ ] **Step 1: Write failing test**

Append to `internal/agent/config_test.go` (create the file if absent — copy package decl from `config.go`):

```go
func TestLoadConfig_ExternalReachDefaults(t *testing.T) {
	yaml := `
backend:
  url: https://example.com
  token: 0123456789abcdef0123456789abcdef
agent:
  nickname: testkeen
checks:
  awg:
    interface: nwg1
    expected_exit_ip: 1.2.3.4
  dns:
    test_domain: example.com
external_reach:
  enabled: true
`
	tmp := t.TempDir()
	path := tmp + "/cfg.yaml"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.ExternalReach.Enabled {
		t.Fatalf("ExternalReach.Enabled=false")
	}
	if len(cfg.ExternalReach.Targets) != 3 {
		t.Fatalf("expected 3 default targets, got %d", len(cfg.ExternalReach.Targets))
	}
	got := map[string]string{}
	for _, t := range cfg.ExternalReach.Targets {
		got[t.Name] = t.URL
	}
	for _, name := range []string{"YouTube", "Telegram", "Instagram"} {
		if got[name] == "" {
			t.Errorf("missing default target %q", name)
		}
	}
	if cfg.ExternalReach.FailThreshold != 2 {
		t.Errorf("FailThreshold=%d, want 2", cfg.ExternalReach.FailThreshold)
	}
}

func TestLoadConfig_ExternalReachOverride(t *testing.T) {
	yaml := `
backend:
  url: https://example.com
  token: 0123456789abcdef0123456789abcdef
agent:
  nickname: testkeen
checks:
  awg:
    interface: nwg1
    expected_exit_ip: 1.2.3.4
  dns:
    test_domain: example.com
external_reach:
  enabled: true
  fail_threshold: 1
  targets:
    - name: Custom
      url: https://example.org/probe
`
	tmp := t.TempDir()
	path := tmp + "/cfg.yaml"
	_ = os.WriteFile(path, []byte(yaml), 0o644)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.ExternalReach.Targets) != 1 || cfg.ExternalReach.Targets[0].Name != "Custom" {
		t.Errorf("override not respected: %+v", cfg.ExternalReach.Targets)
	}
	if cfg.ExternalReach.FailThreshold != 1 {
		t.Errorf("FailThreshold=%d, want 1", cfg.ExternalReach.FailThreshold)
	}
}
```

(If the file is new, add imports `os`, `testing` and a package decl `package agent`.)

- [ ] **Step 2: Run to verify failure**

Run: `cd .worktrees/stage-2 && go test ./internal/agent/ -run TestLoadConfig_ExternalReach -v`
Expected: FAIL — undefined fields.

- [ ] **Step 3: Add config struct + defaults**

In `internal/agent/config.go`, add before the `LoadOption` block:

```go
type ExternalReachConfig struct {
	Enabled       bool                    `yaml:"enabled"`
	FailThreshold int                     `yaml:"fail_threshold"`
	Targets       []ExternalReachTarget   `yaml:"targets"`
	BindToDefault bool                    `yaml:"bind_to_default"` // bind HTTP to defaultRoute WG iface; default true
}

type ExternalReachTarget struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

var defaultExternalReachTargets = []ExternalReachTarget{
	{Name: "YouTube", URL: "https://www.youtube.com/generate_204"},
	{Name: "Telegram", URL: "https://web.telegram.org/"},
	{Name: "Instagram", URL: "https://www.instagram.com/favicon.ico"},
}
```

Add a new field to `Config`:

```go
type Config struct {
	Backend       BackendConfig       `yaml:"backend"`
	Agent         AgentConfig         `yaml:"agent"`
	Checks        ChecksConfig        `yaml:"checks"`
	AwgManager    AwgManagerConfig    `yaml:"awg_manager"` // already there
	State         StateConfig         `yaml:"state"`        // already there
	ExternalReach ExternalReachConfig `yaml:"external_reach"`
}
```

(If field names above don't match what's actually there, snap to the existing list and add `ExternalReach` at the end.)

In `LoadConfig`, after the existing default-application block (where DNS defaults are set), add:

```go
if cfg.ExternalReach.Enabled {
	if len(cfg.ExternalReach.Targets) == 0 {
		cfg.ExternalReach.Targets = append([]ExternalReachTarget(nil), defaultExternalReachTargets...)
	}
	if cfg.ExternalReach.FailThreshold <= 0 {
		// Match check-side default: ceil(N*2/3) — so 3 targets → 2.
		n := len(cfg.ExternalReach.Targets)
		cfg.ExternalReach.FailThreshold = (n*2 + 2) / 3
		if cfg.ExternalReach.FailThreshold < 1 {
			cfg.ExternalReach.FailThreshold = 1
		}
	}
}
```

- [ ] **Step 4: Run tests**

Run: `cd .worktrees/stage-2 && go test ./internal/agent/ -run TestLoadConfig_ExternalReach -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd .worktrees/stage-2
git add internal/agent/config.go internal/agent/config_test.go
git commit -m "feat(config): external_reach schema with sensible defaults

When external_reach.enabled=true and no targets given, fill in
YouTube/Telegram/Instagram. fail_threshold defaults to ceil(N*2/3)."
```

---

### Task 8: Wire ExternalReachCheck into agent main

**Files:**
- Modify: `cmd/agent/main.go`

- [ ] **Step 1: Plan the wiring**

The check needs an iface-bound HTTP client pointing at the defaultRoute=true tunnel. Picking the iface dynamically per-tick is overkill — the agent already builds the iface-map once at startup via `keenetic.FetchIfaceMap`. Reuse it: at startup, look up which tunnel has `defaultRoute=true` from the same awg-manager API call, and bind the HTTP client to its `interfaceName`.

If no defaultRoute tunnel exists OR `bind_to_default=false`, use `http.DefaultClient` (warn at startup).

- [ ] **Step 2: Add helper to pick the default-route iface**

Append to `cmd/agent/main.go` (above `buildDNSCheck`):

```go
// pickDefaultRouteIface returns the linux iface name (e.g. "nwg1") of the
// tunnel marked defaultRoute=true. Empty when no such tunnel exists or
// awg-manager is unreachable — the caller is expected to fall back to the
// system default route.
func pickDefaultRouteIface(ctx context.Context, c *awgmgr.Client, logger *slog.Logger) string {
	ta, err := c.TunnelsAll(ctx)
	if err != nil {
		logger.Warn("external_reach: cannot pick default-route iface", "err", err)
		return ""
	}
	for _, t := range ta.Tunnels {
		if t.DefaultRoute && t.Enabled && t.InterfaceName != "" {
			return t.InterfaceName
		}
	}
	return ""
}
```

- [ ] **Step 3: Wire it after the existing singleChecks list**

In `main()` of `cmd/agent/main.go`, replace the `singleChecks := []checks.Check{...}` block with:

```go
singleChecks := []checks.Check{
	checks.AwgManagerCheck{Client: awgClient},
	checks.HydraRouteCheck{Client: awgClient},
	buildDNSCheck(cfg, logger),
}

if cfg.ExternalReach.Enabled {
	if er := buildExternalReachCheck(cfg, awgClient, logger); er != nil {
		singleChecks = append(singleChecks, er)
	}
}
```

Add a builder near `buildDNSCheck`:

```go
func buildExternalReachCheck(cfg *agent.Config, awgClient *awgmgr.Client, logger *slog.Logger) checks.Check {
	pc := cfg.ExternalReach
	if len(pc.Targets) == 0 {
		logger.Warn("external_reach enabled but no targets — skipping")
		return nil
	}

	targets := make([]checks.ExternalReachTarget, 0, len(pc.Targets))
	for _, t := range pc.Targets {
		targets = append(targets, checks.ExternalReachTarget{Name: t.Name, URL: t.URL})
	}

	httpc := &http.Client{Timeout: 6 * time.Second}
	viaIface := ""
	if pc.BindToDefault {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		viaIface = pickDefaultRouteIface(ctx, awgClient, logger)
		cancel()
		if viaIface != "" {
			httpc = &http.Client{
				Timeout: 6 * time.Second,
				Transport: &http.Transport{
					DialContext: checks.IfaceDialer(viaIface).DialContext,
				},
			}
			logger.Info("external_reach iface-bound", "iface", viaIface, "targets", len(targets))
		} else {
			logger.Warn("external_reach: no defaultRoute tunnel found, using system route")
		}
	}

	return checks.ExternalReachCheck{
		Targets:         targets,
		FailThreshold:   pc.FailThreshold,
		HTTPClient:      httpc,
		PerProbeTimeout: 5 * time.Second,
		ViaInterface:    viaIface,
	}
}
```

(`bind_to_default` defaults to false in YAML — if the user wants iface-binding they set `bind_to_default: true`. Update the config-default block from Task 7 to set `BindToDefault = true` when ExternalReach is enabled and not explicitly false. Since YAML zero-value is false, use a `*bool` pointer if explicit-false matters; for now, default to "always bind when enabled".)

- [ ] **Step 4: Build & run**

Run: `cd .worktrees/stage-2 && go build ./...`
Expected: clean build.

- [ ] **Step 5: Commit**

```bash
cd .worktrees/stage-2
git add cmd/agent/main.go internal/agent/config.go
git commit -m "feat(agent): wire ExternalReachCheck through cmd/agent/main

When external_reach.enabled=true, build an iface-bound HTTP client
pointing at the defaultRoute=true WG tunnel and register the multi-
target probe alongside DNS/HydraRoute/AwgManager."
```

---

### Task 9: Renderer test for external_reach + integration sanity check

**Files:**
- Test: `internal/backend/alerts/format_test.go`

- [ ] **Step 1: Add a renderer integration test**

Append to `internal/backend/alerts/format_test.go`:

```go
func TestFormatHard_ExternalReach(t *testing.T) {
	args := HardArgs{
		Nickname:    "testkeen",
		CheckName:   "external_reach",
		ConsecFails: 3,
		HardSince:   time.Date(2026, 5, 5, 10, 23, 0, 0, time.UTC),
		Check: wire.Check{
			Name:   "external_reach",
			Status: "fail",
			Details: map[string]any{
				"targets_total": 3,
				"targets_failed": []any{
					map[string]any{"name": "YouTube", "err": "timeout"},
					map[string]any{"name": "Instagram", "err": "connection refused"},
				},
				"targets_ok":    []any{"Telegram"},
				"via_interface": "nwg1",
				"error":         "2/3 targets unreachable",
			},
		},
	}
	got := FormatHard(args)
	mustContain(t, got, "Иностранные сервисы")
	mustContain(t, got, "целей: 3")
	mustContain(t, got, "недоступно: 2")
	mustContain(t, got, "✗ YouTube — timeout")
	mustContain(t, got, "✗ Instagram — connection refused")
	mustContain(t, got, "работает: Telegram")
	mustContain(t, got, "через nwg1")
}

func mustContain(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("output missing %q:\n%s", sub, s)
	}
}
```

- [ ] **Step 2: Run all alerts tests**

Run: `cd .worktrees/stage-2 && go test ./internal/backend/alerts/ -v`
Expected: PASS, including new external_reach renderer test.

- [ ] **Step 3: Run full backend + agent test sweeps**

Run: `cd .worktrees/stage-2 && go test ./...`
Expected: PASS across the whole tree.

- [ ] **Step 4: Commit**

```bash
cd .worktrees/stage-2
git add internal/backend/alerts/format_test.go
git commit -m "test(alerts): renderer happy-path for external_reach"
```

---

## Phase D — Live verify on testkeen

### Task 10: Build & deploy & verify

- [ ] **Step 1: Build agent for arm64**

Run: `cd .worktrees/stage-2 && GOOS=linux GOARCH=arm64 go build -ldflags "-X main.Version=0.6.1-alerts-v1" -o dist/wg-monitor-agent-arm64 ./cmd/agent`
Expected: binary written to `dist/`.

- [ ] **Step 2: Build backend for arm64**

Run: `cd .worktrees/stage-2 && GOOS=linux GOARCH=arm64 go build -o dist/wg-monitor-backend-arm64 ./cmd/backend`
Expected: clean build.

- [ ] **Step 3: Deploy backend to VPS Main**

Use existing `deploy/` scripts. If `deploy/backend-update.sh` exists, run it; otherwise scp + restart manually:

```bash
scp dist/wg-monitor-backend-arm64 vps-main:/usr/local/bin/wg-monitor-backend.new
ssh vps-main 'sudo install -m0755 /usr/local/bin/wg-monitor-backend.new /usr/local/bin/wg-monitor-backend && sudo systemctl restart wg-monitor-backend'
```

- [ ] **Step 4: Deploy agent to testkeen**

Use existing rolling-update script in `deploy/` or:

```bash
scp dist/wg-monitor-agent-arm64 testkeen:/opt/sbin/wg-monitor-agent.new
ssh testkeen 'mv /opt/sbin/wg-monitor-agent.new /opt/sbin/wg-monitor-agent && /opt/etc/init.d/S99wg-monitor restart'
```

- [ ] **Step 5: Edit testkeen config to enable external_reach**

```bash
ssh testkeen 'cat >> /opt/etc/wg-monitor/config.yaml <<EOF

external_reach:
  enabled: true
  bind_to_default: true
EOF
/opt/etc/init.d/S99wg-monitor restart'
```

- [ ] **Step 6: Force a HARD by stopping a tunnel (E2E for renderer)**

```bash
ssh testkeen 'ndmc -c "interface Wireguard1 down"'
```

Wait for 3 consecutive fails (~3 minutes at default interval). Verify in TG:
1. The HARD message reads `🔴 [testkeen] amnezia_for_awg (nwg1) — DOWN` with handshake/pingCheck rows — **no decode dump**.
2. The synthetic `tunnels` check stays OK (awg-manager itself is fine).
3. If you also stop awg-manager (`/opt/etc/init.d/S55awg-manager stop`), confirm the new alert reads `🔴 [testkeen] awg-manager API — DOWN` with a clean reason line — **no `(body=…)` trail**.

```bash
ssh testkeen 'ndmc -c "interface Wireguard1 up"; /opt/etc/init.d/S55awg-manager start'
```

- [ ] **Step 7: Force a HARD on external_reach by killing the WG tunnel**

```bash
ssh testkeen 'ndmc -c "interface Wireguard1 down"'  # the defaultRoute tunnel
```

Wait for HARD. Verify TG message:
- Title: `🌍 Иностранные сервисы`
- Body lists each failing target with reason
- "через nwg1" line present

Restore: `ndmc -c "interface Wireguard1 up"`.

- [ ] **Step 8: Final smoke**

Confirm in admin chat that all four categories now render with pretty labels and no raw JSON dumps. If everything looks good — done.

---

## Self-Review

**Spec coverage:**
- "Decode-баг с пустым lastHandshake" → Phase A (Tasks 1-2). ✓
- "tunnels — DOWN рендерится сырой ошибкой" → Task 4 (awgmgr_api category). ✓
- "DNS дублирует строку" → Task 5. ✓
- "Машинные имена в заголовках" → Task 3 (prettyCheckLabel for top-level). ✓
- "Иностранные сервисы (YouTube/Telegram/Instagram)" → Phase C (Tasks 6-9). ✓
- "Привязка к defaultRoute туннелю" → Task 8 (`pickDefaultRouteIface`). ✓
- "Live verify на testkeen" → Phase D (Task 10). ✓

**Placeholder scan:** clean. All steps contain literal code; no "TBD" / "see Task N for code".

**Type consistency:**
- `nullableTime` introduced in Task 1, used in Tasks 2 and 8 (via `pickDefaultRouteIface` reading `t.LastHandshake.Time()` — but actually that helper only reads `DefaultRoute` and `InterfaceName`, both unaffected; safe).
- `ExternalReachTarget` lives in TWO packages (`agent` config + `checks` impl) — these are distinct types by design (config carries YAML tags, check carries the runtime type). Wiring in Task 8 explicitly translates one to the other.
- `FailThreshold` default formula `(n*2 + 2) / 3` appears in both Task 6 (check-side fallback) and Task 7 (config-side default) — kept identical so behavior is the same whether the user sets the field or omits it.

**Potential snag:** In Task 8 the config-default block from Task 7 needs to also set `BindToDefault` when `Enabled=true` and the user didn't say otherwise. Since YAML zero-value `false` is indistinguishable from "not set" without a `*bool`, accept that operators must explicitly set `bind_to_default: true` in YAML — documented in Task 8 Step 3.

**Ready to execute.**
