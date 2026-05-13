# TG UX polish — diagnostics refit, error hints, /help — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace raw error dumps and inconsistent message formatting in the Telegram bot with a canonical `Card` template, a centralised error-hint dictionary, an auto-triggering diagnostics flow, and a `/help` surface for admins and operators.

**Architecture:** All user-facing rendering goes through a new `alerts.Card` builder. Errors flow through `alerts.HintFor(action, statusOrRaw)` which maps typed tokens (`NO_REPORT`, `HTTP_502`, `DIAG_TIMEOUT`, etc.) to friendly Russian summary + hint. The agent's `diag_now` action gains an auto-trigger loop: on `NO_REPORT` it POSTs `/api/diagnostics/run` and polls the result endpoint for up to 36s. Diagnostic results are pretty-printed from JSON; the raw body is cached for a "📄 Полный отчёт" button. `/help` content is built from static constants, dispatched by role (admin / operator / none).

**Tech Stack:** Go 1.22+, SQLite (existing schema, no migration), Telegram Bot API (`r.tg` interface), `encoding/json` for diag parsing. Tests use the in-package `newTestDB` helper and `fakeRouterTG`/`fakeEnqueuer` fakes.

**Reference spec:** [docs/superpowers/specs/2026-05-13-tg-ux-polish-design.md](../specs/2026-05-13-tg-ux-polish-design.md)

---

## Task 1: Canonical `Card` helper

**Files:**
- Create: `internal/backend/alerts/card.go`
- Test: `internal/backend/alerts/card_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/backend/alerts/card_test.go`:

```go
package alerts

import (
	"strings"
	"testing"
)

func TestCard_BadgeLabelSummaryOnly(t *testing.T) {
	c := Card{Badge: "✅", Label: "📊 Диагностика", Summary: "всё ок"}
	got := c.Render(CardOpts{})
	want := "✅ 📊 Диагностика: всё ок"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCard_NoSummaryDropsTrailingColon(t *testing.T) {
	c := Card{Badge: "ℹ", Label: "Помощь"}
	got := c.Render(CardOpts{})
	want := "ℹ Помощь"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCard_DetailsPlainHaveBlankLineSeparator(t *testing.T) {
	c := Card{Badge: "❌", Label: "📊 Диагностика", Summary: "не получилось",
		Details: "deeper info"}
	got := c.Render(CardOpts{})
	if !strings.Contains(got, "не получилось\n\ndeeper info") {
		t.Errorf("expected blank-line separator between summary and details:\n%s", got)
	}
}

func TestCard_DetailsCodeFenceWhenRequested(t *testing.T) {
	c := Card{Badge: "📊", Label: "Диагностика", Summary: "raw report",
		Details: "{\"foo\":1}"}
	got := c.Render(CardOpts{CodeFenceDetails: true})
	if !strings.Contains(got, "```\n{\"foo\":1}\n```") {
		t.Errorf("expected code fence around details:\n%s", got)
	}
}

func TestCard_HintRendersBelowDetailsWithEmoji(t *testing.T) {
	c := Card{Badge: "❌", Label: "📊 Диагностика", Summary: "no report",
		Hint: "Запусти ещё раз."}
	got := c.Render(CardOpts{})
	if !strings.Contains(got, "\n\n💡 Запусти ещё раз.") {
		t.Errorf("expected hint block with 💡 prefix:\n%s", got)
	}
}

func TestCard_HintWithoutDetailsStillHasBlankLine(t *testing.T) {
	c := Card{Badge: "❌", Label: "Команда", Summary: "не сработало",
		Hint: "проверь связь"}
	got := c.Render(CardOpts{})
	if !strings.Contains(got, "не сработало\n\n💡 проверь связь") {
		t.Errorf("hint must follow summary with blank line if no details:\n%s", got)
	}
}

func TestCard_FullQuadOrdering(t *testing.T) {
	c := Card{Badge: "❌", Label: "Тест", Summary: "summary line",
		Details: "details body", Hint: "hint line"}
	got := c.Render(CardOpts{})
	want := "❌ Тест: summary line\n\ndetails body\n\n💡 hint line"
	if got != want {
		t.Errorf("got:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestCard_MaxBytesTruncatesWithEllipsis(t *testing.T) {
	long := strings.Repeat("Я", 5000)
	c := Card{Badge: "📊", Label: "Диагностика", Summary: "raw", Details: long}
	got := c.Render(CardOpts{MaxBytes: 200})
	if len(got) > 200 {
		t.Errorf("rendered length %d exceeds MaxBytes=200", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix on truncation, got: ...%q", got[len(got)-20:])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backend/alerts/ -run TestCard_ -v`
Expected: FAIL — `Card` / `CardOpts` undefined.

- [ ] **Step 3: Implement the Card helper**

Create `internal/backend/alerts/card.go`:

```go
// Package alerts owns all user-facing Telegram message rendering. Card is
// the canonical 4-block template (badge + label/summary + details + hint)
// used by every action result and error path.
package alerts

import (
	"strings"
)

// Card is the canonical shape of one bot-to-user message.
//
//   <Badge> <Label>: <Summary>
//
//   <Details (optional; may be code-fenced via CardOpts.CodeFenceDetails)>
//
//   💡 <Hint (optional)>
//
// Blank lines separate the three blocks. Any field may be empty; empty
// fields collapse their separator.
type Card struct {
	Badge   string // "✅" / "🔴" / "🟡" / "⚪" / "❌" / "⏳" / "ℹ"
	Label   string // "📊 Диагностика" — typically emoji + Russian noun
	Summary string // one-line continuation after the label colon
	Details string // optional multi-line body
	Hint    string // optional next-step explanation; rendered with 💡 prefix
}

// CardOpts tunes Card.Render output.
type CardOpts struct {
	// CodeFenceDetails wraps Details in triple-backtick. Use only for
	// monospaced machine output (JSON, route tables, opkg logs). Never
	// for natural-language errors.
	CodeFenceDetails bool
	// MaxBytes hard-caps the rendered length. 0 means no cap. When the
	// rendered string would exceed MaxBytes, it is truncated rune-aware
	// with a single trailing "…".
	MaxBytes int
}

// Render assembles the card into its final on-the-wire string.
func (c Card) Render(opts CardOpts) string {
	var b strings.Builder
	// Header line: "<Badge> <Label>[: <Summary>]"
	if c.Badge != "" {
		b.WriteString(c.Badge)
		if c.Label != "" || c.Summary != "" {
			b.WriteByte(' ')
		}
	}
	b.WriteString(c.Label)
	if c.Summary != "" {
		if c.Label != "" {
			b.WriteString(": ")
		}
		b.WriteString(c.Summary)
	}
	if c.Details != "" {
		b.WriteString("\n\n")
		if opts.CodeFenceDetails {
			b.WriteString("```\n")
			b.WriteString(c.Details)
			b.WriteString("\n```")
		} else {
			b.WriteString(c.Details)
		}
	}
	if c.Hint != "" {
		b.WriteString("\n\n💡 ")
		b.WriteString(c.Hint)
	}
	out := b.String()
	if opts.MaxBytes > 0 && len(out) > opts.MaxBytes {
		out = truncateWithEllipsis(out, opts.MaxBytes)
	}
	return out
}

// truncateWithEllipsis trims the string by runes (UTF-8 aware) until it
// fits maxBytes including the trailing "…" suffix.
func truncateWithEllipsis(s string, maxBytes int) string {
	const ellipsis = "…"
	if maxBytes <= len(ellipsis) {
		return ellipsis[:maxBytes]
	}
	runes := []rune(s)
	for len(runes) > 0 {
		candidate := string(runes) + ellipsis
		if len(candidate) <= maxBytes {
			return candidate
		}
		runes = runes[:len(runes)-1]
	}
	return ellipsis
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backend/alerts/ -run TestCard_ -v`
Expected: PASS (8 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/backend/alerts/card.go internal/backend/alerts/card_test.go
git commit -m "feat(alerts): canonical Card template (badge + label/summary + details + hint)"
```

---

## Task 2: Error hint dictionary

**Files:**
- Create: `internal/backend/alerts/error_hints.go`
- Test: `internal/backend/alerts/error_hints_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/backend/alerts/error_hints_test.go`:

```go
package alerts

import (
	"strings"
	"testing"
)

func TestHintFor_NoReport(t *testing.T) {
	sum, hint := HintFor("diag_now", "HTTP_400: {\"error\":true,\"code\":\"NO_REPORT\"}")
	if !strings.Contains(sum, "не сформирован") {
		t.Errorf("summary missing 'не сформирован': %q", sum)
	}
	if !strings.Contains(hint, "ещё раз") {
		t.Errorf("hint missing retry suggestion: %q", hint)
	}
}

func TestHintFor_DiagTimeout(t *testing.T) {
	sum, hint := HintFor("diag_now", "DIAG_TIMEOUT: triggered but no result after 36s")
	if !strings.Contains(sum, "36") {
		t.Errorf("timeout summary should mention 36s: %q", sum)
	}
	if !strings.Contains(hint, "30") || !strings.Contains(hint, "60") {
		t.Errorf("hint should reference 30-60s window: %q", hint)
	}
}

func TestHintFor_AwgmgrUnavailable(t *testing.T) {
	for _, code := range []string{"HTTP_502", "HTTP_503"} {
		raw := code + ": awgmgr down"
		sum, hint := HintFor("diag_now", raw)
		if !strings.Contains(sum, "awg-manager") {
			t.Errorf("%s: summary missing 'awg-manager': %q", code, sum)
		}
		if !strings.Contains(hint, "S99awg-manager") {
			t.Errorf("%s: hint missing service path: %q", code, hint)
		}
	}
}

func TestHintFor_AwgmgrUnauthorized(t *testing.T) {
	for _, code := range []string{"HTTP_401", "HTTP_403"} {
		_, hint := HintFor("diag_now", code+": denied")
		if !strings.Contains(hint, "Установить агент") {
			t.Errorf("%s: hint should suggest wizard reinstall: %q", code, hint)
		}
	}
}

func TestHintFor_ConnectionRefused(t *testing.T) {
	_, hint := HintFor("restart_tunnel", "dial tcp 127.0.0.1:2222: connection refused")
	if !strings.Contains(hint, "2222") {
		t.Errorf("hint should mention port 2222: %q", hint)
	}
}

func TestHintFor_Timeout(t *testing.T) {
	sum, hint := HintFor("opkg_upgrade", "TIMEOUT")
	if !strings.Contains(sum, "не уложился") {
		t.Errorf("timeout summary unexpected: %q", sum)
	}
	if !strings.Contains(hint, "logread") {
		t.Errorf("timeout hint should mention logread: %q", hint)
	}
}

func TestHintFor_Locked(t *testing.T) {
	_, hint := HintFor("diag_now", "LOCKED")
	if !strings.Contains(hint, "lock-файл") && !strings.Contains(hint, "lock") {
		t.Errorf("locked hint should mention lock: %q", hint)
	}
}

func TestHintFor_SqliteLocked(t *testing.T) {
	sum, _ := HintFor("admin_topics", "database is locked")
	if !strings.Contains(sum, "SQLite") {
		t.Errorf("sqlite-locked summary should mention SQLite: %q", sum)
	}
}

func TestHintFor_DefaultFallbackTrimsRaw(t *testing.T) {
	raw := strings.Repeat("X", 500) + "\nsecond line"
	sum, hint := HintFor("diag_now", raw)
	if !strings.Contains(sum, "что-то пошло не так") {
		t.Errorf("default summary unexpected: %q", sum)
	}
	if len(hint) > 350 {
		t.Errorf("default hint should trim raw to ~200 chars, got len=%d", len(hint))
	}
	if strings.Contains(hint, "\n") {
		t.Errorf("default hint should not contain newline (first line only): %q", hint)
	}
}

func TestHintFor_DefaultFallbackSanitizesCodeFence(t *testing.T) {
	raw := "weird ``` triple backticks ``` inside"
	_, hint := HintFor("diag_now", raw)
	if strings.Contains(hint, "```") {
		t.Errorf("default hint must strip triple-backticks to avoid fence break: %q", hint)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backend/alerts/ -run TestHintFor_ -v`
Expected: FAIL — `HintFor` undefined.

- [ ] **Step 3: Implement the hint dictionary**

Create `internal/backend/alerts/error_hints.go`:

```go
package alerts

import (
	"strings"
)

// HintFor maps a raw error string + action name to a user-friendly
// (summary, hint) pair, both in Russian. statusOrRaw is either:
//   - a CommandResult.Status token: "TIMEOUT" / "LOCKED"
//   - the agent's typed error prefix: "NO_REPORT" / "DIAG_TIMEOUT" /
//     "HTTP_NNN" / "HTTP_REFUSED"
//   - a raw error message (DB / SSH / network) for admin paths
//
// Match order: longest-pattern-first via substring; the default branch
// is the safety net. Patterns are intentionally case-sensitive: typed
// tokens are uppercase by contract.
func HintFor(action, statusOrRaw string) (summary, hint string) {
	s := statusOrRaw
	switch {
	case strings.Contains(s, "NO_REPORT") || strings.Contains(s, "no report available"):
		return "отчёт ещё не сформирован",
			"Запусти ещё раз — awg-manager не успел подготовить отчёт."
	case strings.Contains(s, "DIAG_TIMEOUT"):
		return "диагностика не уложилась в 36с",
			"awg-manager запустил отчёт, но не успел его собрать за 36с. " +
				"Попробуй ещё раз — обычно это занимает 30–60с."
	case strings.Contains(s, "HTTP_502") || strings.Contains(s, "HTTP_503"):
		return "awg-manager недоступен",
			"Зайди по SSH и выполни: `/opt/etc/init.d/S99awg-manager status`. " +
				"Если упал — `/opt/etc/init.d/S99awg-manager restart`."
	case strings.Contains(s, "HTTP_401") || strings.Contains(s, "HTTP_403"):
		return "awg-manager не пускает агент",
			"Токен агента устарел или права изменились. В wizard: " +
				"«📦 Установить агент» переустановит токен на роутере."
	case strings.Contains(s, "connection refused") ||
		strings.Contains(s, "HTTP_REFUSED") ||
		strings.Contains(s, "dial tcp"):
		return "агент не достучался до awg-manager",
			"awg-manager не слушает порт 2222. `netstat -tln | grep 2222` " +
				"на роутере покажет, поднят ли он."
	case s == "TIMEOUT" || strings.Contains(s, "timeout (агент"):
		return "агент не уложился в лимит",
			"Роутер занят (CPU/диск). Подожди минуту; если повторится — " +
				"`top` + `logread` диагностируют причину."
	case s == "LOCKED" || strings.Contains(s, "locked ("):
		return "другая операция держит lock",
			"Подожди ~30с — параллельная команда ещё не отпустила lock-файл. " +
				"Если зависло > 2 минут — попроси админа: `rm /opt/var/run/wg-monitor.lock`."
	case strings.Contains(s, "database is locked"):
		return "SQLite занят",
			"Это transient. Подожди 1–2 секунды и повтори."
	}
	return "что-то пошло не так",
		"Деталь: `" + sanitizeRaw(s, 200) + "`. Покажи админу или попробуй ещё раз через минуту."
}

// sanitizeRaw cuts off raw error text after maxLen runes, replaces
// newlines with spaces, and strips triple-backticks so the hint can
// safely sit inside its own message without breaking caller-side
// markdown.
func sanitizeRaw(raw string, maxLen int) string {
	// First line only — multiline raw is noisy.
	if i := strings.IndexByte(raw, '\n'); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.ReplaceAll(raw, "```", "'''")
	runes := []rune(raw)
	if len(runes) > maxLen {
		runes = runes[:maxLen]
	}
	return strings.TrimSpace(string(runes))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backend/alerts/ -run TestHintFor_ -v`
Expected: PASS (10 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/backend/alerts/error_hints.go internal/backend/alerts/error_hints_test.go
git commit -m "feat(alerts): centralised error-hint dictionary with typed prefixes"
```

---

## Task 3: Diagnostic JSON parser

**Files:**
- Create: `internal/backend/alerts/diag_report.go`
- Test: `internal/backend/alerts/diag_report_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/backend/alerts/diag_report_test.go`:

```go
package alerts

import (
	"strings"
	"testing"
)

const fixtureDiagSuccess = `{
	"version": "1.0",
	"generatedAt": "2026-05-13T07:20:40.31519112Z",
	"durationMs": 2559,
	"route": {"mode": "direct"},
	"system": {
		"appVersion": "2.8.2",
		"keeneticOS": "5.0+",
		"isOS5": true,
		"arch": "arm64",
		"backend": "kernel",
		"kernelModule": {"exists": true, "loaded": true},
		"totalMemoryMB": 489,
		"uptime": "1d 17h 30m"
	},
	"wan": {
		"interfaces": {
			"apcli0": {"up": false, "label": "Wi-Fi клиент 2.4 ГГц"},
			"eth3":   {"up": true,  "label": "Подключение Ethernet"}
		},
		"anyUp": true
	}
}`

func TestParseDiagReport_HappyPath(t *testing.T) {
	summary, bullets, fallback := ParseDiagReport(fixtureDiagSuccess)
	if fallback {
		t.Fatalf("expected fallback=false on well-formed JSON")
	}
	if !strings.Contains(summary, "2559") && !strings.Contains(summary, "2 559") {
		t.Errorf("summary should include durationMs: %q", summary)
	}
	joined := strings.Join(bullets, "\n")
	for _, want := range []string{"2.8.2", "kernel", "489", "1d 17h 30m", "eth3", "apcli0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("bullets missing %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, "✅") {
		t.Errorf("bullets should include ✅ for up interface: %s", joined)
	}
	if !strings.Contains(joined, "⚪") && !strings.Contains(joined, "🔴") {
		t.Errorf("bullets should mark down interface with ⚪ or 🔴: %s", joined)
	}
}

func TestParseDiagReport_FallbackOnMalformed(t *testing.T) {
	_, _, fallback := ParseDiagReport("this is not json")
	if !fallback {
		t.Errorf("expected fallback=true on malformed input")
	}
}

func TestParseDiagReport_FallbackWhenMissingAppVersion(t *testing.T) {
	// JSON parses but contains none of the documented fields — caller
	// should fall back to dumping raw.
	_, _, fallback := ParseDiagReport(`{"unrelated": 42}`)
	if !fallback {
		t.Errorf("expected fallback=true when no documented field present")
	}
}

func TestParseDiagReport_SkipsMissingFieldsGracefully(t *testing.T) {
	partial := `{"system":{"appVersion":"2.8.2"}}`
	summary, bullets, fallback := ParseDiagReport(partial)
	if fallback {
		t.Errorf("partial-but-recognized JSON must not fall back")
	}
	if !strings.Contains(strings.Join(bullets, "\n"), "2.8.2") {
		t.Errorf("appVersion bullet missing from: %v", bullets)
	}
	// No uptime / WAN / etc — bullets must NOT contain those labels.
	for _, absent := range []string{"Uptime", "WAN"} {
		if strings.Contains(strings.Join(bullets, "\n"), absent) {
			t.Errorf("did not expect %q in bullets when field absent: %v", absent, bullets)
		}
	}
	if summary == "" {
		t.Errorf("summary should at minimum say 'отчёт получен' when minimal: %q", summary)
	}
}

func TestParseDiagReport_GeneratedAtFormatted(t *testing.T) {
	_, bullets, _ := ParseDiagReport(fixtureDiagSuccess)
	joined := strings.Join(bullets, "\n")
	// Just verify the date portion is rendered in a human form (not the
	// raw RFC3339).
	if strings.Contains(joined, "2026-05-13T07:20:40") {
		t.Errorf("generatedAt should be reformatted, not raw RFC3339: %s", joined)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backend/alerts/ -run TestParseDiagReport -v`
Expected: FAIL — `ParseDiagReport` undefined.

- [ ] **Step 3: Implement the parser**

Create `internal/backend/alerts/diag_report.go`:

```go
package alerts

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ParseDiagReport extracts headline facts from the awg-manager JSON
// report (GET /api/diagnostics/result, version "1.0"). Returns:
//   - summary: a one-line headline ("всё ок (2 559 мс)")
//   - bullets: ordered display lines for the Card.Details body
//   - rawFallback: true if the JSON could not be parsed or contained
//     none of the documented top-level fields; caller should dump raw.
//
// Only documented fields are read; unknown fields are silently skipped.
func ParseDiagReport(raw string) (summary string, bullets []string, rawFallback bool) {
	var rep diagReportV1
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		return "", nil, true
	}
	if !rep.hasAnyDocumentedField() {
		return "", nil, true
	}

	bullets = rep.renderBullets()
	summary = rep.renderSummary()
	return summary, bullets, false
}

type diagReportV1 struct {
	Version     string             `json:"version"`
	GeneratedAt string             `json:"generatedAt"`
	DurationMs  int64              `json:"durationMs"`
	System      diagSystem         `json:"system"`
	WAN         diagWAN            `json:"wan"`
	Route       map[string]any     `json:"route"`
}

type diagSystem struct {
	AppVersion    string         `json:"appVersion"`
	KeeneticOS    string         `json:"keeneticOS"`
	Arch          string         `json:"arch"`
	Backend       string         `json:"backend"`
	TotalMemoryMB int64          `json:"totalMemoryMB"`
	Uptime        string         `json:"uptime"`
	KernelModule  diagKernelMod  `json:"kernelModule"`
}

type diagKernelMod struct {
	Exists bool `json:"exists"`
	Loaded bool `json:"loaded"`
}

type diagWAN struct {
	AnyUp      bool                     `json:"anyUp"`
	Interfaces map[string]diagInterface `json:"interfaces"`
}

type diagInterface struct {
	Up    bool   `json:"up"`
	Label string `json:"label"`
}

func (r diagReportV1) hasAnyDocumentedField() bool {
	if r.Version != "" || r.GeneratedAt != "" || r.DurationMs != 0 {
		return true
	}
	if r.System.AppVersion != "" || r.System.Uptime != "" || r.System.TotalMemoryMB != 0 {
		return true
	}
	if len(r.WAN.Interfaces) > 0 {
		return true
	}
	return false
}

func (r diagReportV1) renderSummary() string {
	if r.DurationMs > 0 {
		// Russian thousands separator: thin space.
		return fmt.Sprintf("отчёт получен (%s мс)", thousandsRU(r.DurationMs))
	}
	return "отчёт получен"
}

func (r diagReportV1) renderBullets() []string {
	var out []string
	if r.GeneratedAt != "" {
		if t, err := time.Parse(time.RFC3339, r.GeneratedAt); err == nil {
			out = append(out, "📅 Снято: "+t.UTC().Format("2006-01-02 15:04:05 UTC"))
		}
	}
	if r.System.AppVersion != "" || r.System.Backend != "" || r.System.TotalMemoryMB > 0 {
		parts := []string{}
		if r.System.AppVersion != "" {
			parts = append(parts, "awg-manager "+r.System.AppVersion)
		}
		if r.System.Backend != "" {
			parts = append(parts, "backend "+r.System.Backend)
		}
		if r.System.TotalMemoryMB > 0 {
			parts = append(parts, fmt.Sprintf("RAM %d MB", r.System.TotalMemoryMB))
		}
		if len(parts) > 0 {
			out = append(out, "⚙ "+strings.Join(parts, ", "))
		}
	}
	if r.System.Uptime != "" {
		out = append(out, "⏱ Uptime: "+r.System.Uptime)
	}
	if len(r.WAN.Interfaces) > 0 {
		out = append(out, "🌐 WAN: "+renderWANInterfaces(r.WAN.Interfaces))
	}
	return out
}

// renderWANInterfaces renders interfaces in sorted name order so the
// output is stable across runs (map iteration is unordered).
func renderWANInterfaces(ifs map[string]diagInterface) string {
	names := make([]string, 0, len(ifs))
	for k := range ifs {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		ifc := ifs[n]
		icon := "⚪"
		if ifc.Up {
			icon = "✅"
		}
		parts = append(parts, fmt.Sprintf("%s %s", icon, n))
	}
	return strings.Join(parts, " · ")
}

// thousandsRU renders n with thin-space thousands separators
// ("2 559" for 2559). Russian-locale convention.
func thousandsRU(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	first := len(s) % 3
	if first > 0 {
		b.WriteString(s[:first])
	}
	for i := first; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteRune(' ') // non-breaking thin space
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backend/alerts/ -run TestParseDiagReport -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/backend/alerts/diag_report.go internal/backend/alerts/diag_report_test.go
git commit -m "feat(alerts): parse awg-manager diagnostic JSON v1 into bullets"
```

---

## Task 4: Agent — DiagRun endpoint + typed prefixes

**Files:**
- Modify: `internal/agent/awgmgr/client.go`
- Test: `internal/agent/awgmgr/client_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/awgmgr/client_test.go`:

```go
func TestClient_DiagRun_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/diagnostics/run" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method: %q", r.Method)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With header")
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true,"data":{"status":"running"}}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 2 * time.Second}}
	if err := c.DiagRun(context.Background()); err != nil {
		t.Errorf("DiagRun: %v", err)
	}
}

func TestClient_DiagRun_BubblesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":true,"message":"down"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 2 * time.Second}}
	err := c.DiagRun(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP_503") {
		t.Errorf("expected HTTP_503 in error, got: %v", err)
	}
}

func TestClient_DiagResult_TypedNoReportOnHTTP400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":true,"message":"no report available","code":"NO_REPORT"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 2 * time.Second}}
	_, err := c.DiagResult(context.Background())
	if err == nil || !strings.Contains(err.Error(), "NO_REPORT") {
		t.Errorf("expected NO_REPORT in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP_400") {
		t.Errorf("expected HTTP_400 prefix, got: %v", err)
	}
}
```

Make sure the existing `TestClient_DiagResult_HappyPath` still asserts the success body untouched.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/awgmgr/ -run TestClient_DiagRun_ -v`
Expected: FAIL — `DiagRun` undefined.

Run: `go test ./internal/agent/awgmgr/ -run TestClient_DiagResult_TypedNoReport -v`
Expected: FAIL — error string doesn't include "HTTP_400" prefix yet.

- [ ] **Step 3: Add DiagRun + retype DiagResult errors**

Edit `internal/agent/awgmgr/client.go`:

Find the `DiagResult` function (around line 162). Replace its HTTP-error
return line (originally):

```go
if resp.StatusCode != 200 {
    return "", fmt.Errorf("awgmgr diagnostics/result: HTTP %d: %s", resp.StatusCode, snippet(body))
}
```

with:

```go
if resp.StatusCode != 200 {
    return "", fmt.Errorf("HTTP_%d: awgmgr diagnostics/result: %s", resp.StatusCode, snippet(body))
}
```

After the closing brace of `DiagResult`, append:

```go
// DiagRun POSTs /api/diagnostics/run to trigger a fresh diagnostic
// pass. awg-manager 2.8.2 returns {success:true,data:{status:"running"}}
// on accept; the actual report arrives later via DiagResult. The call
// is idempotent — posting again during an in-flight run returns the
// same body without re-starting.
func (c *Client) DiagRun(ctx context.Context) error {
	start := time.Now()
	const path = "/api/diagnostics/run"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		slog.Warn("awgmgr request failed", "method", "POST", "path", path, "err", err, "duration_ms", time.Since(start).Milliseconds())
		return fmt.Errorf("HTTP_REFUSED: awgmgr POST diagnostics/run: %w", err)
	}
	defer resp.Body.Close()
	slog.Debug("awgmgr", "method", "POST", "path", path, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP_%d: awgmgr diagnostics/run: %s", resp.StatusCode, snippet(body))
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/awgmgr/ -v`
Expected: PASS (all existing + 3 new).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/awgmgr/client.go internal/agent/awgmgr/client_test.go
git commit -m "feat(awgmgr): add DiagRun + typed HTTP_NNN error prefixes"
```

---

## Task 5: Agent — diag_now auto-trigger + poll loop

**Files:**
- Modify: `internal/agent/actions/runner.go`
- Test: `internal/agent/actions/runner_test.go`

- [ ] **Step 1: Locate current diag_now handler**

Run: `grep -n 'diag_now\|DiagResult' internal/agent/actions/runner.go`

The action handler currently calls `r.AwgClient.DiagResult(ctx)` and
returns the body or the wrapped error. We are going to wrap this in a
trigger-and-poll loop.

- [ ] **Step 2: Write the failing tests**

Append to `internal/agent/actions/runner_test.go`:

```go
// awgmgrFakeMulti returns a handler that dispatches by method+path and
// counts hits per path. Used by the diag_now auto-trigger tests.
type awgmgrFakeState struct {
	resultHits int
	runHits    int
	resultBody func(hit int) (status int, body string)
	runBody    func(hit int) (status int, body string)
}

func awgmgrFakeMulti(t *testing.T, state *awgmgrFakeState) *awgmgr.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/diagnostics/result":
			state.resultHits++
			s, b := state.resultBody(state.resultHits)
			w.WriteHeader(s)
			_, _ = w.Write([]byte(b))
		case "/api/diagnostics/run":
			state.runHits++
			s, b := state.runBody(state.runHits)
			w.WriteHeader(s)
			_, _ = w.Write([]byte(b))
		default:
			t.Errorf("unexpected path: %q", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return &awgmgr.Client{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 2 * time.Second}}
}

func TestRunner_DiagNow_NoReport_TriggersAndPolls(t *testing.T) {
	state := &awgmgrFakeState{
		resultBody: func(hit int) (int, string) {
			if hit < 3 {
				return 400, `{"error":true,"code":"NO_REPORT"}`
			}
			return 200, `{"system":{"appVersion":"2.8.2"}}`
		},
		runBody: func(_ int) (int, string) {
			return 200, `{"success":true,"data":{"status":"running"}}`
		},
	}
	cli := awgmgrFakeMulti(t, state)
	r := actions.NewRunner(actions.RunnerDeps{AwgClient: cli, DiagPollEvery: 10 * time.Millisecond})

	res, err := r.DiagNow(context.Background())
	if err != nil {
		t.Fatalf("DiagNow: %v", err)
	}
	if !strings.Contains(res, "2.8.2") {
		t.Errorf("expected final result body, got: %q", res)
	}
	if state.runHits != 1 {
		t.Errorf("expected exactly 1 run call, got %d", state.runHits)
	}
	if state.resultHits < 3 {
		t.Errorf("expected at least 3 result polls, got %d", state.resultHits)
	}
}

func TestRunner_DiagNow_ImmediateOK_NoTrigger(t *testing.T) {
	state := &awgmgrFakeState{
		resultBody: func(_ int) (int, string) {
			return 200, `{"system":{"appVersion":"2.8.2"}}`
		},
		runBody: func(_ int) (int, string) {
			t.Errorf("DiagRun should NOT be called when result is immediately OK")
			return 200, ""
		},
	}
	cli := awgmgrFakeMulti(t, state)
	r := actions.NewRunner(actions.RunnerDeps{AwgClient: cli, DiagPollEvery: 10 * time.Millisecond})

	if _, err := r.DiagNow(context.Background()); err != nil {
		t.Fatalf("DiagNow: %v", err)
	}
	if state.runHits != 0 {
		t.Errorf("expected 0 run calls, got %d", state.runHits)
	}
}

func TestRunner_DiagNow_NoReport_TimeoutEmitsTypedToken(t *testing.T) {
	state := &awgmgrFakeState{
		resultBody: func(_ int) (int, string) {
			return 400, `{"code":"NO_REPORT"}` // never resolves
		},
		runBody: func(_ int) (int, string) {
			return 200, `{"success":true,"data":{"status":"running"}}`
		},
	}
	cli := awgmgrFakeMulti(t, state)
	r := actions.NewRunner(actions.RunnerDeps{
		AwgClient:     cli,
		DiagPollEvery: 5 * time.Millisecond,
		DiagPollMax:   3, // tight cap for the test
	})
	_, err := r.DiagNow(context.Background())
	if err == nil || !strings.Contains(err.Error(), "DIAG_TIMEOUT") {
		t.Errorf("expected DIAG_TIMEOUT, got: %v", err)
	}
}

func TestRunner_DiagNow_NoReport_RunFails_BubblesError(t *testing.T) {
	state := &awgmgrFakeState{
		resultBody: func(_ int) (int, string) {
			return 400, `{"code":"NO_REPORT"}`
		},
		runBody: func(_ int) (int, string) {
			return 503, `{"error":true,"message":"awgmgr restarting"}`
		},
	}
	cli := awgmgrFakeMulti(t, state)
	r := actions.NewRunner(actions.RunnerDeps{AwgClient: cli, DiagPollEvery: 5 * time.Millisecond})
	_, err := r.DiagNow(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP_503") {
		t.Errorf("expected HTTP_503 bubble, got: %v", err)
	}
}
```

These tests assume a `DiagNow` method on the runner with the
trigger-and-poll behavior, plus configurable `DiagPollEvery` and
`DiagPollMax` on the deps struct. Existing tests like
`TestRunner_DiagNow_PassesThroughBody` should be renamed/migrated as
needed — if it exists with that exact name, update it to use the new
deps struct.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/agent/actions/ -run TestRunner_DiagNow_ -v`
Expected: FAIL — `r.DiagNow` does not exist (or the polling behavior is missing).

- [ ] **Step 4: Implement the trigger-and-poll loop**

In `internal/agent/actions/runner.go`:

1. If the runner type does not yet have a `DiagPollEvery` / `DiagPollMax`
   field, add them to the deps. Default values (in the constructor):
   `DiagPollEvery = 3 * time.Second`, `DiagPollMax = 12` (= 36s budget).

2. Add (or refactor) the `DiagNow` method:

```go
// DiagNow fetches the awg-manager diagnostic report. If awg-manager
// reports NO_REPORT, DiagNow auto-triggers a fresh run via DiagRun and
// polls DiagResult every r.DiagPollEvery for up to r.DiagPollMax
// iterations. Final outcomes:
//   - immediate 200       → return body, nil
//   - NO_REPORT → run → poll-succeeds → return body, nil
//   - NO_REPORT → run-error → return "", HTTP_NNN error
//   - NO_REPORT → run → poll-never-resolves → return "",
//     fmt.Errorf("DIAG_TIMEOUT: ...")
//   - other GET error → return "", that error (typed prefix preserved)
func (r *Runner) DiagNow(ctx context.Context) (string, error) {
	body, err := r.AwgClient.DiagResult(ctx)
	if err == nil {
		return body, nil
	}
	if !isNoReport(err) {
		return "", err
	}
	if runErr := r.AwgClient.DiagRun(ctx); runErr != nil {
		return "", runErr
	}
	every := r.DiagPollEvery
	if every <= 0 {
		every = 3 * time.Second
	}
	max := r.DiagPollMax
	if max <= 0 {
		max = 12
	}
	for i := 0; i < max; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(every):
		}
		body, err := r.AwgClient.DiagResult(ctx)
		if err == nil {
			return body, nil
		}
		if !isNoReport(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("DIAG_TIMEOUT: triggered but no result after %d iterations", max)
}

func isNoReport(err error) bool {
	return err != nil && strings.Contains(err.Error(), "NO_REPORT")
}
```

3. Rewire the dispatch site for `action="diag_now"` to call
   `r.DiagNow(ctx)` instead of `r.AwgClient.DiagResult(ctx)`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/agent/actions/ -v`
Expected: PASS (all existing + 4 new).

- [ ] **Step 6: Commit**

```bash
git add internal/agent/actions/runner.go internal/agent/actions/runner_test.go
git commit -m "feat(agent): diag_now auto-triggers DiagRun + polls on NO_REPORT"
```

---

## Task 6: Rewire `FormatCommandResult` to use Card + HintFor

**Files:**
- Modify: `internal/backend/alerts/command_result.go`
- Modify: `internal/backend/alerts/command_result_test.go`

- [ ] **Step 1: Update existing tests to match Card output**

Open `internal/backend/alerts/command_result_test.go`. The existing
tests assert raw substrings like `"❌ Не удалось:"`. Replace those
assertions with the new Card-based shape.

Replace `TestFormatCommandResult_DiagOK`:

```go
func TestFormatCommandResult_DiagOK_ParsedReport(t *testing.T) {
	r := wire.CommandResult{Status: "ok", Output: fixtureDiagSuccess}
	chunks := FormatCommandResult("diag_now", r, 3500)
	if len(chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(chunks))
	}
	body := chunks[0]
	if !strings.Contains(body, "📊 Диагностика") {
		t.Errorf("missing label: %s", body)
	}
	if !strings.Contains(body, "✅") {
		t.Errorf("expected ✅ in summary line: %s", body)
	}
	if !strings.Contains(body, "2.8.2") {
		t.Errorf("expected parsed appVersion: %s", body)
	}
	// Parsed diagnostic must NOT be wrapped in a code-fence — the body
	// is bullets, not monospaced data.
	if strings.Contains(body, "```") {
		t.Errorf("parsed diag must not use code-fence: %s", body)
	}
}

func TestFormatCommandResult_DiagOK_FallbackToRaw(t *testing.T) {
	// Output is not JSON → ParseDiagReport falls back; renderer should
	// drop into legacy code-fence wrapping so we don't lose information.
	r := wire.CommandResult{Status: "ok", Output: "diagnostics:\nall green"}
	chunks := FormatCommandResult("diag_now", r, 3500)
	if !strings.Contains(chunks[0], "```") {
		t.Errorf("unparseable diag should fall back to code-fence: %s", chunks[0])
	}
	if !strings.Contains(chunks[0], "all green") {
		t.Errorf("raw body must be preserved on fallback: %s", chunks[0])
	}
}

func TestFormatCommandResult_DiagErr_NoReportHint(t *testing.T) {
	r := wire.CommandResult{Status: "err", Output: "HTTP_400: NO_REPORT"}
	chunks := FormatCommandResult("diag_now", r, 3500)
	body := chunks[0]
	if !strings.Contains(body, "❌") {
		t.Errorf("error body missing ❌ badge: %s", body)
	}
	if !strings.Contains(body, "💡") {
		t.Errorf("error body missing hint marker 💡: %s", body)
	}
	if strings.Contains(body, "HTTP_400") {
		t.Errorf("typed prefix must NOT leak to user: %s", body)
	}
	if strings.Contains(body, "```") {
		t.Errorf("error must not be code-fenced: %s", body)
	}
}

func TestFormatCommandResult_DiagErr_DiagTimeoutHint(t *testing.T) {
	r := wire.CommandResult{Status: "err", Output: "DIAG_TIMEOUT: triggered but no result after 12 iterations"}
	chunks := FormatCommandResult("diag_now", r, 3500)
	body := chunks[0]
	if !strings.Contains(body, "36") {
		t.Errorf("timeout summary should mention 36с: %s", body)
	}
	if !strings.Contains(body, "💡") {
		t.Errorf("timeout body missing hint: %s", body)
	}
}
```

Update `TestFormatCommandResult_LockedAndTimeout`:

```go
func TestFormatCommandResult_LockedAndTimeout(t *testing.T) {
	for _, st := range []string{"locked", "timeout"} {
		r := wire.CommandResult{Status: st, Output: ""}
		chunks := FormatCommandResult("diag_now", r, 3500)
		body := chunks[0]
		if !strings.Contains(body, "❌") {
			t.Errorf("status=%s: missing ❌ badge: %s", st, body)
		}
		if !strings.Contains(body, "💡") {
			t.Errorf("status=%s: missing hint: %s", st, body)
		}
	}
}
```

Update `TestFormatCommandResult_ErrorPrefix`:

```go
func TestFormatCommandResult_ErrorBadge(t *testing.T) {
	r := wire.CommandResult{Status: "err", Output: "tunnel not found"}
	chunks := FormatCommandResult("restart_tunnel", r, 3500)
	if !strings.Contains(chunks[0], "❌") {
		t.Errorf("missing error badge: %s", chunks[0])
	}
}
```

Leave `TestFormatCommandResult_PingcheckOneLiner`,
`TestFormatCommandResult_RestartTunnelOK`,
`TestFormatCommandResult_OpkgPaginated`, and
`TestFormatCommandResult_HardCapAt4096` unchanged — they exercise
non-diag paths that retain their existing one-line / paginated shape.

- [ ] **Step 2: Run updated tests to verify they fail**

Run: `go test ./internal/backend/alerts/ -run TestFormatCommandResult -v`
Expected: FAIL — old formatter still emits "❌ Не удалось:" not Card.

- [ ] **Step 3: Rewrite FormatCommandResult**

Replace `internal/backend/alerts/command_result.go`:

```go
package alerts

import (
	"fmt"
	"strings"

	"github.com/anex/wg-monitor/pkg/wire"
)

const tgMaxMessageBytes = 4096

// FormatCommandResult renders a wire.CommandResult as one or more TG
// message bodies (chunks). Errors flow through HintFor; the diag_now
// success path goes through ParseDiagReport with a code-fence fallback
// for unrecognised JSON shapes.
func FormatCommandResult(action string, r wire.CommandResult, maxChars int) []string {
	if maxChars <= 0 || maxChars > tgMaxMessageBytes {
		maxChars = tgMaxMessageBytes - 200
	}
	label := commandLabelHuman(action)

	// Error / locked / timeout path → always a single Card.
	if r.Status != "ok" {
		token := strings.ToUpper(r.Status) // "ERR", "LOCKED", "TIMEOUT"
		hintInput := token
		if r.Status == "err" {
			hintInput = r.Output
		}
		summary, hint := HintFor(action, hintInput)
		card := Card{Badge: "❌", Label: label, Summary: summary, Hint: hint}
		return []string{card.Render(CardOpts{MaxBytes: maxChars})}
	}

	switch action {
	case "diag_now":
		return formatDiagSuccess(label, r.Output, maxChars)
	case "pingcheck_now":
		summary := strings.TrimSpace(r.Output)
		card := Card{
			Badge:   "▶",
			Label:   label,
			Summary: fmt.Sprintf("%s (за %dмс)", summary, r.DurationMs),
		}
		return []string{card.Render(CardOpts{MaxBytes: maxChars})}
	case "restart_tunnel", "tunnel_enable", "tunnel_disable":
		card := Card{Badge: "🔁", Label: label, Summary: strings.TrimSpace(r.Output)}
		return []string{card.Render(CardOpts{MaxBytes: maxChars})}
	case "check_via_tunnel", "check_direct":
		// Body is already a human-readable multi-line report from
		// actions.CheckViaTunnel/CheckDirect. Pass through unchanged.
		return []string{strings.TrimSpace(r.Output)}
	case "opkg_upgrade":
		// SmartUpgrade returns a human-readable multi-line summary;
		// no Card wrapping (it would over-formalise a multi-paragraph
		// report). Paginate as before if it overflows.
		full := fmt.Sprintf("%s:\n\n%s", label, r.Output)
		if len(full) <= maxChars {
			return []string{full}
		}
		return paginate(label+":", r.Output, maxChars)
	}
	// Defensive fallback for unknown actions.
	full := fmt.Sprintf("%s: %s", label, r.Output)
	if len(full) <= maxChars {
		return []string{full}
	}
	return paginate(label+":", r.Output, maxChars)
}

// formatDiagSuccess parses the awg-manager JSON report into a Card.
// If the body can't be parsed, falls back to code-fenced raw dump.
func formatDiagSuccess(label, body string, maxChars int) []string {
	summary, bullets, fallback := ParseDiagReport(body)
	if fallback {
		// Legacy behaviour: code-fence the raw body, paginate on overflow.
		full := fmt.Sprintf("%s:\n\n```\n%s\n```", label, body)
		if len(full) <= maxChars {
			return []string{full}
		}
		return paginate(label+":", body, maxChars)
	}
	card := Card{
		Badge:   "📊",
		Label:   label,
		Summary: summary,
		Details: strings.Join(bullets, "\n"),
		Hint:    "Полный JSON-отчёт доступен по кнопке ниже.",
	}
	return []string{card.Render(CardOpts{MaxBytes: maxChars})}
}

// paginate splits body into chunks each prefixed with "(K/N) <header>".
// (Unchanged from prior implementation — kept verbatim.)
func paginate(header, body string, maxChars int) []string {
	per := maxChars
	if per < 100 {
		per = 100
	}
	runes := []rune(body)
	var chunks []string
	if len(runes) == 0 {
		chunks = []string{""}
	} else {
		for i := 0; i < len(runes); i += per {
			end := i + per
			if end > len(runes) {
				end = len(runes)
			}
			chunks = append(chunks, string(runes[i:end]))
		}
	}
	out := make([]string, len(chunks))
	for i, c := range chunks {
		rendered := fmt.Sprintf("(%d/%d) %s\n%s", i+1, len(chunks), header, c)
		if len(rendered) > tgMaxMessageBytes {
			rr := []rune(rendered)
			for len(string(rr)) > tgMaxMessageBytes && len(rr) > 0 {
				rr = rr[:len(rr)-1]
			}
			rendered = string(rr)
		}
		out[i] = rendered
	}
	return out
}

func commandLabelHuman(action string) string {
	switch action {
	case "diag_now":
		return "📊 Диагностика"
	case "pingcheck_now":
		return "▶ Тест связи"
	case "restart_tunnel":
		return "🔁 Перезапуск туннеля"
	case "opkg_upgrade":
		return "⬆ Обновление пакетов"
	case "force_recheck":
		return "🔁 Force recheck"
	case "check_via_tunnel":
		return "🌍 Через тоннель"
	case "check_direct":
		return "🇷🇺 Напрямую"
	case "tunnel_enable":
		return "▶ Включить туннель"
	case "tunnel_disable":
		return "⏸ Выключить туннель"
	}
	return action
}
```

Make sure the test file imports the fixture constant from
`diag_report_test.go` (same package — direct reference is fine).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backend/alerts/ -v`
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add internal/backend/alerts/command_result.go internal/backend/alerts/command_result_test.go
git commit -m "refactor(alerts): FormatCommandResult through Card + HintFor + diag parser"
```

---

## Task 7: Diag raw cache + `diag_raw` callback

**Files:**
- Create: `internal/backend/callbacks/diag_cache.go`
- Test: `internal/backend/callbacks/diag_cache_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/backend/callbacks/diag_cache_test.go`:

```go
package callbacks

import (
	"testing"
	"time"
)

func TestDiagCache_PutGet(t *testing.T) {
	c := newDiagCache()
	tok := c.Put("raw json body", 5*time.Minute)
	if tok == "" {
		t.Fatal("Put returned empty token")
	}
	got, ok := c.Get(tok)
	if !ok || got != "raw json body" {
		t.Errorf("Get(%q) = (%q, %v), want (\"raw json body\", true)", tok, got, ok)
	}
}

func TestDiagCache_TokenIsHex8(t *testing.T) {
	c := newDiagCache()
	tok := c.Put("x", time.Minute)
	if len(tok) != 8 {
		t.Errorf("token len=%d, want 8", len(tok))
	}
	for _, r := range tok {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("non-hex char %q in token %q", r, tok)
		}
	}
}

func TestDiagCache_ExpiresAfterTTL(t *testing.T) {
	c := newDiagCache()
	tok := c.Put("x", 5*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	if _, ok := c.Get(tok); ok {
		t.Error("expected Get to fail after TTL elapsed")
	}
}

func TestDiagCache_MultiGetReturnsBodyEachTime(t *testing.T) {
	// Button is re-tappable until TTL — Get does not consume.
	c := newDiagCache()
	tok := c.Put("body", time.Minute)
	for i := 0; i < 3; i++ {
		if got, ok := c.Get(tok); !ok || got != "body" {
			t.Errorf("call %d: got=(%q,%v), want (\"body\",true)", i, got, ok)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backend/callbacks/ -run TestDiagCache -v`
Expected: FAIL — `newDiagCache` undefined.

- [ ] **Step 3: Implement the cache**

Create `internal/backend/callbacks/diag_cache.go`:

```go
package callbacks

import (
	cryptoRand "crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// diagCache stores the raw JSON body of a diag_now success result so
// the "📄 Полный отчёт" inline button can fetch it without re-running
// the diagnostic. Tokens are 8 hex chars (4 random bytes). TTL is set
// per Put; expired entries are evicted lazily on Get.
type diagCache struct {
	mu sync.Mutex
	m  map[string]diagCacheEntry
}

type diagCacheEntry struct {
	body      string
	expiresAt time.Time
}

func newDiagCache() *diagCache {
	return &diagCache{m: make(map[string]diagCacheEntry)}
}

// Put stores body under a fresh 8-hex token and returns the token.
func (c *diagCache) Put(body string, ttl time.Duration) string {
	tok := newDiagToken()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[tok] = diagCacheEntry{body: body, expiresAt: time.Now().Add(ttl)}
	return tok
}

// Get returns the body for token if present and not expired.
func (c *diagCache) Get(token string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[token]
	if !ok {
		return "", false
	}
	if time.Now().After(e.expiresAt) {
		delete(c.m, token)
		return "", false
	}
	return e.body, true
}

func newDiagToken() string {
	var b [4]byte
	_, _ = cryptoRand.Read(b[:])
	return hex.EncodeToString(b[:])
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/backend/callbacks/ -run TestDiagCache -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/backend/callbacks/diag_cache.go internal/backend/callbacks/diag_cache_test.go
git commit -m "feat(callbacks): diagCache for retrievable raw diag bodies"
```

---

## Task 8: `diag_raw` parse + dispatch + result-side wiring

**Files:**
- Modify: `internal/backend/callbacks/parse.go`
- Modify: `internal/backend/callbacks/parse_test.go`
- Modify: `internal/backend/callbacks/router.go`
- Modify: `internal/backend/callbacks/router_test.go`
- Modify: `internal/backend/alerts/command_result.go` (kb construction helper)
- Modify: `internal/backend/callbacks/notifier_test.go` (or wherever command-result delivery is staged — see Step 1)

- [ ] **Step 1: Locate the command-result delivery site**

Run: `grep -rn 'FormatCommandResult' internal/backend/`

The caller that turns chunks into outgoing TG messages is the place
where (a) we add inline-keyboard buttons under the diag result, (b)
we put the raw body into `diagCache`. Note the file + function name
before proceeding.

- [ ] **Step 2: Extend Args + Parse for `diag_raw`**

Edit `internal/backend/callbacks/parse.go`:

In the `Args` struct (after `OpkgRepairToken`), add:

```go
// DiagRawToken is the 8-hex token of a cached diag JSON body retrieved
// by the "📄 Полный отчёт" button under a diag result.
DiagRawToken string
```

In `validActions`, add `"diag_raw": true`.

In the `Parse` function, after the `opkg_disable` case, add:

```go
case "diag_raw":
    if len(parts) < 4 || parts[3] == "" {
        return Args{}, fmt.Errorf("diag_raw requires token: %q", data)
    }
    a.DiagRawToken = parts[3]
```

- [ ] **Step 3: Write parse-level tests**

Append to `internal/backend/callbacks/parse_test.go`:

```go
func TestParse_DiagRaw(t *testing.T) {
	a, err := Parse("diag_raw:42:_panel_:deadbeef")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Action != "diag_raw" {
		t.Errorf("action=%q", a.Action)
	}
	if a.UserID != 42 {
		t.Errorf("UserID=%d", a.UserID)
	}
	if a.DiagRawToken != "deadbeef" {
		t.Errorf("DiagRawToken=%q", a.DiagRawToken)
	}
}

func TestParse_DiagRaw_MissingToken(t *testing.T) {
	if _, err := Parse("diag_raw:42:_panel_:"); err == nil {
		t.Errorf("expected error on empty token")
	}
}
```

- [ ] **Step 4: Implement the diag_raw dispatch in Router**

Edit `internal/backend/callbacks/router.go`:

1. Add a field `diagCache *diagCache` on the `Router` struct. Initialise
   it in `NewRouterWithSink` (look for the place where similar caches
   like `RoutesCache` are wired).
2. In `HandleCallback`, add a case in the action switch (next to the
   existing `case "opkg_disable":` block):

```go
case "diag_raw":
    body, ok := r.diagCache.Get(args.DiagRawToken)
    if !ok {
        _ = r.tg.AnswerCallbackQuery(ctx, q.ID, "отчёт уже не доступен (5 мин TTL)")
        return
    }
    _ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
    // Send the raw JSON as a NEW message in the same topic, code-fenced.
    full := "📄 Полный отчёт диагностики:\n\n```\n" + body + "\n```"
    chunks := []string{full}
    if len(full) > 4000 {
        chunks = paginateRaw(body, "📄 Полный отчёт диагностики:", 4000)
    }
    for _, c := range chunks {
        _, _ = r.tg.SendMessage(ctx, q.Message.Chat.ID, q.Message.MessageThreadID, c, "", nil)
    }
    return
```

(`paginateRaw` is a thin local wrapper around the existing alerts
paginate logic — if it's already exported, import and reuse; otherwise
copy the simple loop inline.)

3. Expose a small `(r *Router) StageDiagResult(body string)` returning
   the new token, so the command-result delivery layer can stage the
   cache entry before rendering. Add the receiver near the other
   exported staging helpers; if no such pattern exists, add an
   internal `(r *Router) diagCache *diagCache` accessor for the
   notifier instead.

- [ ] **Step 5: Wire the result-side keyboard in command_result.go**

Add to `internal/backend/alerts/command_result.go`:

```go
// DiagResultButtons returns the inline keyboard rows that should sit
// under a rendered diag_now Card. token is the diagCache token for
// the raw body; userID is the router target (encoded into callbacks).
// Status must match wire.CommandResult.Status of the result that's
// being rendered: "ok" → primary "Полный отчёт" + restart + close;
// non-ok → retry + help + close.
func DiagResultButtons(status string, userID int64, rawToken string) [][]CallbackButton {
	if status == "ok" {
		return [][]CallbackButton{
			{
				{Text: "📄 Полный отчёт", CallbackData: fmt.Sprintf("diag_raw:%d:_panel_:%s", userID, rawToken)},
				{Text: "🔁 Перезапустить диагностику", CallbackData: fmt.Sprintf("diag_now:%d:_menu", userID)},
			},
			{
				{Text: "✖ Закрыть", CallbackData: fmt.Sprintf("routes_close:%d:_panel_", userID)},
			},
		}
	}
	return [][]CallbackButton{
		{
			{Text: "🔁 Попробовать снова", CallbackData: fmt.Sprintf("diag_now:%d:_menu", userID)},
			{Text: "ℹ Помощь", CallbackData: fmt.Sprintf("panel:0:help:diag")},
		},
		{
			{Text: "✖ Закрыть", CallbackData: fmt.Sprintf("routes_close:%d:_panel_", userID)},
		},
	}
}

// CallbackButton is a transport-agnostic copy of tg.InlineKeyboardButton
// — alerts package cannot import internal/backend/tg to stay layer-
// pure. The notifier layer is responsible for converting.
type CallbackButton struct {
	Text         string
	CallbackData string
}
```

(If a similar transport-agnostic type already exists in this package,
reuse it instead.)

- [ ] **Step 6: Update the command-result notifier to stage cache + attach keyboard**

In the file located in Step 1 (the call site of `FormatCommandResult`
that actually sends the message), update the diag_now branch:

```go
// (Pseudocode — adapt to the actual notifier interface in the codebase.)
chunks := alerts.FormatCommandResult(action, result, 3500)
var kb [][]alerts.CallbackButton
if action == "diag_now" {
    rawToken := r.diagCache.Put(result.Output, 5*time.Minute)
    kb = alerts.DiagResultButtons(result.Status, userID, rawToken)
}
// send chunks; attach kb to last chunk via the existing TG client
// helper. If no helper exists, build tg.InlineKeyboardMarkup inline.
```

- [ ] **Step 7: Write the dispatch test**

Append to `internal/backend/callbacks/router_test.go`:

```go
func TestRouter_DiagRaw_ServesCachedBodyOnce(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouterWithSink(d, f, &fakeEnqueuer{}, Config{ChatID: -100, AdminUserID: 12345})
	tok := r.diagCache.Put("RAW_BODY", time.Minute)

	q := &tg.CallbackQuery{
		ID:      "cbk",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}},
		Data:    "diag_raw:" + itoa(uid) + ":_panel_:" + tok,
	}
	r.HandleCallback(context.Background(), q)

	if len(f.sentMsgs) != 1 {
		t.Fatalf("want 1 sent raw-report message, got %d", len(f.sentMsgs))
	}
	if !strings.Contains(f.sentMsgs[0], "RAW_BODY") {
		t.Errorf("raw body missing from sent message: %s", f.sentMsgs[0])
	}
}

func TestRouter_DiagRaw_ExpiredTokenAnswersToast(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouterWithSink(d, f, &fakeEnqueuer{}, Config{ChatID: -100, AdminUserID: 12345})

	q := &tg.CallbackQuery{
		ID:      "cbk",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}},
		Data:    "diag_raw:" + itoa(uid) + ":_panel_:deadbeef", // never staged
	}
	r.HandleCallback(context.Background(), q)

	if len(f.answers) != 1 || !strings.Contains(f.answers[0], "уже не доступен") {
		t.Errorf("expected expired-toast, got %v", f.answers)
	}
}
```

- [ ] **Step 8: Run all callback tests**

Run: `go test ./internal/backend/callbacks/ -v`
Expected: PASS (existing + new tests).

- [ ] **Step 9: Commit**

```bash
git add internal/backend/callbacks/parse.go \
        internal/backend/callbacks/parse_test.go \
        internal/backend/callbacks/router.go \
        internal/backend/callbacks/router_test.go \
        internal/backend/alerts/command_result.go
git commit -m "feat(callbacks): diag_raw button + DiagResultButtons keyboard"
```

---

## Task 9: Replace admin-side raw `err.Error()` with HintFor

**Files:**
- Modify: `internal/backend/callbacks/admin_topics.go`
- Modify: `internal/backend/callbacks/access_panel.go`
- Modify: `internal/backend/callbacks/panel_hub.go`
- Test: small additions in respective `_test.go` files

- [ ] **Step 1: Write the failing tests**

Open `internal/backend/callbacks/admin_topics_test.go`. Locate a test
that exercises `adminEnsureTopics` failure (or add a new one).
Add:

```go
func TestAdmin_EnsureTopics_FailsRendersHint(t *testing.T) {
	// Force DB error by closing the DB before calling.
	d, _ := newTestDB(t)
	d.Close()
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	msg := &tg.Message{
		Chat:    tg.Chat{ID: -100},
		From:    tg.User{ID: 42},
		Text:    "/ensure_topics",
	}
	r.HandleMessage(context.Background(), msg)
	if len(f.sentMsgs) == 0 {
		t.Fatal("expected an error reply")
	}
	body := f.sentMsgs[0]
	if !strings.Contains(body, "❌") || !strings.Contains(body, "💡") {
		t.Errorf("error reply should contain ❌ badge AND 💡 hint, got: %s", body)
	}
}
```

Similarly add `TestAccess_HomeDBErrorRendersHint` in
`access_panel_test.go`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/backend/callbacks/ -run 'EnsureTopics_FailsRendersHint|HomeDBErrorRendersHint' -v`
Expected: FAIL — current code emits raw `err.Error()` without 💡.

- [ ] **Step 3: Edit admin_topics.go to use HintFor**

Open `internal/backend/callbacks/admin_topics.go`. Find every place
returning a user reply containing `"❌ "` followed by `err.Error()`.
For each, replace the construction with `alerts.HintFor(action, raw)`
and the Card render. Example for `adminEnsureTopics`:

Before:
```go
if err != nil {
    r.adminReply(ctx, m, "❌ ошибка чтения пользователей: "+err.Error())
    return
}
```

After:
```go
if err != nil {
    sum, hint := alerts.HintFor("admin_ensure_topics", err.Error())
    card := alerts.Card{Badge: "❌", Label: "Не удалось получить роутеры", Summary: sum, Hint: hint}
    r.adminReply(ctx, m, card.Render(alerts.CardOpts{MaxBytes: 3500}))
    return
}
```

Repeat at the other 3 sites in this file. Add
`"github.com/anex/wg-monitor/internal/backend/alerts"` to imports if
not present.

- [ ] **Step 4: Edit access_panel.go**

In `accessHomeMessage` and any inline error site:

Before:
```go
return "👥 Управление доступом\n\nНе удалось прочитать роутеров: " + err.Error(), ...
```

After:
```go
sum, hint := alerts.HintFor("access_home", err.Error())
card := alerts.Card{Badge: "❌", Label: "👥 Управление доступом", Summary: sum, Hint: hint}
return card.Render(alerts.CardOpts{MaxBytes: 3500}), tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
    {{Text: "« Назад", CallbackData: "panel:0:home"}},
}}
```

- [ ] **Step 5: Edit panel_hub.go**

Find error sites at the lines flagged in the audit (around the
"роутер не найден" / "не удалось опубликовать" branches). Convert the
short toast text to come from `HintFor`'s summary only (keep ≤ 200
chars since these are AnswerCallbackQuery toasts). Where a follow-up
edited-message body exists, set its body via `Card.Render`.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/backend/callbacks/ -v`
Expected: PASS (new and existing).

- [ ] **Step 7: Commit**

```bash
git add internal/backend/callbacks/admin_topics.go \
        internal/backend/callbacks/access_panel.go \
        internal/backend/callbacks/panel_hub.go \
        internal/backend/callbacks/admin_topics_test.go \
        internal/backend/callbacks/access_panel_test.go
git commit -m "refactor(callbacks): admin error sites use Card + HintFor"
```

---

## Task 10: `users.HasAnyOperatorOrOwnerBinding`

**Files:**
- Modify: `internal/backend/db/users.go`
- Test: `internal/backend/db/users_test.go` (or new dedicated file)

- [ ] **Step 1: Write the failing test**

Append to `internal/backend/db/users_test.go`:

```go
func TestUsers_HasAnyOperatorOrOwnerBinding(t *testing.T) {
	d := newTestDBHelper(t) // reuse existing helper; replace with the one this file already uses
	uid, _ := d.Users().Insert("alpha", "tok", "1.1.1.1", "awg11")

	// Stranger has no bindings yet.
	if has, err := d.Users().HasAnyOperatorOrOwnerBinding(999); err != nil {
		t.Fatalf("err: %v", err)
	} else if has {
		t.Error("stranger should not have a binding")
	}

	// As owner.
	_ = d.Users().SetTelegramUserID(uid, 100)
	if has, _ := d.Users().HasAnyOperatorOrOwnerBinding(100); !has {
		t.Error("owner 100 should have a binding")
	}

	// As operator (new router, different owner).
	uid2, _ := d.Users().Insert("beta", "tok2", "2.2.2.2", "awg11")
	_ = d.Users().SetTelegramUserID(uid2, 200)
	_ = d.RouterOperators().Add(uid2, 300, 42)
	if has, _ := d.Users().HasAnyOperatorOrOwnerBinding(300); !has {
		t.Error("operator 300 should have a binding")
	}
}
```

Use whatever test-db helper this file uses (look at the top of
`users_test.go` for the existing pattern — likely `setupDB(t)` or
similar — and reuse it).

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/backend/db/ -run TestUsers_HasAnyOperatorOrOwnerBinding -v`
Expected: FAIL — method undefined.

- [ ] **Step 3: Add the method**

Append to `internal/backend/db/users.go`:

```go
// HasAnyOperatorOrOwnerBinding reports whether the given Telegram
// user id is bound to any router as owner (users.telegram_user_id)
// or listed in any router_operators row. Used by /help to pick
// admin vs operator vs none content.
func (u *UsersRepo) HasAnyOperatorOrOwnerBinding(tgUserID int64) (bool, error) {
	var one int
	err := u.d.db.QueryRow(
		`SELECT 1 WHERE EXISTS (SELECT 1 FROM users WHERE telegram_user_id = ?)
		            OR EXISTS (SELECT 1 FROM router_operators WHERE telegram_user_id = ?)`,
		tgUserID, tgUserID,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("HasAnyOperatorOrOwnerBinding: %w", err)
	}
	return true, nil
}
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./internal/backend/db/ -run TestUsers_HasAnyOperatorOrOwnerBinding -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/db/users.go internal/backend/db/users_test.go
git commit -m "feat(db): HasAnyOperatorOrOwnerBinding for /help role detection"
```

---

## Task 11: `/help` command

**Files:**
- Create: `internal/backend/callbacks/help.go`
- Test: `internal/backend/callbacks/help_test.go`
- Modify: `internal/backend/callbacks/admin_topics.go` (recognise `/help`)
- Modify: `internal/backend/callbacks/router.go` (operator path lets `/help` through)

- [ ] **Step 1: Write the failing tests**

Create `internal/backend/callbacks/help_test.go`:

```go
package callbacks

import (
	"context"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/tg"
)

func TestHelp_AdminGetsFullBody(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	msg := &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 42}, Text: "/help"}
	r.HandleMessage(context.Background(), msg)

	if len(f.sentMsgs) != 1 {
		t.Fatalf("want 1 help reply, got %d", len(f.sentMsgs))
	}
	body := f.sentMsgs[0]
	for _, want := range []string{"Алерты", "Кнопки в топике", "Админ-команды", "/panel"} {
		if !strings.Contains(body, want) {
			t.Errorf("admin /help missing %q in body:\n%s", want, body)
		}
	}
}

func TestHelp_OperatorGetsOperatorBody(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 55); err != nil {
		t.Fatal(err)
	}
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 42)

	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	tid := int64(55)
	msg := &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 200}, MessageThreadID: &tid, Text: "/help"}
	r.HandleMessage(context.Background(), msg)

	if len(f.sentMsgs) != 1 {
		t.Fatalf("want 1 help reply, got %d", len(f.sentMsgs))
	}
	body := f.sentMsgs[0]
	if strings.Contains(body, "/panel") || strings.Contains(body, "Админ-команды") {
		t.Errorf("operator help must NOT include admin section:\n%s", body)
	}
	if !strings.Contains(body, "Кнопки в топике") {
		t.Errorf("operator help must include 'Кнопки в топике':\n%s", body)
	}
}

func TestHelp_StrangerDeniedWithFriendlyMessage(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	msg := &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 999}, Text: "/help"}
	r.HandleMessage(context.Background(), msg)

	// Stranger has no operator gate satisfied (no per_router topic + no
	// bindings); HandleMessage drops them silently. /help via DM should
	// also reach a no-rights path — but that's a future iteration. For
	// now we just confirm no admin body leaks.
	for _, m := range f.sentMsgs {
		if strings.Contains(m, "Админ-команды") {
			t.Errorf("stranger must not see admin body, got: %s", m)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/backend/callbacks/ -run TestHelp_ -v`
Expected: FAIL — `/help` not recognised yet.

- [ ] **Step 3: Create help.go with content + dispatcher**

Create `internal/backend/callbacks/help.go`:

```go
package callbacks

import (
	"context"
	"strings"

	"github.com/anex/wg-monitor/internal/backend/tg"
)

const helpCommonBody = `ℹ Помощь по боту

📛 Алерты:
✅ — всё работает; 🟡 — soft (1 falure not yet escalated); 🔴 — hard (escalated); 📵 — роутер недоступен.

🎛 Кнопки в топике (per_router):
📊 Что происходит? — короткий smart-reply (state + советы).
🎛 Туннели — снапшот по туннелям, кнопки enable/disable.
🛣 Маршруты — DNS/Static маршруты + rebind между туннелями.
🛠 Обслуживание — version_audit, restart awg-mgr/hrneo/router, прошивка.
🌍 Через тоннель? / 🇷🇺 Напрямую? — проверка связности.
⬆ Обновить пакеты — opkg update + upgrade на роутере.

🔘 Inline-кнопки под HARD-алертом:
⏸ Тише 1ч/4ч/24ч, ✅ Понял, 🔇 Тихо до утра, 📋 История за 24ч,
🔁 Перезапуск туннеля, 📊 Диагностика, ▶ Тест связи.`

const helpAdminBody = `

🛡 Админ-команды:
/panel — главный хаб (🛠 Обслуживание · 🛣 Маршруты · 📊 Status · 🪄 Оживить топики · 👥 Доступ).
/this_is <nickname> — привязать текущий топик к роутеру.
/ensure_topics — создать недостающие топики.
/recreate_topic — пересоздать топик текущего роутера.
/topic_help — alias на /help (старая команда).`

const helpOperatorBody = `

👤 Ты — оператор. Можешь всё то же, что владелец роутера (см. список выше), кроме админ-команд (/panel и т.д.). Слэш-команды без эффекта.`

func (r *Router) handleHelpCommand(ctx context.Context, m *tg.Message) {
	body := helpCommonBody
	switch r.helpRole(m.From.ID) {
	case "admin":
		body += helpAdminBody
	case "operator":
		body += helpOperatorBody
	}
	if _, err := r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, body, "", nil); err != nil {
		// Fail-silent — /help is not critical.
		return
	}
}

func (r *Router) helpRole(userID int64) string {
	if r.cfg.AdminUserID != 0 && userID == r.cfg.AdminUserID {
		return "admin"
	}
	has, err := r.d.Users().HasAnyOperatorOrOwnerBinding(userID)
	if err == nil && has {
		return "operator"
	}
	return "none"
}

// trim avoids unused-import warnings if strings is dropped during edit.
var _ = strings.TrimSpace
```

- [ ] **Step 4: Hook /help into admin slash dispatcher**

Edit `internal/backend/callbacks/admin_topics.go`, `handleAdminCommand`,
add a case BEFORE the existing `/topic_help`:

```go
case "/help":
    r.handleHelpCommand(ctx, m)
    return true
```

Add `/topic_help` already exists — leave it as the deprecated alias.

- [ ] **Step 5: Let operators reach `/help`**

Edit `internal/backend/callbacks/router.go`, in the operator-gate
branch added in the rc25 fix. Right after the operator passes the
gate (before the switch on `m.Text`), add:

```go
// Operators get /help too; other slash commands stay admin-only.
if strings.TrimSpace(m.Text) == "/help" {
    r.handleHelpCommand(ctx, m)
    return
}
```

Make sure `strings` is in imports (it likely already is).

- [ ] **Step 6: Run tests to verify pass**

Run: `go test ./internal/backend/callbacks/ -run TestHelp_ -v`
Expected: PASS (3 tests).

- [ ] **Step 7: Commit**

```bash
git add internal/backend/callbacks/help.go \
        internal/backend/callbacks/help_test.go \
        internal/backend/callbacks/admin_topics.go \
        internal/backend/callbacks/router.go
git commit -m "feat(callbacks): /help with admin / operator / none role-aware body"
```

---

## Task 12: Per-panel "ℹ Помощь" buttons + `panel:0:help:<screen>` dispatch

**Files:**
- Create: `internal/backend/tg/help_panels.go`
- Test: `internal/backend/tg/help_panels_test.go`
- Modify: `internal/backend/tg/maint_panel.go` (append row)
- Modify: `internal/backend/tg/routes_panel.go` (append row)
- Modify: `internal/backend/tg/tunnels_panel.go` (append row)
- Modify: `internal/backend/callbacks/parse.go` (extend `panel` screens)
- Modify: `internal/backend/callbacks/panel_hub.go` (dispatch help screen)

- [ ] **Step 1: Write the failing test**

Create `internal/backend/tg/help_panels_test.go`:

```go
package tg

import (
	"strings"
	"testing"
)

func TestHelpForScreen_KnownScreens(t *testing.T) {
	for _, screen := range []string{"maint", "routes", "tunnels", "access", "diag", "status"} {
		body := HelpForScreen(screen)
		if body == "" {
			t.Errorf("screen %q: empty help body", screen)
		}
		if len(body) > 3500 {
			t.Errorf("screen %q: help body too long (%d > 3500)", screen, len(body))
		}
	}
}

func TestHelpForScreen_UnknownReturnsGeneric(t *testing.T) {
	body := HelpForScreen("totally_made_up")
	if !strings.Contains(body, "Помощь") {
		t.Errorf("unknown screen should still return some help text, got: %q", body)
	}
}
```

- [ ] **Step 2: Implement help_panels.go**

Create `internal/backend/tg/help_panels.go`:

```go
package tg

// HelpForScreen returns the inline-help body for a given panel screen.
// Used by the "ℹ Помощь" button: tap → EditMessageText replaces panel
// body with this text. "« Назад" returns to the panel.
func HelpForScreen(screen string) string {
	switch screen {
	case "maint":
		return `🛠 Обслуживание — справка

🔁 Restart hrneo — перезапустить HydraRoute-Neo (DNS/routing маршрутизатор). Безопасно; пакеты задерживаются на ~5с.
🔁 Restart awg-mgr — перезапустить менеджер AmneziaWG. Туннели не падают (системные), но awg-mgr API недоступен ~10с.
🔁 Reboot router — полная перезагрузка. ~2–3 мин downtime. Используется кулдаун 10 мин.
📦 Прошивка — установить новую версию KeeneticOS. Включает reboot. Кулдаун 60 мин.
🔄 Проверить апдейты — заново снять отчёт о доступных версиях.`
	case "routes":
		return `🛣 Маршруты — справка

Снимок DNS- и Static-маршрутов HydraRoute-Neo, сгруппированный по туннелям.
🔄 Rebind — массово переключить все правила одного туннеля на другой. WAN/system правила НЕ трогаются.
🔁 Обновить — заново снять снимок.`
	case "tunnels":
		return `🎛 Туннели — справка

Снимок состояния всех туннелей: ✅ up, 🔴 down, ⏸ disabled.
▶/⏸ — включить/выключить конкретный туннель (awg-mgr enable/disable).
🔁 Перезагрузить awg-mgr — рестарт менеджера.
🔄 Обновить — заново снять снимок.`
	case "access":
		return `👥 Доступ — справка

Управление операторами роутеров. Каждый роутер имеет одного owner'а и опциональный whitelist дополнительных TG user'ов.
➕ Добавить оператора — FSM: forward сообщение от человека в личку боту, ИЛИ числовой TG ID.
✖ — удалить запись (owner'а или оператора).`
	case "diag":
		return `📊 Диагностика — справка

Короткий отчёт о системе: версия awg-manager, состояние WAN, туннели, DNS.
Запуск ~30–60с. Не меняет состояние, только читает.
📄 Полный отчёт — JSON-дамп (для отладки).
🔁 Перезапустить — снять отчёт заново.`
	case "status":
		return `📊 Status — справка

Публикует smart-reply (📊 Что происходит?) в топик каждого роутера. Useful когда нужно одним тапом получить срез по всем роутерам.`
	}
	return "ℹ Помощь по этому экрану ещё не написана."
}
```

- [ ] **Step 3: Add "ℹ Помощь" row helper**

Append to `internal/backend/tg/help_panels.go`:

```go
// HelpRowFor returns a single-row inline-keyboard suffix containing the
// "ℹ Помощь" button for a given panel screen. Designed to be appended
// to existing panel keyboards just before the "✖ Закрыть" row.
func HelpRowFor(screen string) []InlineKeyboardButton {
	return []InlineKeyboardButton{
		{Text: "ℹ Помощь", CallbackData: "panel:0:help:" + screen},
	}
}
```

- [ ] **Step 4: Wire the row into existing panels**

For each of these renderers, append `HelpRowFor("<screen>")` to the
rows slice JUST BEFORE the existing "✖ Закрыть" / "🔁 Обновить" row.

Files & insertion screens:

- `internal/backend/tg/maint_panel.go::MaintPanelKeyboard` — screen `"maint"`
- `internal/backend/tg/routes_panel.go::RoutesPanelKeyboard` — screen `"routes"`
- `internal/backend/tg/tunnels_panel.go::TunnelsPanelKeyboard` — screen `"tunnels"`

Pattern:
```go
rows = append(rows, HelpRowFor("maint"))
rows = append(rows, []InlineKeyboardButton{
    {Text: "✖ Закрыть", CallbackData: cd("maint_close", "_panel_")},
})
```

- [ ] **Step 5: Update existing panel-keyboard tests**

Existing tests like `TestMaintPanelKeyboard_CallbackData` may count
rows or assert specific callback strings. Update them to expect the
new `panel:0:help:<screen>` callback in the new row. Run:

```bash
go test ./internal/backend/tg/ -v
```

Fix any assertion failures by adding the new "ℹ Помощь" expected
callback to the test's `want` set.

- [ ] **Step 6: Extend Parse for `panel:0:help:<screen>`**

Edit `internal/backend/callbacks/parse.go`:

In `validPanelScreens`, add: `"help": true`.

In the `if action == "panel"` block, AFTER the existing `if screen ==
"kind" || screen == "push"` check, add:

```go
if screen == "help" {
    if len(parts) < 4 || parts[3] == "" {
        return Args{}, fmt.Errorf("panel help requires screen: %q", data)
    }
    // Reuse PanelKind as transport for the help-target screen name.
    a.PanelKind = parts[3]
}
```

Then in `validKinds`, add `"maint": true, "routes": true, "status":
true` already there — also add `"tunnels": true, "access": true,
"diag": true` so help screen names pass the kind validator. (Or
introduce a separate `validHelpScreens` map; pick whichever keeps the
diff small.)

- [ ] **Step 7: Dispatch in panel_hub.go**

In `internal/backend/callbacks/panel_hub.go`, locate
`handlePanelCallback`. Add a case for screen `"help"`:

```go
case "help":
    body := tg.HelpForScreen(args.PanelKind)
    backRow := []tg.InlineKeyboardButton{
        {Text: "« Назад к панели", CallbackData: "panel:0:kind:" + backKindFor(args.PanelKind)},
        {Text: "✖ Закрыть", CallbackData: "panel:0:close"},
    }
    kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{backRow}}
    _ = r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, body, "", &kb)
    _ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
    return
```

Add helper near the bottom of the same file:

```go
// backKindFor maps a help screen to the panel kind to return to.
// Help screens directly mirror panel kinds for maint/routes/status;
// tunnels/access/diag don't have a panel kind, so they go back to
// hub home.
func backKindFor(screen string) string {
    switch screen {
    case "maint", "routes", "status":
        return screen
    }
    return "home"
}
```

If the back callback for "home" doesn't match the existing panel-home
pattern, adjust to match (likely `panel:0:home`).

- [ ] **Step 8: Add a dispatch test**

Append to `internal/backend/callbacks/panel_hub_test.go` (or
`router_test.go` if no panel_hub_test exists):

```go
func TestPanelHub_HelpScreen_EditsBody(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})

	q := &tg.CallbackQuery{
		ID:      "cbk",
		From:    tg.User{ID: 42},
		Message: tg.Message{MessageID: 1, Chat: tg.Chat{ID: -100}},
		Data:    "panel:0:help:maint",
	}
	r.HandleCallback(context.Background(), q)

	if len(f.edits) != 1 {
		t.Fatalf("want 1 edit, got %d", len(f.edits))
	}
	if !strings.Contains(f.edits[0], "Restart hrneo") {
		t.Errorf("maint help body should mention 'Restart hrneo':\n%s", f.edits[0])
	}
}
```

- [ ] **Step 9: Run tests**

Run: `go test ./internal/backend/tg/ ./internal/backend/callbacks/ -v`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/backend/tg/help_panels.go \
        internal/backend/tg/help_panels_test.go \
        internal/backend/tg/maint_panel.go \
        internal/backend/tg/routes_panel.go \
        internal/backend/tg/tunnels_panel.go \
        internal/backend/callbacks/parse.go \
        internal/backend/callbacks/panel_hub.go \
        internal/backend/callbacks/panel_hub_test.go
git commit -m "feat(panels): per-panel 'ℹ Помощь' inline button + help-screen dispatch"
```

---

## Task 13: Register `/help` in bot menu

**Files:**
- Modify: `cmd/backend/main.go`

- [ ] **Step 1: Locate SetMyCommands call**

Run: `grep -n 'SetMyCommands' cmd/backend/main.go`

- [ ] **Step 2: Add /help entry**

Edit `cmd/backend/main.go`. In the `SetMyCommands` argument slice
(currently lists `/panel`, `/ensure_topics`, etc.), insert a new
entry near the top:

```go
{Command: "help", Description: "Справка по командам и кнопкам"},
```

The exact struct type comes from the existing call — match the
existing pattern.

- [ ] **Step 3: Run backend tests**

Run: `go test ./cmd/backend/ -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/backend/main.go
git commit -m "chore(backend): register /help in bot command menu"
```

---

## Task 14: Integration test — diag auto-trigger end-to-end

**Files:**
- Modify: `cmd/backend/integration_test.go`

- [ ] **Step 1: Locate the existing diag fake**

The existing integration test has a fake awg-manager that always
returns 200 for `/api/diagnostics/result`. We're going to extend it
into a state machine: first GET returns 400+NO_REPORT, the
POST `/api/diagnostics/run` returns 200, subsequent GETs return 200
with a canned body.

- [ ] **Step 2: Write the test**

Add a new test:

```go
func TestIntegration_DiagNow_NoReportAutoTriggers(t *testing.T) {
	// State machine: first /result → 400+NO_REPORT; /run → 200; then
	// /result → 200 with canned body.
	var resultHits int
	awgFake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/diagnostics/result":
			resultHits++
			if resultHits == 1 {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":true,"code":"NO_REPORT"}`))
				return
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"system":{"appVersion":"2.8.2","uptime":"5m"}}`))
		case "/api/diagnostics/run":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true,"data":{"status":"running"}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer awgFake.Close()

	// (Wire awgFake into the agent runner with a tiny DiagPollEvery —
	// match the existing integration test's runner-setup pattern.)
	// ... use existing TestIntegration_* harness helpers ...

	// Trigger a diag_now via the backend's cmd queue exactly as the
	// real callback does. Then wait for the result to arrive in the
	// fake TG client's outgoing message log. Assert:
	//   - resultHits >= 2 (at least one re-poll after NO_REPORT)
	//   - the TG message contains "📊 Диагностика" + "2.8.2"
	//   - the TG message has an inline keyboard with "📄 Полный отчёт"
}
```

Fill in the harness wiring based on the existing
`TestIntegration_DiagPipeline_Smoke` pattern (or whatever the
existing test is named).

- [ ] **Step 3: Run the test**

Run: `go test ./cmd/backend/ -run TestIntegration_DiagNow_NoReportAutoTriggers -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/backend/integration_test.go
git commit -m "test(integration): diag_now auto-trigger end-to-end"
```

---

## Task 15: Full suite + manual smoke on testkeen

- [ ] **Step 1: Full Go test suite**

Run: `go test ./... -count=1`
Expected: every package green.

- [ ] **Step 2: Vet + build**

Run: `go vet ./...`
Run: `go build ./...`
Expected: clean.

- [ ] **Step 3: Manual on testkeen (operator side)**

Deploy via wizard: `update-backend` + `update-agent` for testkeen.
In the testkeen router topic, as admin AND as one of the operators
(alyaba/de4ddy), verify:

- `/help` returns role-correct body.
- "📊 Диагностика" inline-button under a HARD-alert OR via the
  smart-reply flow produces the parsed card with "📄 Полный отчёт"
  button (in NO_REPORT scenario, ~30–40s wait then card).
- "📄 Полный отчёт" sends the raw JSON in a code-fence.
- Tap "ℹ Помощь" on the maintenance panel — body switches to help.
- Stop awg-manager (`/opt/etc/init.d/S99awg-manager stop`) and tap
  "📊 Диагностика" — friendly "awg-manager недоступен" + service
  restart hint.
- Restart awg-manager — diag works again.

- [ ] **Step 4: Update spec status section**

Edit `docs/superpowers/specs/2026-05-13-tg-ux-polish-design.md`
appending a short "Status" section at the bottom: "Implemented in
v0.12.0-rc5 on 2026-05-13. Manual acceptance: ✅ testkeen."

- [ ] **Step 5: Final commit + tag (optional)**

```bash
git add docs/superpowers/specs/2026-05-13-tg-ux-polish-design.md
git commit -m "docs(spec): mark TG UX polish implemented in v0.12.0-rc5"
```

Tagging rc5 is a separate operator decision; do not tag from this
plan automatically.

---

## Self-Review

**1. Spec coverage:**

| Spec section | Plan task(s) |
|---|---|
| Section 1 (Card template) | Task 1 |
| Section 2 (HintFor dictionary) | Task 2, Task 9 (callers) |
| Section 3 (diag flow: trigger + parse + render) | Task 3 (parser), Task 4 (DiagRun), Task 5 (runner), Task 6 (formatter), Task 7 (cache), Task 8 (diag_raw button) |
| Section 4 (/help admin/operator/none) | Task 10 (DB helper), Task 11 (command), Task 13 (bot menu) |
| Section 5 (per-panel ℹ Помощь) | Task 12 |
| Integration tests | Task 14 |
| Acceptance | Task 15 |

No spec section uncovered.

**2. Placeholder scan:** No "TODO" / "TBD" / "add appropriate error
handling" patterns. Every code step has the full text required to
apply the change. The two places that say "match the existing pattern"
(integration test harness, SetMyCommands struct type) are explicit
references to single concrete adjacent constructs — not vague
hand-waves.

**3. Type consistency:** `Card`/`CardOpts` and `HintFor` signatures
are stable across all referencing tasks. `DiagRun` / `DiagNow` /
`DiagPollEvery` / `DiagPollMax` all consistent. Callback grammar
strings (`diag_raw:<uid>:_panel_:<token>`,
`panel:0:help:<screen>`) consistent between definition (Task 7/12)
and dispatch (Task 8/12) and tests.

**4. Risks flagged:**
- Task 12 step 5 may require updating multiple existing
  panel-keyboard tests; if the assertion-count is large, the executor
  should batch those into one extra commit per file.
- Task 14 integration test references an existing helper that must
  be located; if no such helper exists in `cmd/backend/integration_test.go`,
  drop the integration test (the agent-level unit tests in Task 5
  already cover the auto-trigger path end-to-end at the runner level)
  and add a note to the spec.
