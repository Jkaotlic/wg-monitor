package dnswatch

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/dnsref"
)

// CHK-03: KeenOS держит не больше восьми DoT-строк («server list limit
// exceeded, the maximum is 8 addresses»). Запасной набор, не знавший лимита,
// оставлял хвост «недобавленных» строк: сторож повторял их каждую минуту и
// каждую минуту переписывал файл состояния во флеш.
func TestTrimToDoTLimit(t *testing.T) {
	present := []string{
		"tls upstream 203.0.113.1:853 sni a.example.com",
		"tls upstream 203.0.113.2:853 sni b.example.com",
		"tls upstream 203.0.113.3:853 sni c.example.com",
		"tls upstream 203.0.113.4:853 sni d.example.com",
		"tls upstream 203.0.113.5:853 sni e.example.com",
	}
	set := []string{
		"https upstream https://dns.quad9.net/dns-query",
		"tls upstream 203.0.113.1 sni a.example.com", // уже есть (эхо с :853)
		"tls upstream common.dot.dns.yandex.net domain ru",
		"tls upstream common.dot.dns.yandex.net domain su",
		"tls upstream common.dot.dns.yandex.net domain xn--p1ai",
		"tls upstream common.dot.dns.yandex.net domain xn--80adxhks",
		"https upstream https://cloudflare-dns.com/dns-query domain tmdb.org",
	}
	kept, dropped := trimToDoTLimit(set, present, 8)
	tls := 0
	for _, l := range present {
		if strings.HasPrefix(l, "tls ") {
			tls++
		}
	}
	for _, l := range kept {
		if strings.HasPrefix(l, "tls ") && l != "tls upstream 203.0.113.1 sni a.example.com" {
			tls++
		}
	}
	if tls > 8 {
		t.Fatalf("final DoT lines = %d > 8: kept=%v", tls, kept)
	}
	if len(dropped) != 1 || dropped[0] != "tls upstream common.dot.dns.yandex.net domain xn--80adxhks" {
		t.Fatalf("dropped=%v, want the last RU zone only (tail first)", dropped)
	}
	for _, want := range []string{"https upstream https://dns.quad9.net/dns-query", "https upstream https://cloudflare-dns.com/dns-query domain tmdb.org"} {
		found := false
		for _, l := range kept {
			found = found || l == want
		}
		if !found {
			t.Fatalf("DoH line %q must not be trimmed (the limit is DoT only): %v", want, kept)
		}
	}
}

// Уход на запасные при роутере, где DoT-слоты почти заняты: запасной набор
// обрезается под лимит, недостающих строк нет, повторы не крутятся.
func TestFallbackRespectsDoTLimitAndStopsRetrying(t *testing.T) {
	var lines []string
	for i := 0; i < 5; i++ {
		lines = append(lines, "tls upstream 203.0.113."+string(rune('1'+i))+":853 sni dot"+string(rune('a'+i))+".example.com")
	}
	r := newRouter(append([]string{ownLine}, lines...)...)
	limit := dnsref.KeeneticDoTLimit
	r.onCall = func(cmd string) {
		if strings.HasPrefix(cmd, "dns-proxy tls ") {
			n := 0
			for _, l := range r.lines {
				if strings.HasPrefix(l, "tls ") {
					n++
				}
			}
			if n >= limit {
				r.failOn[cmd] = true // KeenOS: server list limit exceeded
			}
		}
	}
	h := newHarness(t, r)
	h.goFallback()
	if len(h.w.saved.Missing) != 0 {
		t.Fatalf("missing after fallback = %v: lines beyond the DoT limit were planned", h.w.saved.Missing)
	}
	if _, err := os.Stat(h.statePath); err != nil {
		t.Fatal(err)
	}
	writes := h.w.stateWrites
	calls := len(r.calls)
	h.tick(time.Minute)
	h.tick(time.Minute)
	if h.w.stateWrites != writes {
		t.Fatalf("state file rewritten on quiet ticks: %d -> %d", writes, h.w.stateWrites)
	}
	for _, c := range r.calls[calls:] {
		if strings.HasPrefix(c, "dns-proxy ") {
			t.Fatalf("quiet ticks retried %q", c)
		}
	}
}

// Файл состояния пишется только при перемене: тик без изменений -- без
// записи во флеш.
func TestSaveSkipsUnchangedState(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	p := persisted{Mode: ModeFallback, Pending: "", Leftover: []string{"x"}}
	h.w.save(p)
	h.w.save(p)
	if h.w.stateWrites != 1 {
		t.Fatalf("identical state written %d times, want 1", h.w.stateWrites)
	}
	p.Leftover = nil
	h.w.save(p)
	if h.w.stateWrites != 2 {
		t.Fatalf("changed state not written: writes=%d", h.w.stateWrites)
	}
}
