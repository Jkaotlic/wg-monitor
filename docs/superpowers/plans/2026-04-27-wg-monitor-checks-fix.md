# wg-monitor: Checks Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Заменить `awg_routing` на DNS-агностичный probe (`https://1.1.1.1/cdn-cgi/trace`), сменить дефолтный `awg_marker` URL с уязвимого youtube-manifest на стабильный `gstatic/generate_204`, и **полностью переписать `dns_doh`** в новый чек, который проверяет здоровье **пользовательских** DNS-резолверов (auto-discovered из Keenetic NDM running-config через `/bin/ndmc`, transport plain/DoH).

**Architecture:**
- Чек `awg_routing` — HTTP GET → парсит строку `ip=...` из тела ответа `cdn-cgi/trace`. Iface-bound dialer уже работает корректно (диагностика показала, что прежняя гипотеза «iface-bound dialer broken on Keenetic» — ложная атрибуция трёх ортогональных проблем).
- Чек `awg_marker` — без code-changes, только смена дефолтного URL в `agent/config.go`.
- Новый чек `dns` (раньше `dns_doh`) — multi-transport: plain UDP (RFC 1035, через `golang.org/x/net/dns/dnsmessage`, iface-bound) и DoH (HTTP GET с `application/dns-json`, опциональный iface-bound). Endpoints обнаруживаются автоматически на старте агента через `/bin/ndmc -c "show running-config"`, парсятся регулярками. Маппинг NDMS-имён (`Wireguard0`, `Wireguard1`) → Linux-имён (`nwg0`, `nwg1`) идёт через уже существующий awg-manager API (`/api/tunnels/all`). Пользователь может также указать manual endpoints в `config.yaml`.
- Helper-команда `wg-monitor-cli show-discovered-dns` для debug — выводит, что бы было обнаружено.

**Tech Stack:**
- Go 1.22+, pure-Go (без cgo, без shell кроме `/bin/ndmc`)
- `golang.org/x/net/dns/dnsmessage` — для plain UDP DNS encode/decode
- `httptest` для unit-тестов HTTP/DoH endpoints
- `slog` (уже в проекте) — debug-logs для discovery
- Существующий `IfaceDialer` (`internal/agent/checks/dialer_linux.go`) — переиспользуется для plain UDP и DoH

---

## Background

### Что сломалось и почему (Phase 1 systematic-debugging conclusions)

Live-диагностика на MyRouter (см. `deploy/diag/keenetic_diag.py`) выявила, что **гипотеза о сломанном iface-bound dialer в `project_wg_monitor.md` была ложной**. Реальные root-cause:

1. **`awg_routing`** фейлился потому что Keenetic DNS-proxy блокирует `api.ipify.org` (возвращает `0.0.0.0`). Подтверждение: `curl --interface nwg0 --resolve api.ipify.org:443:104.26.12.205 https://api.ipify.org` → `89.125.101.122` (правильный exit IP). Значит `SO_BINDTODEVICE` работает идеально.
2. **`awg_marker`** фейлился потому что `https://www.youtube.com/-/manifest` возвращает HTTP 404 (требуемый чеком 2xx — недостижим).
3. **`dns_doh`** фейлился потому что `dig` **не установлен** в Entware (`/opt/bin/dig` отсутствует). `Runner.Run("dig", ...)` всегда возвращает exec error.

Дополнительные находки live-диагностики:
- `cdn-cgi/trace` через `nwg0` возвращает `ip=89.125.101.122` (= `expected_exit_ip`) — рабочая замена для `awg_routing`.
- `gstatic/generate_204` через `nwg0` возвращает HTTP 204 — рабочая замена для `awg_marker`.
- `/bin/ndmc -c "show running-config"` работает локально без admin-auth — ключ к auto-discovery.

### Формат NDM running-config для DNS

```
ip name-server 1.1.1.1 "" on Wireguard1
ip name-server 1.0.0.1 "" on Wireguard1
ip name-server 172.29.172.254 "" on Wireguard0
ip name-server 1.0.0.1 "" on Wireguard0
...
dns-proxy
    rebind-protect auto
    https upstream https://jkaotlic.duckdns.org:8443/dns-query dnsm
!
```

- Plain DNS (per-interface): `ip name-server <IP> "<suffix>" on <NDMSName>`
- DoH (глобальный): внутри блока `dns-proxy ... !`: `https upstream <URL> dnsm`
- DoT (теоретический, на будущее): `tls upstream <host>:<port> dnss?` — добавим парсинг, но не в production до появления у пользователей

### Маппинг NDMSName → Linux-iface

Через awg-manager `/api/tunnels/all`:
```json
{"tunnels":[
  {"id":"awg11","ndmsName":"Wireguard1","interfaceName":"nwg1",...},
  {"id":"awg12","ndmsName":"Wireguard0","interfaceName":"nwg0",...}
]}
```

Не-туннельные NDMSName (`Home`, `ISP`, `GigabitEthernet1`) пропускаются — для них Linux-имя не определяется через awg-manager, и DNS через них идёт через Keenetic OS native routing, не нашего интереса.

---

## File Structure

### Create

| Path | Responsibility |
|---|---|
| `internal/agent/keenetic/ndmc.go` | Тонкий wrapper над `/bin/ndmc -c <cmd>`. Возвращает stdout как строку. Принимает CmdRunner для тестирования. |
| `internal/agent/keenetic/ndmc_test.go` | Тест: stub-runner возвращает заведомо известный output, wrapper передаёт правильные args. |
| `internal/agent/keenetic/parser.go` | Парсер running-config в `[]DNSEndpoint`. Регулярки для `ip name-server` и блока `dns-proxy`. |
| `internal/agent/keenetic/parser_test.go` | Покрытие: 4 plain + 1 DoH из реального live-output, missing sections, malformed lines. |
| `internal/agent/keenetic/iface_map.go` | Запрос к awg-manager `/api/tunnels/all`, возврат `map[NDMSName]LinuxIface`. |
| `internal/agent/keenetic/iface_map_test.go` | httptest mock awg-manager, нормальный кейс, ошибки. |
| `internal/agent/checks/dns.go` | Новый чек: probe всех endpoints, threshold-логика, FAIL/OK. |
| `internal/agent/checks/dns_test.go` | Покрытие probe-функций (plain UDP через ad-hoc UDP server в тесте, DoH через httptest), threshold edge-cases. |
| `internal/agent/checks/dns_plain.go` | Plain UDP DNS prober. Encode A-query, отправка через iface-bound conn, decode answer. |
| `internal/agent/checks/dns_doh.go` | DoH HTTP prober. **Replaces** существующий `dns_doh.go` (старый dig-shell impl удаляется). |
| `cmd/wg-monitor-cli/show_dns.go` | Subcommand `show-discovered-dns` — debug helper. |

### Modify

| Path | Change |
|---|---|
| `internal/agent/checks/awg_routing.go` | Парсер тела ответа: вместо trim → regex поиск `ip=<value>`. URL field остаётся configurable (default — в `config.go`). |
| `internal/agent/checks/awg_routing_test.go` | Обновить body fixture на `cdn-cgi/trace`-формат. |
| `internal/agent/config.go` | (1) Default `RoutingProbeURL` → `https://1.1.1.1/cdn-cgi/trace`. (2) Default для `MarkerURL` → `http://www.gstatic.com/generate_204`, и снять обязательность (было `MarkerURL == "" → error`). (3) `DNSCheckConfig` — новые поля `Endpoints []DNSEndpointConfig`, `AutoDiscover bool`, `ViaDefault string` (имя iface для DoH default). Удаление `Providers []DNSProviderConfig`. |
| `internal/agent/config_test.go` (новый или дополнение) | Проверки новой схемы конфига. |
| `cmd/agent/main.go` | (1) При старте — если AutoDiscover=true, вызвать `keenetic.Discover(...)` и mergее с manual-endpoints. (2) Сконструировать `checks.DNS` с iface-bound transport-pool. (3) Удалить старую `dnsCheckFromCfg`. |
| `cmd/wg-monitor-cli/main.go` | Зарегистрировать новый subcommand `show-discovered-dns`. |

### Delete

| Path | Reason |
|---|---|
| `internal/agent/checks/dns_doh_test.go` (старая версия) | Полностью переписан в `dns_test.go`. |

(Старый `dns_doh.go` файл будет переписан под DoH-prober в новом стиле — не удаляется, заменяется содержимое.)

---

## Tasks

### Task 1: `awg_routing` — переключиться на `cdn-cgi/trace` и парсить `ip=...`

**Files:**
- Modify: `internal/agent/checks/awg_routing.go`
- Modify: `internal/agent/checks/awg_routing_test.go`
- Modify: `internal/agent/config.go:58-63` (default URL)

**Why:** Body `cdn-cgi/trace` многострочный (`fl=...\nh=1.1.1.1\nip=<addr>\nts=...`), а не plain IP — нужен regex-парсер.

- [ ] **Step 1: Update test to expect cdn-cgi/trace body format**

Полностью заменить файл `internal/agent/checks/awg_routing_test.go`:

```go
package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const cdnCgiTraceMatch = `fl=1179f36
h=1.1.1.1
ip=89.125.101.122
ts=1777301814.000
visit_scheme=https
uag=curl/8.15.0
colo=AMS
http=http/2
tls=TLSv1.3
`

const cdnCgiTraceMismatch = `fl=1179f36
h=1.1.1.1
ip=1.2.3.4
ts=1777301814.000
`

const cdnCgiTraceMissingIP = `fl=1179f36
h=1.1.1.1
ts=1777301814.000
`

func TestAwgRoutingMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(cdnCgiTraceMatch))
	}))
	defer srv.Close()

	chk := AwgRouting{Iface: "ignored", URL: srv.URL, Expected: "89.125.101.122"}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "ok" {
		t.Fatalf("got %+v", got)
	}
	if got.Details["got_ip"] != "89.125.101.122" {
		t.Fatalf("details: %+v", got.Details)
	}
}

func TestAwgRoutingMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(cdnCgiTraceMismatch))
	}))
	defer srv.Close()

	chk := AwgRouting{URL: srv.URL, Expected: "89.125.101.122"}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "fail" || got.Details["got_ip"] != "1.2.3.4" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestAwgRoutingMissingIPLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(cdnCgiTraceMissingIP))
	}))
	defer srv.Close()

	chk := AwgRouting{URL: srv.URL, Expected: "89.125.101.122"}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "fail" {
		t.Fatalf("expected fail on missing ip= line, got %+v", got)
	}
}

func TestAwgRoutingHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	chk := AwgRouting{URL: srv.URL, Expected: "89.125.101.122"}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "fail" {
		t.Fatalf("expected fail on 502, got %+v", got)
	}
}
```

- [ ] **Step 2: Run tests — they should fail (still parses raw body)**

Run: `go test ./internal/agent/checks/ -run TestAwgRouting -v`
Expected: `TestAwgRoutingMatch` FAIL — `got_ip` будет multiline `cdn-cgi/trace` body, not `89.125.101.122`.

- [ ] **Step 3: Update implementation in `awg_routing.go`**

Полностью заменить файл:

```go
package checks

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

type AwgRouting struct {
	Iface    string // informational; binding happens in HTTPClient's dialer
	URL      string // e.g. https://1.1.1.1/cdn-cgi/trace
	Expected string // expected egress IPv4
}

func (AwgRouting) Name() string { return "awg_routing" }

func (c AwgRouting) Run(ctx context.Context, d Deps) wire.Check {
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, c.URL, nil)
	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		return Fail(c.Name(), start, "http error", map[string]any{"err": err.Error()})
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Fail(c.Name(), start, "non-2xx", map[string]any{"http_code": resp.StatusCode})
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	got := parseCdnCgiTraceIP(string(body))
	if got == "" {
		return Fail(c.Name(), start, "no ip= line in trace body", nil)
	}
	if got != c.Expected {
		return Fail(c.Name(), start, "exit ip mismatch", map[string]any{"got_ip": got, "expected_ip": c.Expected})
	}
	return OK(c.Name(), start, map[string]any{"got_ip": got})
}

// parseCdnCgiTraceIP extracts the value of the "ip=" line from a Cloudflare
// cdn-cgi/trace response. Returns "" if no such line is present.
func parseCdnCgiTraceIP(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(line, "ip="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
```

- [ ] **Step 4: Run tests — they should pass**

Run: `go test ./internal/agent/checks/ -run TestAwgRouting -v`
Expected: 4 PASS.

- [ ] **Step 5: Update default URL in `config.go`**

В `internal/agent/config.go:58-63`:

```go
func (a AWGCheckConfig) RoutingURL() string {
	if a.RoutingProbeURL != "" {
		return a.RoutingProbeURL
	}
	return "https://1.1.1.1/cdn-cgi/trace"
}
```

- [ ] **Step 6: Run all tests — full sanity**

Run: `go test ./...`
Expected: всё зелёное.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/checks/awg_routing.go internal/agent/checks/awg_routing_test.go internal/agent/config.go
git commit -m "feat(checks/awg_routing): switch probe to cdn-cgi/trace + parse ip= line

api.ipify.org gets DNS-blocked by user-side DNS proxies (AdGuard Home etc.),
returning 0.0.0.0 and breaking the check independent of routing. Cloudflare
cdn-cgi/trace is Anycast (impossible to DNS-block without breaking 1.1.1.1
itself) and reliably returns the egress IP via the 'ip=...' line."
```

---

### Task 2: `awg_marker` — сменить дефолтный URL

**Files:**
- Modify: `internal/agent/config.go:43-49, 121-123`

**Why:** `youtube/-/manifest` отдаёт HTTP 404 → чек always-fail. `gstatic/generate_204` отдаёт стабильный HTTP 204.

- [ ] **Step 1: Add `MarkerURL()` accessor to `AWGCheckConfig`, default to gstatic**

В `internal/agent/config.go`, после `RoutingURL()`:

```go
func (a AWGCheckConfig) ResolvedMarkerURL() string {
	if a.MarkerURL != "" {
		return a.MarkerURL
	}
	return "http://www.gstatic.com/generate_204"
}
```

- [ ] **Step 2: Remove obligatory check on `MarkerURL`**

В `LoadConfig` в `config.go`, удалить блок:
```go
if cfg.Checks.AWG.MarkerURL == "" {
    return nil, fmt.Errorf("checks.awg.marker_url is required")
}
```

- [ ] **Step 3: Update wiring in `cmd/agent/main.go:51`**

Старая строка:
```go
checks.AwgMarker{Iface: cfg.Checks.AWG.Interface, URL: cfg.Checks.AWG.MarkerURL, MaxRetries: 3, BaseBackoff: 250 * time.Millisecond},
```

Новая:
```go
checks.AwgMarker{Iface: cfg.Checks.AWG.Interface, URL: cfg.Checks.AWG.ResolvedMarkerURL(), MaxRetries: 3, BaseBackoff: 250 * time.Millisecond},
```

- [ ] **Step 4: Run all tests — sanity**

Run: `go test ./...`
Expected: всё зелёное.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/config.go cmd/agent/main.go
git commit -m "feat(checks/awg_marker): default URL → gstatic/generate_204

youtube/-/manifest returns 404 unauthenticated, breaking the check
unconditionally. gstatic captive-portal endpoint always returns HTTP 204
(by design — used by every Android device for connectivity test).
Marker URL still configurable per-user via checks.awg.marker_url."
```

---

### Task 3: `keenetic.NDMC` wrapper

**Files:**
- Create: `internal/agent/keenetic/ndmc.go`
- Create: `internal/agent/keenetic/ndmc_test.go`

**Why:** Изолируем shell-вызов `/bin/ndmc -c <cmd>` чтобы дальнейшие парсеры были pure-Go-тестируемыми.

- [ ] **Step 1: Write failing test**

Создать `internal/agent/keenetic/ndmc_test.go`:

```go
package keenetic

import (
	"context"
	"errors"
	"testing"
)

// stubRunner implements CmdRunner with canned outputs keyed by joined argv.
type stubRunner struct {
	want   string // expected joined argv
	out    string
	err    error
	called bool
}

func (s *stubRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	s.called = true
	got := name
	for _, a := range args {
		got += " " + a
	}
	if got != s.want {
		return "", &mismatchErr{want: s.want, got: got}
	}
	return s.out, s.err
}

type mismatchErr struct{ want, got string }

func (e *mismatchErr) Error() string { return "stub: argv mismatch want=" + e.want + " got=" + e.got }

func TestNDMC_RunsCorrectArgv(t *testing.T) {
	stub := &stubRunner{
		want: "/bin/ndmc -c show running-config",
		out:  "stub-output\n",
	}
	n := NDMC{Runner: stub}
	out, err := n.Show(context.Background(), "running-config")
	if err != nil {
		t.Fatalf("Show err: %v", err)
	}
	if out != "stub-output\n" {
		t.Fatalf("out: %q", out)
	}
	if !stub.called {
		t.Fatalf("runner not called")
	}
}

func TestNDMC_PropagatesErr(t *testing.T) {
	stub := &stubRunner{
		want: "/bin/ndmc -c show running-config",
		err:  errors.New("ndmc not found"),
	}
	n := NDMC{Runner: stub}
	_, err := n.Show(context.Background(), "running-config")
	if err == nil {
		t.Fatalf("expected error")
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/agent/keenetic/ -run TestNDMC -v`
Expected: FAIL — package `keenetic` doesn't exist or `NDMC` is undefined.

- [ ] **Step 3: Write minimal implementation**

Создать `internal/agent/keenetic/ndmc.go`:

```go
// Package keenetic isolates Keenetic OS-specific integrations: ndmc binary
// invocation, running-config parsing, and NDMS-name → Linux-iface mapping.
package keenetic

import (
	"context"
	"fmt"
)

// CmdRunner mirrors checks.Runner — redeclared here to keep keenetic a leaf
// package without an import cycle on `checks`.
type CmdRunner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// NDMC is a thin wrapper around the /bin/ndmc binary on KeeneticOS. The binary
// is set-uid root and talks to the local ndm core process via a unix socket,
// so it requires no auth credentials when run as root (which the agent is, via
// Entware init.d).
type NDMC struct {
	Runner CmdRunner
	// BinaryPath defaults to "/bin/ndmc" if empty.
	BinaryPath string
}

func (n NDMC) bin() string {
	if n.BinaryPath != "" {
		return n.BinaryPath
	}
	return "/bin/ndmc"
}

// Show runs `ndmc -c "show <subcmd>"`. Returns raw stdout.
func (n NDMC) Show(ctx context.Context, subcmd string) (string, error) {
	out, err := n.Runner.Run(ctx, n.bin(), "-c", "show "+subcmd)
	if err != nil {
		return "", fmt.Errorf("ndmc show %s: %w", subcmd, err)
	}
	return out, nil
}
```

- [ ] **Step 4: Run test, verify it passes**

Run: `go test ./internal/agent/keenetic/ -run TestNDMC -v`
Expected: 2 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/keenetic/ndmc.go internal/agent/keenetic/ndmc_test.go
git commit -m "feat(keenetic): NDMC wrapper for /bin/ndmc -c <cmd>

Local ndmc invocation requires no NDM admin/password — talks to ndm core
via unix socket and authenticates via OS-level root privilege (set-uid).
Wrapper keeps subprocess invocation isolated for parser unit tests."
```

---

### Task 4: `keenetic` parser — running-config → DNS endpoints

**Files:**
- Create: `internal/agent/keenetic/parser.go`
- Create: `internal/agent/keenetic/parser_test.go`

**Why:** Парсер — core логика auto-discovery. Должен быть pure-string-input для удобства тестирования.

- [ ] **Step 1: Write failing test with real-life live fixture**

Создать `internal/agent/keenetic/parser_test.go`:

```go
package keenetic

import "testing"

// liveSnippet is a verbatim excerpt of `ndmc -c "show running-config"` from
// the testkeen MyRouter (2026-04-27). Lines outside DNS-related sections are
// included to confirm the parser ignores them.
const liveSnippet = `
ip name-server 1.1.1.1 "" on Wireguard1
ip name-server 1.0.0.1 "" on Wireguard1
ip name-server 172.29.172.254 "" on Wireguard0
ip name-server 1.0.0.1 "" on Wireguard0
ip route 10.0.0.0 255.0.0.0 nwg2 auto
!
service dns-proxy
service http
!
dns-proxy
    rebind-protect auto
    https upstream https://jkaotlic.duckdns.org:8443/dns-query dnsm
!
mdns
    reflector enforce
!
`

func TestParseDNSEndpoints_PlainAndDoH(t *testing.T) {
	eps := ParseDNSEndpoints(liveSnippet)
	if len(eps) != 5 {
		t.Fatalf("want 5 endpoints, got %d: %+v", len(eps), eps)
	}

	// First 4 are plain DNS in source order
	for i, want := range []DNSEndpoint{
		{Type: "plain", Host: "1.1.1.1", Port: 53, NDMSName: "Wireguard1"},
		{Type: "plain", Host: "1.0.0.1", Port: 53, NDMSName: "Wireguard1"},
		{Type: "plain", Host: "172.29.172.254", Port: 53, NDMSName: "Wireguard0"},
		{Type: "plain", Host: "1.0.0.1", Port: 53, NDMSName: "Wireguard0"},
	} {
		if eps[i] != want {
			t.Errorf("ep[%d]: want %+v got %+v", i, want, eps[i])
		}
	}

	// Last is the DoH global endpoint
	doh := eps[4]
	if doh.Type != "doh" || doh.URL != "https://jkaotlic.duckdns.org:8443/dns-query" {
		t.Errorf("doh: %+v", doh)
	}
	if doh.NDMSName != "" {
		t.Errorf("DoH should have no NDMSName binding, got %q", doh.NDMSName)
	}
}

func TestParseDNSEndpoints_NoDNS(t *testing.T) {
	eps := ParseDNSEndpoints("system\n    hostname Test\n!\n")
	if len(eps) != 0 {
		t.Fatalf("want 0, got %d", len(eps))
	}
}

func TestParseDNSEndpoints_DoHOnly(t *testing.T) {
	cfg := `
dns-proxy
    rebind-protect auto
    https upstream https://my.example.com/dns-query dnsm
!
`
	eps := ParseDNSEndpoints(cfg)
	if len(eps) != 1 || eps[0].Type != "doh" {
		t.Fatalf("got %+v", eps)
	}
	if eps[0].URL != "https://my.example.com/dns-query" {
		t.Fatalf("URL: %q", eps[0].URL)
	}
}

func TestParseDNSEndpoints_DoTHandled(t *testing.T) {
	cfg := `
dns-proxy
    rebind-protect auto
    tls upstream 1.1.1.1:853 dnss
!
`
	eps := ParseDNSEndpoints(cfg)
	if len(eps) != 1 || eps[0].Type != "dot" {
		t.Fatalf("got %+v", eps)
	}
	if eps[0].Host != "1.1.1.1" || eps[0].Port != 853 {
		t.Fatalf("host:port: %s:%d", eps[0].Host, eps[0].Port)
	}
}

func TestParseDNSEndpoints_IgnoresMalformed(t *testing.T) {
	cfg := `
ip name-server                          ` + // garbage line, missing fields
		`
ip name-server 1.2.3.4 "" on
ip name-server 1.2.3.4 "" on Iface1
`
	eps := ParseDNSEndpoints(cfg)
	if len(eps) != 1 {
		t.Fatalf("want 1 valid line, got %d: %+v", len(eps), eps)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/agent/keenetic/ -run TestParseDNS -v`
Expected: FAIL — `ParseDNSEndpoints` and `DNSEndpoint` are undefined.

- [ ] **Step 3: Write minimal implementation**

Создать `internal/agent/keenetic/parser.go`:

```go
package keenetic

import (
	"regexp"
	"strconv"
	"strings"
)

// DNSEndpoint represents one DNS resolver discovered from the Keenetic NDM
// running-config. Plain DNS is per-interface (NDMSName is set); DoH/DoT are
// global to the dns-proxy and have empty NDMSName.
type DNSEndpoint struct {
	Type     string // "plain", "doh", "dot"
	Host     string // for plain and dot
	Port     int    // for plain (default 53) and dot (typically 853)
	URL      string // for doh
	NDMSName string // NDM-side iface name (e.g. "Wireguard0"); empty for global
}

var (
	// ip name-server <IP> "<suffix>" on <NDMSName>
	rePlain = regexp.MustCompile(`^\s*ip\s+name-server\s+(\S+)\s+"([^"]*)"\s+on\s+(\S+)\s*$`)
	// inside `dns-proxy` block:  https upstream <URL> dnsm
	reDoH = regexp.MustCompile(`^\s*https\s+upstream\s+(\S+)(?:\s+(\S+))?\s*$`)
	// inside `dns-proxy` block:  tls upstream <host>:<port> dnss?
	reDoT = regexp.MustCompile(`^\s*tls\s+upstream\s+(\S+):(\d+)(?:\s+(\S+))?\s*$`)
)

// ParseDNSEndpoints walks `ndmc show running-config` output and returns all
// DNS endpoints in source order: per-interface plain entries first (matching
// the visual order on KeeneticOS web-UI DNS panel), then global DoH/DoT.
//
// Block tracking: `https upstream` and `tls upstream` are only valid inside a
// top-level `dns-proxy` block, terminated by a `!` on its own line.
func ParseDNSEndpoints(cfg string) []DNSEndpoint {
	var out []DNSEndpoint
	inDNSProxy := false
	for _, line := range strings.Split(cfg, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		// Block enter/exit
		if strings.TrimSpace(trimmed) == "dns-proxy" && !strings.HasPrefix(trimmed, " ") {
			inDNSProxy = true
			continue
		}
		if inDNSProxy && strings.TrimSpace(trimmed) == "!" {
			inDNSProxy = false
			continue
		}

		// Plain name-server (anywhere)
		if m := rePlain.FindStringSubmatch(trimmed); m != nil {
			out = append(out, DNSEndpoint{
				Type:     "plain",
				Host:     m[1],
				Port:     53,
				NDMSName: m[3],
			})
			continue
		}

		if !inDNSProxy {
			continue
		}

		if m := reDoH.FindStringSubmatch(trimmed); m != nil {
			out = append(out, DNSEndpoint{Type: "doh", URL: m[1]})
			continue
		}
		if m := reDoT.FindStringSubmatch(trimmed); m != nil {
			port, _ := strconv.Atoi(m[2])
			out = append(out, DNSEndpoint{Type: "dot", Host: m[1], Port: port})
		}
	}
	return out
}
```

- [ ] **Step 4: Run test, verify it passes**

Run: `go test ./internal/agent/keenetic/ -run TestParseDNS -v`
Expected: 5 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/keenetic/parser.go internal/agent/keenetic/parser_test.go
git commit -m "feat(keenetic): parser for ndmc running-config DNS endpoints

Recognises three syntaxes: 'ip name-server <IP> \"\" on <NDMSName>' for plain
per-interface DNS, 'https upstream <URL> dnsm' for global DoH inside
dns-proxy block, 'tls upstream <host>:<port> dnss' for DoT (future-proof,
not in production today)."
```

---

### Task 5: `keenetic` iface map — NDMS-name → Linux-iface via awg-manager

**Files:**
- Create: `internal/agent/keenetic/iface_map.go`
- Create: `internal/agent/keenetic/iface_map_test.go`

**Why:** Plain DNS endpoints привязаны к NDMSName (`Wireguard0`), а наш iface-bound dialer оперирует Linux-именами (`nwg0`). Маппинг даёт awg-manager API.

- [ ] **Step 1: Write failing test**

Создать `internal/agent/keenetic/iface_map_test.go`:

```go
package keenetic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const tunnelsLive = `{"success":true,"data":{"tunnels":[
  {"id":"awg11","ndmsName":"Wireguard1","interfaceName":"nwg1","status":"running"},
  {"id":"awg12","ndmsName":"Wireguard0","interfaceName":"nwg0","status":"running"}
]}}`

func TestFetchIfaceMap_FromAwgManager(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			http.Error(w, "missing XHR header", http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/api/tunnels/all" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tunnelsLive))
	}))
	defer srv.Close()

	m, err := FetchIfaceMap(context.Background(), IfaceMapOptions{
		AwgManagerURL: srv.URL,
		Client:        srv.Client(),
	})
	if err != nil {
		t.Fatalf("FetchIfaceMap: %v", err)
	}
	if m["Wireguard0"] != "nwg0" {
		t.Errorf("Wireguard0 → %q, want nwg0", m["Wireguard0"])
	}
	if m["Wireguard1"] != "nwg1" {
		t.Errorf("Wireguard1 → %q, want nwg1", m["Wireguard1"])
	}
}

func TestFetchIfaceMap_AwgManagerDown(t *testing.T) {
	// Reach an obviously-dead URL (RFC 5737 documentation IP, port 1).
	_, err := FetchIfaceMap(context.Background(), IfaceMapOptions{
		AwgManagerURL: "http://192.0.2.1:1",
		Client:        &http.Client{},
	})
	if err == nil {
		t.Fatalf("expected error on unreachable awg-manager")
	}
}

func TestFetchIfaceMap_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>not json</html>`))
	}))
	defer srv.Close()
	_, err := FetchIfaceMap(context.Background(), IfaceMapOptions{
		AwgManagerURL: srv.URL,
		Client:        srv.Client(),
	})
	if err == nil {
		t.Fatalf("expected error on bad JSON")
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/agent/keenetic/ -run TestFetchIfaceMap -v`
Expected: FAIL — `FetchIfaceMap` undefined.

- [ ] **Step 3: Write minimal implementation**

Создать `internal/agent/keenetic/iface_map.go`:

```go
package keenetic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// IfaceMapOptions configures FetchIfaceMap. Defaults work on KeeneticOS.
type IfaceMapOptions struct {
	AwgManagerURL string        // default "http://127.0.0.1:2222"
	Client        *http.Client  // default 5s-timeout client
	Timeout       time.Duration // default 5s
}

type tunnelEntry struct {
	NDMSName      string `json:"ndmsName"`
	InterfaceName string `json:"interfaceName"`
}

type tunnelsResp struct {
	Success bool `json:"success"`
	Data    struct {
		Tunnels []tunnelEntry `json:"tunnels"`
	} `json:"data"`
}

// FetchIfaceMap returns a map from NDMSName (e.g. "Wireguard0") to the Linux
// interface name (e.g. "nwg0") for every active WG tunnel managed by
// awg-manager. Non-tunnel interfaces (Home, ISP, GigabitEthernet1) are not in
// the map — DNS endpoints bound to them go through Keenetic native routing,
// outside our agent's iface-bound dialer.
func FetchIfaceMap(ctx context.Context, opts IfaceMapOptions) (map[string]string, error) {
	url := opts.AwgManagerURL
	if url == "" {
		url = "http://127.0.0.1:2222"
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url+"/api/tunnels/all", nil)
	if err != nil {
		return nil, fmt.Errorf("awg-manager request: %w", err)
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("awg-manager: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("awg-manager: status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	var out tunnelsResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("awg-manager: decode: %w", err)
	}
	if !out.Success {
		return nil, errors.New("awg-manager: success=false")
	}
	m := make(map[string]string, len(out.Data.Tunnels))
	for _, t := range out.Data.Tunnels {
		if t.NDMSName != "" && t.InterfaceName != "" {
			m[t.NDMSName] = t.InterfaceName
		}
	}
	return m, nil
}
```

- [ ] **Step 4: Run test, verify it passes**

Run: `go test ./internal/agent/keenetic/ -run TestFetchIfaceMap -v`
Expected: 3 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/keenetic/iface_map.go internal/agent/keenetic/iface_map_test.go
git commit -m "feat(keenetic): NDMSName→Linux-iface map via awg-manager API

Plain DNS records in NDM running-config bind to NDMSName ('Wireguard0'),
but our SO_BINDTODEVICE dialer needs Linux name ('nwg0'). awg-manager
/api/tunnels/all already exposes both fields per tunnel — reuses the
same X-Requested-With header trick as the wgreader's awg-manager strategy."
```

---

### Task 6: `checks/dns_plain` — UDP DNS prober

**Files:**
- Create: `internal/agent/checks/dns_plain.go`
- Create: `internal/agent/checks/dns_plain_test.go`

**Why:** Plain DNS = UDP/53 send query, parse response. Reusable function, бинд через iface dialer тестируется отдельно.

- [ ] **Step 1: Write failing test using a tiny in-test UDP server**

Создать `internal/agent/checks/dns_plain_test.go`:

```go
package checks

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// startMockUDPDNS returns a UDP server that replies with a single A-record
// answer for any A-query. Returns its host:port for use as Server in
// ProbePlainDNS, plus a stop function.
func startMockUDPDNS(t *testing.T, answerIP [4]byte) (string, func()) {
	t.Helper()
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	stop := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			select {
			case <-stop:
				return
			default:
			}
			conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, raddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			var msg dnsmessage.Message
			if err := msg.Unpack(buf[:n]); err != nil {
				continue
			}
			resp := dnsmessage.Message{
				Header: dnsmessage.Header{
					ID:            msg.Header.ID,
					Response:      true,
					Authoritative: true,
				},
				Questions: msg.Questions,
				Answers: []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{
						Name:  msg.Questions[0].Name,
						Type:  dnsmessage.TypeA,
						Class: dnsmessage.ClassINET,
						TTL:   60,
					},
					Body: &dnsmessage.AResource{A: answerIP},
				}},
			}
			out, _ := resp.Pack()
			_, _ = conn.WriteToUDP(out, raddr)
		}
	}()
	return conn.LocalAddr().String(), func() { close(stop); conn.Close() }
}

func TestProbePlainDNS_Answers(t *testing.T) {
	server, stop := startMockUDPDNS(t, [4]byte{93, 184, 216, 34})
	defer stop()

	got, err := ProbePlainDNS(context.Background(), server, "example.com.", nil, 1*time.Second)
	if err != nil {
		t.Fatalf("ProbePlainDNS: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("no answers")
	}
}

func TestProbePlainDNS_Timeout(t *testing.T) {
	// Connect to a port that has no listener — deadline expires.
	_, err := ProbePlainDNS(context.Background(), "127.0.0.1:1", "example.com.", nil, 200*time.Millisecond)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/agent/checks/ -run TestProbePlainDNS -v`
Expected: FAIL — `ProbePlainDNS` undefined.

- [ ] **Step 3: Write minimal implementation**

Создать `internal/agent/checks/dns_plain.go`:

```go
package checks

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// ProbePlainDNS sends a single A-query for `domain` to `server` (host:port)
// over UDP and returns the answer A-records. The dialer (if non-nil) is used
// to bind the socket to a specific interface; nil falls back to net.Dialer{}.
//
// Returns ([]net.IP, nil) on success (even if empty answer section),
// (nil, error) on transport, parse, or timeout failure.
func ProbePlainDNS(ctx context.Context, server, domain string, dialer *net.Dialer, timeout time.Duration) ([]net.IP, error) {
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dialer.DialContext(cctx, "udp", server)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", server, err)
	}
	defer conn.Close()
	deadline, _ := cctx.Deadline()
	conn.SetDeadline(deadline)

	name, err := dnsmessage.NewName(domain)
	if err != nil {
		return nil, fmt.Errorf("dns name %q: %w", domain, err)
	}

	var idBuf [2]byte
	_, _ = rand.Read(idBuf[:])
	id := binary.BigEndian.Uint16(idBuf[:])
	q := dnsmessage.Message{
		Header: dnsmessage.Header{ID: id, RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  name,
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		}},
	}
	pkt, err := q.Pack()
	if err != nil {
		return nil, fmt.Errorf("pack query: %w", err)
	}
	if _, err := conn.Write(pkt); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	var resp dnsmessage.Message
	if err := resp.Unpack(buf[:n]); err != nil {
		return nil, fmt.Errorf("unpack reply: %w", err)
	}
	if resp.Header.ID != id {
		return nil, fmt.Errorf("response id mismatch: %d != %d", resp.Header.ID, id)
	}
	if resp.Header.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("rcode %v", resp.Header.RCode)
	}

	var ips []net.IP
	for _, rr := range resp.Answers {
		if a, ok := rr.Body.(*dnsmessage.AResource); ok {
			ips = append(ips, net.IP(a.A[:]))
		}
	}
	return ips, nil
}
```

- [ ] **Step 4: Add `golang.org/x/net` dependency if not present**

Run: `cd /c/Users/Anex/Projects/wg-monitor && go get golang.org/x/net/dns/dnsmessage && go mod tidy`
Expected: `go.sum` updated, no errors.

- [ ] **Step 5: Run test, verify it passes**

Run: `go test ./internal/agent/checks/ -run TestProbePlainDNS -v`
Expected: 2 PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/checks/dns_plain.go internal/agent/checks/dns_plain_test.go go.mod go.sum
git commit -m "feat(checks): pure-Go plain UDP DNS prober (dnsmessage)

Replaces shelling out to dig (which is missing in Entware on KeeneticOS).
Accepts an optional *net.Dialer so callers can bind to a specific iface
via SO_BINDTODEVICE — matches existing IfaceDialer pattern."
```

---

### Task 7: `checks/dns_doh` — pure-Go DoH prober (replace dig-shell impl)

**Files:**
- Modify: `internal/agent/checks/dns_doh.go` (полное замещение содержимого)
- Modify: `internal/agent/checks/dns_doh_test.go` (полное замещение)

**Why:** Старый импл использовал `Runner.Run("dig", ...)` — `dig` отсутствует в Entware. Pure-Go DoH через `application/dns-json` снимает этот блокер.

- [ ] **Step 1: Replace test file**

Заменить полностью `internal/agent/checks/dns_doh_test.go`:

```go
package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const dohJSONOK = `{"Status":0,"TC":false,"RD":true,"RA":true,"AD":true,"CD":false,
"Question":[{"name":"example.com","type":1}],
"Answer":[{"name":"example.com","type":1,"TTL":83,"data":"93.184.216.34"}]}`

const dohJSONNoAnswer = `{"Status":0,"TC":false,"RD":true,"RA":true,"Question":[{"name":"example.com","type":1}]}`

const dohJSONNXDOMAIN = `{"Status":3,"TC":false,"RD":true,"RA":true,"Question":[{"name":"foo","type":1}]}`

func TestProbeDoH_Answers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "example.com" {
			http.Error(w, "missing name", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/dns-json")
		_, _ = w.Write([]byte(dohJSONOK))
	}))
	defer srv.Close()

	got, err := ProbeDoH(context.Background(), srv.URL, "example.com", srv.Client(), 1*time.Second)
	if err != nil {
		t.Fatalf("ProbeDoH: %v", err)
	}
	if len(got) != 1 || got[0] != "93.184.216.34" {
		t.Fatalf("answers: %+v", got)
	}
}

func TestProbeDoH_EmptyAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(dohJSONNoAnswer))
	}))
	defer srv.Close()
	got, err := ProbeDoH(context.Background(), srv.URL, "example.com", srv.Client(), 1*time.Second)
	if err == nil {
		t.Fatalf("expected error on empty answer, got %v", got)
	}
}

func TestProbeDoH_NXDOMAIN(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(dohJSONNXDOMAIN))
	}))
	defer srv.Close()
	_, err := ProbeDoH(context.Background(), srv.URL, "foo", srv.Client(), 1*time.Second)
	if err == nil {
		t.Fatalf("expected error on NXDOMAIN")
	}
}

func TestProbeDoH_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	_, err := ProbeDoH(context.Background(), srv.URL, "x", srv.Client(), 1*time.Second)
	if err == nil {
		t.Fatalf("expected error on 502")
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/agent/checks/ -run TestProbeDoH -v`
Expected: FAIL — `ProbeDoH` undefined.

- [ ] **Step 3: Replace implementation file**

Полностью заменить `internal/agent/checks/dns_doh.go`:

```go
package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ProbeDoH issues an A-query for `domain` against the DoH endpoint at `url`
// using the application/dns-json query syntax (?name=...&type=A) and returns
// the answer IPs. Errors when the server returns non-2xx, NXDOMAIN/SERVFAIL,
// or zero answers.
//
// The HTTP client should be pre-configured with any iface-bound dialer.
// Default timeout from caller's context, capped by `timeout`.
func ProbeDoH(ctx context.Context, url, domain string, client *http.Client, timeout time.Duration) ([]string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	q := strings.Builder{}
	q.WriteString(url)
	if strings.Contains(url, "?") {
		q.WriteString("&")
	} else {
		q.WriteString("?")
	}
	q.WriteString("name=")
	q.WriteString(domain)
	q.WriteString("&type=A")

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, q.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("doh request: %w", err)
	}
	req.Header.Set("Accept", "application/dns-json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("doh: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("doh: status %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var jr struct {
		Status int `json:"Status"`
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.Unmarshal(body, &jr); err != nil {
		return nil, fmt.Errorf("doh: decode: %w", err)
	}
	if jr.Status != 0 {
		return nil, fmt.Errorf("doh: dns rcode %d", jr.Status)
	}
	var out []string
	for _, a := range jr.Answer {
		if a.Type == 1 { // A
			out = append(out, a.Data)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("doh: no A answers")
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/agent/checks/ -run TestProbeDoH -v`
Expected: 4 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/checks/dns_doh.go internal/agent/checks/dns_doh_test.go
git commit -m "feat(checks): pure-Go DoH prober (application/dns-json)

Replaces dig-shell implementation. Pure net/http with pre-configured
client (caller threads iface-bound dialer if needed). Tested against
real Cloudflare DoH JSON wire format."
```

---

### Task 8: `checks/dns` — high-level DNS Check with multi-endpoint, threshold

**Files:**
- Create: `internal/agent/checks/dns.go`
- Create: `internal/agent/checks/dns_test.go`

**Why:** Объединить prober'ы plain+DoH под одной Check-сущностью с threshold-логикой и отчётом по конкретным failed endpoints.

- [ ] **Step 1: Write failing test**

Создать `internal/agent/checks/dns_test.go`:

```go
package checks

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/agent/keenetic"
)

func TestDNS_AllOK_PlainOnly(t *testing.T) {
	server, stop := startMockUDPDNS(t, [4]byte{1, 2, 3, 4})
	defer stop()
	host, port := splitHostPort(t, server)

	chk := DNS{
		Endpoints: []keenetic.DNSEndpoint{
			{Type: "plain", Host: host, Port: port}, // no NDMSName → use default dialer
		},
		TestDomain:    "example.com",
		FailThreshold: 1,
		IfaceDialFn:   func(_ string) *net.Dialer { return &net.Dialer{} },
	}
	got := chk.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("got %+v", got)
	}
}

func TestDNS_DoHOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(dohJSONOK))
	}))
	defer srv.Close()

	chk := DNS{
		Endpoints: []keenetic.DNSEndpoint{
			{Type: "doh", URL: srv.URL},
		},
		TestDomain:    "example.com",
		FailThreshold: 1,
		HTTPClient:    srv.Client(),
	}
	got := chk.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("got %+v", got)
	}
}

func TestDNS_AllFail_TriggersFail(t *testing.T) {
	chk := DNS{
		Endpoints: []keenetic.DNSEndpoint{
			{Type: "plain", Host: "127.0.0.1", Port: 1},
			{Type: "plain", Host: "127.0.0.1", Port: 1},
		},
		TestDomain:    "example.com",
		FailThreshold: 1,
		IfaceDialFn:   func(_ string) *net.Dialer { return &net.Dialer{} },
		PerProbeTimeout: 100 * time.Millisecond,
	}
	got := chk.Run(context.Background(), Deps{})
	if got.Status != "fail" {
		t.Fatalf("expected fail, got %+v", got)
	}
	failed, _ := got.Details["failed"].([]map[string]any)
	if len(failed) != 2 {
		t.Fatalf("failed=%+v", failed)
	}
}

func TestDNS_PartialFailUnderThreshold(t *testing.T) {
	server, stop := startMockUDPDNS(t, [4]byte{1, 2, 3, 4})
	defer stop()
	host, port := splitHostPort(t, server)

	chk := DNS{
		Endpoints: []keenetic.DNSEndpoint{
			{Type: "plain", Host: host, Port: port},          // ok
			{Type: "plain", Host: "127.0.0.1", Port: 1},      // fail
		},
		TestDomain:    "example.com",
		FailThreshold: 2,
		IfaceDialFn:   func(_ string) *net.Dialer { return &net.Dialer{} },
		PerProbeTimeout: 100 * time.Millisecond,
	}
	got := chk.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("expected ok with 1/2 fail under threshold=2, got %+v", got)
	}
}

func TestDNS_NoEndpoints_ReturnsOK(t *testing.T) {
	// No endpoints means the discovery returned nothing; we don't FAIL on that.
	chk := DNS{Endpoints: nil, TestDomain: "example.com", FailThreshold: 1}
	got := chk.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("expected ok on empty endpoints, got %+v", got)
	}
	if got.Details["endpoints"] != 0 {
		t.Fatalf("details: %+v", got.Details)
	}
}

func splitHostPort(t *testing.T, hp string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(hp)
	if err != nil {
		t.Fatalf("SplitHostPort %q: %v", hp, err)
	}
	var port int
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}
	return host, port
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/agent/checks/ -run TestDNS -v`
Expected: FAIL — `DNS` struct undefined.

- [ ] **Step 3: Write implementation**

Создать `internal/agent/checks/dns.go`:

```go
package checks

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/anex/wg-monitor/internal/agent/keenetic"
	"github.com/anex/wg-monitor/pkg/wire"
)

// DNS is the high-level check that probes every configured/discovered DNS
// endpoint and reports a FAIL if at least FailThreshold of them are
// unreachable or return a bad answer.
type DNS struct {
	Endpoints       []keenetic.DNSEndpoint
	TestDomain      string                       // e.g. "example.com"
	FailThreshold   int                          // FAIL if failed >= threshold; 0 → 1
	IfaceDialFn     func(iface string) *net.Dialer // for plain DNS iface-bound; required if any endpoint has NDMSName mapped
	HTTPClient      *http.Client                 // for DoH; if nil, http.DefaultClient
	PerProbeTimeout time.Duration                // default 3s
	IfaceMap        map[string]string            // NDMSName → linux iface; injected from agent main
}

func (DNS) Name() string { return "dns" }

func (c DNS) Run(ctx context.Context, _ Deps) wire.Check {
	start := time.Now()
	if c.PerProbeTimeout <= 0 {
		c.PerProbeTimeout = 3 * time.Second
	}
	threshold := c.FailThreshold
	if threshold <= 0 {
		threshold = 1
	}
	httpc := c.HTTPClient
	if httpc == nil {
		httpc = http.DefaultClient
	}

	if len(c.Endpoints) == 0 {
		return OK(c.Name(), start, map[string]any{"endpoints": 0, "note": "no DNS endpoints discovered/configured"})
	}

	var failed []map[string]any
	for _, ep := range c.Endpoints {
		err := c.probeOne(ctx, ep, httpc)
		if err != nil {
			failed = append(failed, map[string]any{
				"type":      ep.Type,
				"target":    epTarget(ep),
				"ndms_name": ep.NDMSName,
				"err":       err.Error(),
			})
		}
	}

	details := map[string]any{
		"endpoints":   len(c.Endpoints),
		"failed":      failed,
		"failed_count": len(failed),
	}
	if len(failed) >= threshold {
		return Fail(c.Name(), start, fmt.Sprintf("%d/%d endpoints failed", len(failed), len(c.Endpoints)), details)
	}
	return OK(c.Name(), start, details)
}

func epTarget(ep keenetic.DNSEndpoint) string {
	switch ep.Type {
	case "plain", "dot":
		return fmt.Sprintf("%s:%d", ep.Host, ep.Port)
	case "doh":
		return ep.URL
	}
	return "?"
}

func (c DNS) probeOne(ctx context.Context, ep keenetic.DNSEndpoint, httpc *http.Client) error {
	switch ep.Type {
	case "plain":
		var dialer *net.Dialer
		if linuxIface := c.resolveIface(ep.NDMSName); linuxIface != "" && c.IfaceDialFn != nil {
			dialer = c.IfaceDialFn(linuxIface)
		}
		_, err := ProbePlainDNS(ctx, fmt.Sprintf("%s:%d", ep.Host, ep.Port), c.TestDomain+".", dialer, c.PerProbeTimeout)
		return err
	case "doh":
		_, err := ProbeDoH(ctx, ep.URL, c.TestDomain, httpc, c.PerProbeTimeout)
		return err
	case "dot":
		// DoT not implemented yet — count as fail-by-policy. Users with DoT
		// will see this in details and can either switch transport or wait.
		return fmt.Errorf("dot transport not implemented")
	default:
		return fmt.Errorf("unknown transport %q", ep.Type)
	}
}

// resolveIface translates an NDMSName (e.g. "Wireguard0") to a Linux iface
// name (e.g. "nwg0") via the precomputed map. Returns empty if not in the map
// (non-WG interface or unknown — fall back to system default routing).
func (c DNS) resolveIface(ndms string) string {
	if ndms == "" {
		return ""
	}
	return c.IfaceMap[ndms]
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/agent/checks/ -run TestDNS -v`
Expected: 5 PASS.

- [ ] **Step 5: Run full check suite**

Run: `go test ./...`
Expected: всё зелёное.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/checks/dns.go internal/agent/checks/dns_test.go
git commit -m "feat(checks): high-level DNS check with multi-endpoint + threshold

Probes each endpoint via plain UDP or DoH (DoT stub returns fail-by-policy
until implemented). Iface-bound dial applied when NDMSName→Linux mapping
present. Empty endpoints set returns OK with note (auto-discovery on
non-Keenetic device or no DNS configured)."
```

---

### Task 9: Migrate `agent.DNSCheckConfig` schema

**Files:**
- Modify: `internal/agent/config.go:65-74, 124-136`
- Create: `internal/agent/config_test.go` (если ещё нет)

**Why:** Drop старый `Providers []DNSProviderConfig`, добавить `Endpoints []DNSEndpointConfig` + `AutoDiscover bool`.

- [ ] **Step 1: Replace `DNSCheckConfig` types in `config.go`**

В `internal/agent/config.go`:

Удалить:
```go
type DNSProviderConfig struct {
	Name string `yaml:"name"`
	Host string `yaml:"host"`
}

type DNSCheckConfig struct {
	Providers     []DNSProviderConfig `yaml:"providers"`
	TestDomain    string              `yaml:"test_domain"`
	FailThreshold int                 `yaml:"fail_threshold"`
}
```

Заменить на:
```go
// DNSEndpointConfig represents one user-supplied DNS endpoint to probe.
// Auto-discovered endpoints (from `ndmc show running-config`) are merged
// with these at agent startup.
type DNSEndpointConfig struct {
	Type     string `yaml:"type"`               // "plain", "doh", "dot"
	Host     string `yaml:"host,omitempty"`     // for plain/dot
	Port     int    `yaml:"port,omitempty"`     // for plain/dot; default 53/853
	URL      string `yaml:"url,omitempty"`      // for doh
	NDMSName string `yaml:"ndms_name,omitempty"` // bind via iface for plain
}

type DNSCheckConfig struct {
	AutoDiscover  bool                `yaml:"auto_discover"` // discover endpoints from ndmc
	Endpoints     []DNSEndpointConfig `yaml:"endpoints"`     // explicit endpoints; merged with discovery
	TestDomain    string              `yaml:"test_domain"`
	FailThreshold int                 `yaml:"fail_threshold"`
}
```

- [ ] **Step 2: Update validation in `LoadConfig`**

В `LoadConfig` сейчас два DNS-related блока:
```go
if cfg.Checks.DNS.TestDomain == "" {
    cfg.Checks.DNS.TestDomain = "example.com"
}
if cfg.Checks.DNS.FailThreshold <= 0 {
    cfg.Checks.DNS.FailThreshold = 2
}
if len(cfg.Checks.DNS.Providers) == 0 {
    cfg.Checks.DNS.Providers = []DNSProviderConfig{
        {Name: "cloudflare", Host: "1.1.1.1"},
        {Name: "google", Host: "8.8.8.8"},
        {Name: "quad9", Host: "9.9.9.9"},
    }
}
```

Изменения:
1. Оставить блок `TestDomain` без изменений.
2. В блоке `FailThreshold` сменить дефолт `2` → `1`.
3. **Удалить весь** `if len(cfg.Checks.DNS.Providers) == 0 { ... }` блок.

Финальный вид этой части `LoadConfig`:
```go
if cfg.Checks.DNS.TestDomain == "" {
    cfg.Checks.DNS.TestDomain = "example.com"
}
// FailThreshold default = 1 (alert if any single endpoint is unreachable).
// Endpoints can be empty if AutoDiscover succeeds at runtime; that's not an error.
// AutoDiscover has NO default — user must explicitly set auto_discover: true.
if cfg.Checks.DNS.FailThreshold <= 0 {
    cfg.Checks.DNS.FailThreshold = 1
}
```

- [ ] **Step 3: Add config schema test**

Создать или дополнить `internal/agent/config_test.go`:

```go
package agent

import (
	"os"
	"path/filepath"
	"testing"
)

const minimalNewSchema = `
backend:
  url: https://wgmon.example.org
  token: abcdefghijklmnopqrstuvwxyz0123456789ABCD
agent:
  nickname: testkeen
  interval_sec: 60
checks:
  awg:
    interface: nwg0
    expected_exit_ip: 89.125.101.122
  dns:
    auto_discover: true
    test_domain: example.com
    fail_threshold: 1
    endpoints:
      - { type: doh, url: "https://my.example/dns-query" }
      - { type: plain, host: 1.1.1.1, port: 53, ndms_name: Wireguard1 }
`

func TestLoadConfig_NewDNSSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(minimalNewSchema), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Checks.DNS.AutoDiscover {
		t.Errorf("AutoDiscover not parsed")
	}
	if len(cfg.Checks.DNS.Endpoints) != 2 {
		t.Fatalf("Endpoints: %+v", cfg.Checks.DNS.Endpoints)
	}
	doh := cfg.Checks.DNS.Endpoints[0]
	if doh.Type != "doh" || doh.URL != "https://my.example/dns-query" {
		t.Errorf("doh: %+v", doh)
	}
	plain := cfg.Checks.DNS.Endpoints[1]
	if plain.Type != "plain" || plain.Host != "1.1.1.1" || plain.Port != 53 || plain.NDMSName != "Wireguard1" {
		t.Errorf("plain: %+v", plain)
	}
}
```

- [ ] **Step 4: Run config tests**

Run: `go test ./internal/agent/ -run TestLoadConfig -v`
Expected: PASS (включая существующие тесты, если они есть).

- [ ] **Step 5: Run all tests — sanity (likely break agent main)**

Run: `go test ./...`
Expected: возможен компиляционный fail в `cmd/agent/main.go` (используется `dnsCheckFromCfg` со старым типом). Это будет починено в Task 11.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/config.go internal/agent/config_test.go
git commit -m "feat(agent/config): DNSCheckConfig schema migration

Replace static Providers list with Endpoints + AutoDiscover. NDMSName
field marks per-iface plain DNS for iface-bound dial. No backwards-
compat shim — single live agent, manual config rewrite during deploy."
```

---

### Task 10: `wg-monitor-cli show-discovered-dns` debug helper

**Files:**
- Create: `cmd/wg-monitor-cli/show_dns.go`
- Modify: `cmd/wg-monitor-cli/main.go` (зарегистрировать subcommand)

**Why:** Удобно отлаживать auto-discovery вручную: запустил CLI → видишь всё, что увидит агент.

- [ ] **Step 1: Read existing CLI structure**

Run: `cat cmd/wg-monitor-cli/main.go`
(чтобы понять, какие subcommand'ы уже есть, и как они роутятся)

- [ ] **Step 2: Create subcommand**

Создать `cmd/wg-monitor-cli/show_dns.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/anex/wg-monitor/internal/agent/keenetic"
)

func cmdShowDiscoveredDNS(args []string) {
	fs := flag.NewFlagSet("show-discovered-dns", flag.ExitOnError)
	awgmgrURL := fs.String("awg-manager-url", "http://127.0.0.1:2222", "awg-manager API base URL (for NDMSName→linux iface map)")
	ndmcBin := fs.String("ndmc", "/bin/ndmc", "path to /bin/ndmc")
	_ = fs.Parse(args)

	runner := osRunner{}
	ndmc := keenetic.NDMC{Runner: runner, BinaryPath: *ndmcBin}
	rc, err := ndmc.Show(context.Background(), "running-config")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ndmc show running-config: %v\n", err)
		os.Exit(1)
	}
	eps := keenetic.ParseDNSEndpoints(rc)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ifaceMap, mapErr := keenetic.FetchIfaceMap(ctx, keenetic.IfaceMapOptions{AwgManagerURL: *awgmgrURL})
	if mapErr != nil {
		fmt.Fprintf(os.Stderr, "warning: fetch iface map: %v\n", mapErr)
	}

	fmt.Printf("Discovered %d DNS endpoint(s):\n", len(eps))
	for i, ep := range eps {
		fmt.Printf("  [%d] type=%s ", i+1, ep.Type)
		switch ep.Type {
		case "plain", "dot":
			fmt.Printf("target=%s:%d", ep.Host, ep.Port)
			if ep.NDMSName != "" {
				linux := ifaceMap[ep.NDMSName]
				if linux == "" {
					linux = "(unmapped)"
				}
				fmt.Printf(" via NDMS=%s linux=%s", ep.NDMSName, linux)
			}
		case "doh":
			fmt.Printf("url=%s", ep.URL)
		}
		fmt.Println()
	}
}

// osRunner — minimal exec.Cmd-based CmdRunner for the CLI.
type osRunner struct{}

func (osRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	return string(out), err
}
```

- [ ] **Step 3: Wire subcommand into main.go**

В `cmd/wg-monitor-cli/main.go`, в switch по subcommand добавить case:
```go
case "show-discovered-dns":
    cmdShowDiscoveredDNS(os.Args[2:])
```

(Точное место зависит от текущего main.go — agent делает `git diff` после обновления.)

- [ ] **Step 4: Smoke build**

Run: `cd /c/Users/Anex/Projects/wg-monitor && go build ./cmd/wg-monitor-cli/`
Expected: `wg-monitor-cli.exe` (или `wg-monitor-cli` под linux) собирается без ошибок.

- [ ] **Step 5: Commit**

```bash
git add cmd/wg-monitor-cli/show_dns.go cmd/wg-monitor-cli/main.go
git commit -m "feat(cli): show-discovered-dns subcommand for auto-discovery debug

Runs the same ndmc parser + awg-manager iface map that the agent uses,
prints discovered endpoints in a human-readable format. Useful for
verifying setup before deploying agent to a new Keenetic."
```

---

### Task 11: Wire DNS auto-discovery into `cmd/agent/main.go`

**Files:**
- Modify: `cmd/agent/main.go` (replace `dnsCheckFromCfg` with new constructor)

**Why:** Финальная интеграция — агент должен на старте провести discovery, объединить с manual endpoints и передать всё в `checks.DNS`.

- [ ] **Step 1: Replace dnsCheckFromCfg with new builder**

Удалить функцию `dnsCheckFromCfg` (строки ~66-72 в текущем `cmd/agent/main.go`).

В `main()` после `cfg, err := agent.LoadConfig(...)` и логирования заменить блок постройки `chks` на:

```go
client := agent.NewClient(cfg.Backend.URL, cfg.Backend.Token, Version, 10*time.Second)

// HTTP client for awg_routing/awg_marker — iface-bound to checks.awg.interface.
httpc := &http.Client{
	Transport: &http.Transport{
		DialContext: checks.IfaceDialer(cfg.Checks.AWG.Interface).DialContext,
	},
	Timeout: 12 * time.Second,
}

// DNS check: auto-discover + manual merge.
dnsCheck, dnsErr := buildDNSCheck(cfg, logger)
if dnsErr != nil {
	logger.Warn("dns check setup", "err", dnsErr, "note", "DNS check disabled this run")
}

chks := []checks.Check{
	checks.AwgHandshake{Iface: cfg.Checks.AWG.Interface, MaxAge: cfg.Checks.AWG.HandshakeMaxAge()},
	checks.AwgRouting{Iface: cfg.Checks.AWG.Interface, URL: cfg.Checks.AWG.RoutingURL(), Expected: cfg.Checks.AWG.ExpectedExitIP},
	checks.AwgMarker{Iface: cfg.Checks.AWG.Interface, URL: cfg.Checks.AWG.ResolvedMarkerURL(), MaxRetries: 3, BaseBackoff: 250 * time.Millisecond},
}
if dnsCheck != nil {
	chks = append(chks, dnsCheck)
}
```

- [ ] **Step 2: Add buildDNSCheck function**

В тот же `cmd/agent/main.go` добавить:

```go
func buildDNSCheck(cfg *agent.Config, logger *slog.Logger) (checks.Check, error) {
	dc := cfg.Checks.DNS

	var endpoints []keenetic.DNSEndpoint
	if dc.AutoDiscover {
		runner := checks.OSExec{}
		ndmc := keenetic.NDMC{Runner: runner}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		rc, err := ndmc.Show(ctx, "running-config")
		cancel()
		if err != nil {
			logger.Warn("dns auto-discover skipped", "err", err)
		} else {
			endpoints = append(endpoints, keenetic.ParseDNSEndpoints(rc)...)
			logger.Info("dns auto-discovered", "count", len(endpoints))
		}
	}

	// Merge with manual endpoints from config.
	for _, ec := range dc.Endpoints {
		ep := keenetic.DNSEndpoint{
			Type:     ec.Type,
			Host:     ec.Host,
			Port:     ec.Port,
			URL:      ec.URL,
			NDMSName: ec.NDMSName,
		}
		if (ep.Type == "plain" || ep.Type == "dot") && ep.Port == 0 {
			if ep.Type == "plain" {
				ep.Port = 53
			} else {
				ep.Port = 853
			}
		}
		endpoints = append(endpoints, ep)
	}

	// Iface map for NDMSName resolution.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ifaceMap, err := keenetic.FetchIfaceMap(ctx, keenetic.IfaceMapOptions{})
	if err != nil {
		logger.Warn("iface map unavailable; plain DNS will use default routing", "err", err)
		ifaceMap = nil
	}

	return checks.DNS{
		Endpoints:       endpoints,
		TestDomain:      dc.TestDomain,
		FailThreshold:   dc.FailThreshold,
		IfaceDialFn:     checks.IfaceDialer,
		HTTPClient:      &http.Client{Timeout: 5 * time.Second},
		PerProbeTimeout: 3 * time.Second,
		IfaceMap:        ifaceMap,
	}, nil
}
```

- [ ] **Step 3: Add `keenetic` import**

В блок import `cmd/agent/main.go` добавить:
```go
"github.com/anex/wg-monitor/internal/agent/keenetic"
```

- [ ] **Step 4: Build agent**

Run: `cd /c/Users/Anex/Projects/wg-monitor && GOOS=linux GOARCH=arm64 go build -o /tmp/wg-monitor-agent ./cmd/agent/`
(Под Windows GitBash может быть нюанс с GOOS — если не работает, использовать PowerShell.)
Expected: бинарник собрался без ошибок.

- [ ] **Step 5: Run all tests**

Run: `go test ./...`
Expected: всё зелёное.

- [ ] **Step 6: Commit**

```bash
git add cmd/agent/main.go
git commit -m "feat(agent/main): wire DNS check with auto-discovery + iface map

On startup: (1) if AutoDiscover enabled, run ndmc show running-config and
parse endpoints; (2) merge with manual endpoints from config.yaml; (3)
fetch NDMSName→Linux-iface map from awg-manager. All errors are logged
as warnings; DNS check still runs (with empty/partial endpoints) rather
than crashing."
```

---

### Task 12: Live deploy + verify on MyRouter

**Files:** Live system; production config update.

**Why:** Phase 4 systematic-debugging — verify the fix actually works under real conditions before tagging.

- [ ] **Step 1: Cross-compile agent for arm64**

Run (PowerShell — Windows-friendly):
```powershell
$env:GOOS="linux"; $env:GOARCH="arm64"; go build -ldflags "-X main.Version=0.3.0-checksfix-dev" -o bin/linux-arm64/wg-monitor ./cmd/agent/
```
Expected: `bin/linux-arm64/wg-monitor` создан, ~10-15 MB ELF.

- [ ] **Step 2: Optionally UPX-compress (matches Stage 1 deploy pattern)**

Run: `upx --best bin/linux-arm64/wg-monitor`
Expected: бинарник ~1.5-2 MiB.

- [ ] **Step 3: Update local config.yaml fixture for testkeen**

Файл `deploy/agent/configs/testkeen.yaml` (или эквивалент — узнать через `ls deploy/agent/configs/`):

```yaml
backend:
  url: https://wgmonitor.jkaotlic.duckdns.org
  token: <existing testkeen token — keep as-is from current config>

agent:
  nickname: testkeen
  interval_sec: 60

checks:
  awg:
    interface: nwg0
    handshake_max_age_sec: 180
    expected_exit_ip: 89.125.101.122
    # routing_probe_url defaults to https://1.1.1.1/cdn-cgi/trace
    # marker_url defaults to http://www.gstatic.com/generate_204
  dns:
    auto_discover: true
    test_domain: example.com
    fail_threshold: 1
```

(Token из текущего production config — переносится без изменений.)

- [ ] **Step 4: Deploy via existing deploy_keenetic.py**

Run:
```bash
python deploy/agent/deploy_keenetic.py --config deploy/agent/configs/testkeen.yaml
```
(Скрипт читает password из memory file автоматически, как в `deploy/diag/keenetic_diag.py` — если deploy_keenetic.py этого не делает, прокинуть `--password` через env-var или интерактивно.)

Expected: deploy завершён, init.d сервис S99wg-monitor рестартован.

- [ ] **Step 5: Verify agent is running with new code**

Через diag-скрипт:
```bash
python deploy/diag/keenetic_diag.py agent-state
```
Expected: видна process line с new version `0.3.0-checksfix-dev`.

- [ ] **Step 6: Watch backend journal for next report cycle**

На VPS Main:
```bash
ssh ... 'sudo journalctl -u wg-monitor-backend -n 60 --no-pager | tail -40'
```
Wait ≥60 sec for next agent report.

Expected: видна запись с `nickname=testkeen`, и в `checks` массив **все 4 чека**:
- `awg_handshake=ok`
- `awg_routing=ok` (новый: cdn-cgi/trace)
- `awg_marker=ok` (новый default)
- `dns=ok` (новый чек, с deteceted endpoints)

Если `dns=fail` с конкретным failed endpoint — проверить что endpoint реально жив (через diag-скрипт). Если все fail — баг auto-discovery, return to Task 4-5 с failing case.

- [ ] **Step 7: Verify no false-positive HARD alerts in TG**

Check Telegram group `Status_Group`, topic `👤 testkeen`. После 3 циклов (3-4 минуты live) не должно появиться новых HARD-алертов из-за false-positive checks.

- [ ] **Step 8: Commit production config update**

```bash
git add deploy/agent/configs/testkeen.yaml
git commit -m "feat(deploy): testkeen config — DNS auto-discover, new defaults

Old DNSCheckConfig.providers replaced by auto_discover: true. Routing
and marker URLs use new defaults. Token unchanged."
```

---

### Task 13: Tag release + memory update

- [ ] **Step 1: Tag release**

Run:
```bash
git tag -a v0.3.0-checksfix -m "Checks fix: cdn-cgi/trace, gstatic, pure-Go DNS w/ auto-discovery

- awg_routing: api.ipify.org → 1.1.1.1/cdn-cgi/trace (DNS-block-resistant)
- awg_marker default: youtube/-/manifest → gstatic/generate_204 (always 204)
- dns: pure-Go plain UDP + DoH; auto-discover from /bin/ndmc;
        replaces dig-shell impl that broke on Entware (no dig binary)
- Live verified on MyRouter testkeen, all 4 checks ok end-to-end"
```

- [ ] **Step 2: Update memory files**

Edit `~/.claude/projects/C--Users-Anex/memory/project_wg_monitor.md`:
- Поправить **первое же** упоминание в Stage 1 секции: «3 чека (awg_routing, awg_marker, dns_doh) fail на Keenetic из-за iface-bound dialer» — это **была ложная гипотеза**. Заменить на: «v0.3.0-checksfix (2026-04-27) — 3 чека пофикшены: api.ipify.org блокировался DNS-фильтром (заменён на cdn-cgi/trace), youtube/-/manifest 404 (заменён на gstatic/204), dig отсутствует в Entware (DNS чек переписан на pure-Go с auto-discovery из ndmc)».
- Поправить маппинг nwg0/nwg1 в `host_keenetic.md`: actual mapping is dynamic, sourced from awg-manager `/api/tunnels/all`. На сейчас (2026-04-27): `nwg0 = awg12 = VPS Amnezia 89.125.101.122 (NDMS Wireguard0)`, `nwg1 = awg11 = VPS Main 103.106.1.253 (NDMS Wireguard1)`.

Edit `~/.claude/projects/C--Users-Anex/memory/session_context.md`: новая запись с summary этой сессии (Phase 1 systematic-debugging findings → план + execution → live verify → tag).

Edit `~/.claude/projects/C--Users-Anex/memory/active_tasks.md`: **wg-monitor Stage 1.5 (checks-fix)** ✅ COMPLETE, добавить v0.3.0-checksfix tag.

- [ ] **Step 3: Push tag (optional — confirm with user)**

If user wants remote tag:
```bash
git push origin feature/stage-1 --tags
```

(Skip if repo still local-only.)

---

## Acceptance criteria

After all tasks:

1. ✅ `go test ./...` — всё зелёное
2. ✅ `go build ./cmd/agent/` собирается под linux/arm64
3. ✅ Live agent на testkeen MyRouter шлёт reports с **4 ok checks** (awg_handshake, awg_routing, awg_marker, dns)
4. ✅ В TG-топике `👤 testkeen` нет новых HARD-алертов через 3 цикла после deploy
5. ✅ `wg-monitor-cli show-discovered-dns` на роутере выводит 5 endpoints (4 plain + 1 DoH) совпадающих со скрином KeenetiсOS Web UI
6. ✅ Tag `v0.3.0-checksfix` создан
7. ✅ Memory обновлена; ложная гипотеза «iface-bound dialer broken» удалена/исправлена

---

## Out of scope (для следующих этапов)

- **Stage 2 backlog**: inline button callbacks (silence/ack/history/mute), StaleHards re-alert poller (~6h)
- **Stage 5 backlog**: install.sh + self-update + раскатка
- **DoT prober**: stub в `dns.go::probeOne` возвращает «not implemented». Реализация — отдельная задача когда у юзеров появится DoT.
- **`wg-monitor-cli sync-keenetic-dns`**: автогенерация manual endpoints в `config.yaml` из ndmc — convenience, не блокер.
- **Cross-platform DNS auto-discovery** (OpenWrt, ASUS Merlin, etc.): ndmc — Keenetic-only. Универсальный путь — через `/etc/resolv.conf` + iproute2, отдельная задача.
