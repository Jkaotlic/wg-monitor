package dnswatch

import (
	"reflect"
	"strings"
	"testing"
)

const (
	yandexDoT     = "tls upstream common.dot.dns.yandex.net"
	yandexDoH     = "https upstream https://common.dot.dns.yandex.net/dns-query"
	quad9DoH      = "https upstream https://dns.quad9.net/dns-query"
	controlDDoH   = "https upstream https://freedns.controld.com/p0"
	cloudflareDoH = "https upstream https://cloudflare-dns.com/dns-query"
	cloudflareDoT = "tls upstream 1.1.1.1 sni cloudflare-dns.com"
	quad9DoT      = "tls upstream 9.9.9.9 sni dns.quad9.net"
)

var ruZones = []string{"ru", "su", "xn--p1ai", "xn--80adxhks", "xn--d1acj3b", "xn--p1acf", "tatar"}
var pinnedZones = []string{"themoviedb.org", "tmdb.org", "b-cdn.net", "phncdn.com", "pornhub.com", "rncdn7.com"}

func defaultSetConfig() Config {
	return Config{
		MaxForeign:        3,
		RUZones:           DefaultRUZones,
		RUCandidates:      DefaultRUCandidates,
		ForeignCandidates: DefaultForeignCandidates,
		PinnedZones:       DefaultPinnedZones,
		PinnedCandidate:   DefaultPinnedCandidate,
	}
}

func domainLines(base string, zones []string) []string {
	out := make([]string, 0, len(zones))
	for _, z := range zones {
		out = append(out, base+" domain "+z)
	}
	return out
}

// TestDefaults_Verbatim pins the operator's split set (his AGH, 11.09.2026).
func TestDefaults_Verbatim(t *testing.T) {
	if !reflect.DeepEqual(DefaultRUZones, ruZones) {
		t.Errorf("DefaultRUZones = %v", DefaultRUZones)
	}
	if !reflect.DeepEqual(DefaultRUCandidates, []string{yandexDoT, yandexDoH}) {
		t.Errorf("DefaultRUCandidates = %v", DefaultRUCandidates)
	}
	if !reflect.DeepEqual(DefaultForeignCandidates, []string{quad9DoH, controlDDoH, cloudflareDoH, cloudflareDoT, quad9DoT}) {
		t.Errorf("DefaultForeignCandidates = %v", DefaultForeignCandidates)
	}
	if !reflect.DeepEqual(DefaultPinnedZones, pinnedZones) {
		t.Errorf("DefaultPinnedZones = %v", DefaultPinnedZones)
	}
	if DefaultPinnedCandidate != cloudflareDoH {
		t.Errorf("DefaultPinnedCandidate = %q", DefaultPinnedCandidate)
	}
}

// TestBuildFallbackSet_SplitLikeAGH: everything live → the mirror of the
// operator's AGH upstream_dns: 3 foreign without domain, 7 Yandex DoT domain
// lines, 6 Cloudflare-pinned domain lines — in that order.
func TestBuildFallbackSet_SplitLikeAGH(t *testing.T) {
	lines, ruDegraded, ok := BuildFallbackSet(defaultSetConfig(), DefaultRUCandidates, DefaultForeignCandidates)
	if !ok || ruDegraded {
		t.Fatalf("ok=%v ruDegraded=%v, want true/false", ok, ruDegraded)
	}
	want := []string{quad9DoH, controlDDoH, cloudflareDoH}
	want = append(want, domainLines(yandexDoT, ruZones)...)
	want = append(want, domainLines(cloudflareDoH, pinnedZones)...)
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines:\n got %q\nwant %q", lines, want)
	}
	if len(lines) != 3+7+6 {
		t.Fatalf("len = %d, want 16", len(lines))
	}
}

// TestBuildFallbackSet_NoForeignNoSwitch: without a single live foreign
// resolver there is nothing to carry the bulk of the traffic — the caller must
// not switch at all (no_live_fallback), even with Yandex alive.
func TestBuildFallbackSet_NoForeignNoSwitch(t *testing.T) {
	lines, _, ok := BuildFallbackSet(defaultSetConfig(), DefaultRUCandidates, nil)
	if ok {
		t.Fatal("ok must be false without a live foreign candidate")
	}
	if len(lines) != 0 {
		t.Fatalf("no lines expected, got %q", lines)
	}
}

// TestBuildFallbackSet_YandexDeadIsRUDegraded: no live RU candidate → RU zones
// fall through to the foreign pool (no RU lines) and ruDegraded is reported.
func TestBuildFallbackSet_YandexDeadIsRUDegraded(t *testing.T) {
	lines, ruDegraded, ok := BuildFallbackSet(defaultSetConfig(), nil, DefaultForeignCandidates)
	if !ok || !ruDegraded {
		t.Fatalf("ok=%v ruDegraded=%v, want true/true", ok, ruDegraded)
	}
	want := []string{quad9DoH, controlDDoH, cloudflareDoH}
	want = append(want, domainLines(cloudflareDoH, pinnedZones)...)
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines:\n got %q\nwant %q", lines, want)
	}
	for _, l := range lines {
		if strings.Contains(l, "yandex") {
			t.Errorf("dead Yandex must not be in the set: %q", l)
		}
	}
}

// TestBuildFallbackSet_SecondRUCandidateWhenDoTDead: Yandex DoT dead, Yandex
// DoH alive → the RU zones go to the DoH line.
func TestBuildFallbackSet_SecondRUCandidateWhenDoTDead(t *testing.T) {
	lines, ruDegraded, ok := BuildFallbackSet(defaultSetConfig(), []string{yandexDoH}, DefaultForeignCandidates)
	if !ok || ruDegraded {
		t.Fatalf("ok=%v ruDegraded=%v, want true/false", ok, ruDegraded)
	}
	want := []string{quad9DoH, controlDDoH, cloudflareDoH}
	want = append(want, domainLines(yandexDoH, ruZones)...)
	want = append(want, domainLines(cloudflareDoH, pinnedZones)...)
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines:\n got %q\nwant %q", lines, want)
	}
}

// TestBuildFallbackSet_CapsForeignAtMax: only the first MaxForeign live foreign
// lines go in (parallel racing of a dead or slow resolver costs every query).
// The pinned zones depend on Cloudflare being LIVE, not on it making the cap.
func TestBuildFallbackSet_CapsForeignAtMax(t *testing.T) {
	cfg := defaultSetConfig()
	cfg.MaxForeign = 2
	lines, _, ok := BuildFallbackSet(cfg, nil, DefaultForeignCandidates)
	if !ok {
		t.Fatal("ok must be true")
	}
	want := []string{quad9DoH, controlDDoH}
	want = append(want, domainLines(cloudflareDoH, pinnedZones)...)
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines:\n got %q\nwant %q", lines, want)
	}

	// Default cap is 3 even when the config leaves it unset.
	cfg.MaxForeign = 0
	lines, _, _ = BuildFallbackSet(cfg, nil, []string{quad9DoT, cloudflareDoT, controlDDoH, quad9DoH})
	if want := []string{quad9DoT, cloudflareDoT, controlDDoH}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("unset cap:\n got %q\nwant %q", lines, want)
	}
}

// TestBuildFallbackSet_PinnedOnlyWhenCloudflareLive: the pinned zones go to
// Cloudflare DoH only when that exact candidate passed its probe. Cloudflare
// DoT being alive does not count — it is a different line.
func TestBuildFallbackSet_PinnedOnlyWhenCloudflareLive(t *testing.T) {
	lines, _, ok := BuildFallbackSet(defaultSetConfig(), DefaultRUCandidates, []string{quad9DoH, controlDDoH, cloudflareDoT, quad9DoT})
	if !ok {
		t.Fatal("ok must be true")
	}
	want := []string{quad9DoH, controlDDoH, cloudflareDoT}
	want = append(want, domainLines(yandexDoT, ruZones)...)
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines:\n got %q\nwant %q", lines, want)
	}
	for _, l := range lines {
		if strings.Contains(l, "themoviedb.org") || strings.Contains(l, "phncdn.com") {
			t.Errorf("pinned zone without live Cloudflare DoH: %q", l)
		}
	}
}

// TestBuildFallbackSet_NoDuplicates: repeated candidates or zones never yield
// the same dns-proxy line twice, and a repeat does not eat a foreign slot.
func TestBuildFallbackSet_NoDuplicates(t *testing.T) {
	cfg := defaultSetConfig()
	cfg.RUZones = []string{"ru", "su", "ru"}
	cfg.PinnedZones = []string{"tmdb.org", "tmdb.org"}
	lines, _, ok := BuildFallbackSet(cfg, []string{yandexDoT, yandexDoT}, []string{quad9DoH, quad9DoH, cloudflareDoH, controlDDoH})
	if !ok {
		t.Fatal("ok must be true")
	}
	want := []string{quad9DoH, cloudflareDoH, controlDDoH,
		yandexDoT + " domain ru", yandexDoT + " domain su",
		cloudflareDoH + " domain tmdb.org"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines:\n got %q\nwant %q", lines, want)
	}
}

// TestBuildFallbackSet_NeverGoogle: Google steers CDNs to US edges, so it is
// neither a default nor ever produced by the builder.
func TestBuildFallbackSet_NeverGoogle(t *testing.T) {
	google := func(s string) bool {
		return strings.Contains(s, "dns.google") || strings.Contains(s, "8.8.8.8") || strings.Contains(s, "8.8.4.4")
	}
	var defaults []string
	defaults = append(defaults, DefaultRUCandidates...)
	defaults = append(defaults, DefaultForeignCandidates...)
	defaults = append(defaults, DefaultPinnedCandidate)
	for _, d := range defaults {
		if google(d) {
			t.Errorf("Google in defaults: %q", d)
		}
	}
	lines, _, _ := BuildFallbackSet(defaultSetConfig(), DefaultRUCandidates, DefaultForeignCandidates)
	for _, l := range lines {
		if google(l) {
			t.Errorf("Google in the fallback set: %q", l)
		}
	}
	// Nor with Yandex dead (the RU zones then ride the foreign pool).
	lines, _, _ = BuildFallbackSet(defaultSetConfig(), nil, DefaultForeignCandidates)
	for _, l := range lines {
		if google(l) {
			t.Errorf("Google in the RU-degraded set: %q", l)
		}
	}
}
