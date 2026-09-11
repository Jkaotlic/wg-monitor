package dnswatch

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type fixedSnapshot Snapshot

func (f fixedSnapshot) Snapshot() Snapshot { return Snapshot(f) }

func runCheck(t *testing.T, src SnapshotSource) wire.Check {
	t.Helper()
	c := Check{Source: src}
	if c.Name() != "resolver_guard" {
		t.Fatalf("name = %q, want resolver_guard", c.Name())
	}
	got := c.Run(context.Background(), checks.Deps{})
	if got.Name != "resolver_guard" {
		t.Fatalf("wire name = %q", got.Name)
	}
	return got
}

// TestCheck_PrimaryIsOK: contract with the backend — ok + {mode:"primary"}.
func TestCheck_PrimaryIsOK(t *testing.T) {
	got := runCheck(t, fixedSnapshot{Ready: true, Mode: ModePrimary})
	if got.Status != "ok" {
		t.Fatalf("status = %q", got.Status)
	}
	if !reflect.DeepEqual(got.Details, map[string]any{"mode": "primary"}) {
		t.Fatalf("details = %#v, want exactly {mode: primary}", got.Details)
	}
}

// TestCheck_FallbackIsFail: contract — fail + {mode:"fallback", since (RFC3339,
// UTC), reason:"fallback", foreign, ru, ru_degraded}.
func TestCheck_FallbackIsFail(t *testing.T) {
	msk := time.FixedZone("MSK", 3*3600)
	since := time.Date(2026, 9, 11, 15, 1, 0, 0, msk)
	foreign := []string{"https upstream https://dns.quad9.net/dns-query", "tls upstream 1.1.1.1 sni cloudflare-dns.com"}
	got := runCheck(t, fixedSnapshot{Ready: true, Mode: ModeFallback, Since: since, Foreign: foreign,
		RU: "tls upstream common.dot.dns.yandex.net"})
	if got.Status != "fail" {
		t.Fatalf("status = %q", got.Status)
	}
	d := got.Details
	if d["mode"] != "fallback" || d["reason"] != "fallback" {
		t.Errorf("mode/reason = %v/%v", d["mode"], d["reason"])
	}
	if d["since"] != "2026-09-11T12:01:00Z" {
		t.Errorf("since = %v, want RFC3339 UTC 2026-09-11T12:01:00Z", d["since"])
	}
	if _, err := time.Parse(time.RFC3339, d["since"].(string)); err != nil {
		t.Errorf("since must parse as RFC3339: %v", err)
	}
	if !reflect.DeepEqual(d["foreign"], foreign) {
		t.Errorf("foreign = %#v", d["foreign"])
	}
	if d["ru"] != "tls upstream common.dot.dns.yandex.net" || d["ru_degraded"] != false {
		t.Errorf("ru = %v, ru_degraded = %v", d["ru"], d["ru_degraded"])
	}
	if _, ok := d["leftover"]; ok {
		t.Errorf("no leftover key without leftovers: %#v", d)
	}
	if e, _ := d["error"].(string); e == "" {
		t.Errorf("a failing check carries an error text, details %#v", d)
	}
}

// TestCheck_FallbackRUDegraded: Yandex dead → ru "" and ru_degraded true.
func TestCheck_FallbackRUDegraded(t *testing.T) {
	got := runCheck(t, fixedSnapshot{Ready: true, Mode: ModeFallback, Since: t0, RUDegraded: true,
		Foreign: []string{"https upstream https://dns.quad9.net/dns-query"}})
	if got.Status != "fail" {
		t.Fatalf("status = %q", got.Status)
	}
	if ru, ok := got.Details["ru"]; !ok || ru != "" || got.Details["ru_degraded"] != true {
		t.Fatalf("details = %#v, want ru \"\" + ru_degraded true", got.Details)
	}
}

// TestCheck_NoLiveFallbackIsFail: contract — fail + {reason:"no_live_fallback"}.
func TestCheck_NoLiveFallbackIsFail(t *testing.T) {
	got := runCheck(t, fixedSnapshot{Ready: true, Mode: ModePrimary, NoLiveFallback: true, Fails: 3})
	if got.Status != "fail" || got.Details["reason"] != "no_live_fallback" {
		t.Fatalf("got %s %#v, want fail + reason no_live_fallback", got.Status, got.Details)
	}
	if got.Details["mode"] != "primary" {
		t.Errorf("mode = %v, want primary (nothing was switched)", got.Details["mode"])
	}
}

// TestCheck_LeftoverInDetails: a partial switch puts the lines that would not
// go (and would not come) into the details, whatever the mode.
func TestCheck_LeftoverInDetails(t *testing.T) {
	left := []string{"https upstream https://cloudflare-dns.com/dns-query domain tmdb.org"}
	miss := []string{"tls upstream 203.0.113.53 sni dot.example.com"}
	got := runCheck(t, fixedSnapshot{Ready: true, Mode: ModePrimary, Leftover: left, Missing: miss})
	if got.Status != "ok" || got.Details["mode"] != "primary" {
		t.Fatalf("got %s %#v", got.Status, got.Details)
	}
	if !reflect.DeepEqual(got.Details["leftover"], left) || !reflect.DeepEqual(got.Details["missing"], miss) {
		t.Fatalf("details = %#v", got.Details)
	}
	got = runCheck(t, fixedSnapshot{Ready: true, Mode: ModeFallback, Since: t0, Leftover: left,
		Foreign: []string{"https upstream https://dns.quad9.net/dns-query"}})
	if got.Status != "fail" || got.Details["reason"] != "fallback" || !reflect.DeepEqual(got.Details["leftover"], left) {
		t.Fatalf("got %s %#v", got.Status, got.Details)
	}
}

// TestCheck_ForeignLeftoverIsFail: round-4 contract — in a guarded primary
// with foreign lines of the fallback set still on the router: fail +
// {mode:"primary", reason:"foreign_leftover", leftover:[masked], since (RFC3339
// UTC)}. no_live_fallback (own resolver down) outranks it; idle is not guarded.
func TestCheck_ForeignLeftoverIsFail(t *testing.T) {
	msk := time.FixedZone("MSK", 3*3600)
	since := time.Date(2026, 9, 11, 15, 7, 0, 0, msk)
	left := []string{"https upstream https://cloudflare-dns.com/dns-query", "https upstream https://cloudflare-dns.com/dns-query domain tmdb.org"}
	snap := Snapshot{Ready: true, Mode: ModePrimary, Leftover: left, ForeignLeftover: left[:1], LeftoverSince: since}
	got := runCheck(t, fixedSnapshot(snap))
	d := got.Details
	if got.Status != "fail" || d["mode"] != "primary" || d["reason"] != "foreign_leftover" {
		t.Fatalf("got %s %#v", got.Status, d)
	}
	if d["since"] != "2026-09-11T12:07:00Z" || !reflect.DeepEqual(d["leftover"], left) {
		t.Fatalf("since/leftover = %v / %#v", d["since"], d["leftover"])
	}
	for _, k := range []string{"foreign", "ru", "ru_degraded"} {
		if _, ok := d[k]; ok {
			t.Errorf("fallback-only key %q in a primary report: %#v", k, d)
		}
	}

	noLive := snap
	noLive.NoLiveFallback = true
	if got := runCheck(t, fixedSnapshot(noLive)); got.Details["reason"] != "no_live_fallback" {
		t.Fatalf("own resolver down outranks the leftover: %#v", got.Details)
	}
	idle := snap
	idle.Idle = true
	if got := runCheck(t, fixedSnapshot(idle)); got.Status != "ok" {
		t.Fatalf("an idle watchdog guards nothing: %s %#v", got.Status, got.Details)
	}
	// Leftover `domain` lines only (no foreign line): cleanup, still ok.
	domainOnly := Snapshot{Ready: true, Mode: ModePrimary, Leftover: left[1:]}
	if got := runCheck(t, fixedSnapshot(domainOnly)); got.Status != "ok" {
		t.Fatalf("domain-only leftover is cleanup: %s %#v", got.Status, got.Details)
	}
}

// TestCheck_IdleAndNotReadyAreOK: nothing to guard / mode not yet read is
// not an outage — ok with a note, no new reason value.
func TestCheck_IdleAndNotReadyAreOK(t *testing.T) {
	got := runCheck(t, fixedSnapshot{Ready: true, Idle: true, Mode: ModePrimary})
	if got.Status != "ok" || got.Details["mode"] != "primary" || got.Details["idle"] != true {
		t.Fatalf("idle: %s %#v", got.Status, got.Details)
	}
	got = runCheck(t, fixedSnapshot{Mode: ModePrimary})
	if got.Status != "ok" || got.Details["mode"] != "primary" || got.Details["ready"] != false {
		t.Fatalf("not ready: %s %#v", got.Status, got.Details)
	}
	for _, g := range []wire.Check{got} {
		if _, ok := g.Details["reason"]; ok {
			t.Errorf("ok check must carry no reason: %#v", g.Details)
		}
	}
}

// TestCheck_ReadsTheLiveWatcher: end to end over the fake router — the check
// reports the watcher's fallback, and a partial return's leftover.
func TestCheck_ReadsTheLiveWatcher(t *testing.T) {
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.r.stuck["dns-proxy no https upstream https://cloudflare-dns.com/dns-query"] = true
	h.goFallback()
	got := runCheck(t, h.w)
	if got.Status != "fail" || got.Details["reason"] != "fallback" || got.Details["since"] != t0.Add(time.Minute).Format(time.RFC3339) {
		t.Fatalf("fallback: %s %#v", got.Status, got.Details)
	}
	if !reflect.DeepEqual(got.Details["foreign"], DefaultForeignCandidates[:3]) {
		t.Errorf("foreign = %#v", got.Details["foreign"])
	}

	h.goPrimary()
	got = runCheck(t, h.w)
	left, _ := got.Details["leftover"].([]string)
	// Round 4 ruling: the stuck line is the foreign Cloudflare one — unsafe.
	if got.Status != "fail" || got.Details["reason"] != DetailReasonForeignLeftover || len(left) != 1+len(DefaultPinnedZones) {
		t.Fatalf("partial return: %s %#v", got.Status, got.Details)
	}
	for _, v := range got.Details {
		if strings.Contains(strings.Join(anyStrings(v), " "), "secret-path") {
			t.Fatalf("details leak the endpoint path: %#v", got.Details)
		}
	}
}

func anyStrings(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []string:
		return x
	}
	return nil
}
