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
    https upstream https://dns.example.com:8443/dns-query dnsm
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
	if doh.Type != "doh" || doh.URL != "https://dns.example.com:8443/dns-query" {
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

func TestParseDNSEndpoints_IgnoresDoTWithInvalidPort(t *testing.T) {
	cfg := `
dns-proxy
    rebind-protect auto
    tls upstream 1.1.1.1:99999 dnss
    tls upstream dns.example.test:853 dnss
!
`
	eps := ParseDNSEndpoints(cfg)
	if len(eps) != 1 {
		t.Fatalf("want only valid DoT endpoint, got %+v", eps)
	}
	if eps[0].Host != "dns.example.test" || eps[0].Port != 853 {
		t.Fatalf("valid DoT endpoint not preserved: %+v", eps[0])
	}
}

// Пятнадцать строк -- это форма, которую KeenOS правда держит в running-config
// (проверено волной 0, шаг B5). До этой правки ParseDNSEndpoints узнавала шесть
// из них, и проба DoT из v0.30 не работала ни по одному эталонному апстриму:
// код был, входа для него не было.
const dnsProxyFixture = `
dns-proxy
    tls upstream 8.8.8.8 sni dns.google
    tls upstream 9.9.9.9 sni dns.quad9.net
    tls upstream 1.1.1.1 sni cloudflare-dns.com
    tls upstream common.dot.dns.yandex.net
    tls upstream common.dot.dns.yandex.net domain ru
    tls upstream common.dot.dns.yandex.net domain su
    tls upstream common.dot.dns.yandex.net domain xn--p1ai
    tls upstream common.dot.dns.yandex.net domain xn--80adxhks
    tls upstream common.dot.dns.yandex.net domain xn--d1acj3b
    tls upstream common.dot.dns.yandex.net domain xn--p1acf
    tls upstream common.dot.dns.yandex.net domain tatar
    tls upstream 94.140.14.14:853 sni dns.adguard-dns.com
    https upstream https://dns.quad9.net/dns-query
    https upstream https://cloudflare-dns.com/dns-query dnsm
    https upstream https://common.dot.dns.yandex.net/dns-query domain ru
!
`

func TestParseDNSEndpoints_SeesEveryReferenceLine(t *testing.T) {
	got := ParseDNSEndpoints(dnsProxyFixture)
	if len(got) != 15 {
		t.Fatalf("разобрано %d строк из 15: %+v", len(got), got)
	}
}

func TestParseDNSEndpoints_DoTWithoutPortDefaultsTo853(t *testing.T) {
	got := ParseDNSEndpoints(dnsProxyFixture)
	var found bool
	for _, ep := range got {
		if ep.Type == "dot" && ep.Host == "common.dot.dns.yandex.net" && ep.Zone == "" {
			found = true
			if ep.Port != 853 {
				t.Errorf("порт %d, хотим 853 по умолчанию (как уже делает dns_dot.go:128-131)", ep.Port)
			}
		}
	}
	if !found {
		t.Error("строка `tls upstream <host>` без порта не разобрана вовсе")
	}
}

func TestParseDNSEndpoints_KeepsSNIAndZone(t *testing.T) {
	got := ParseDNSEndpoints(dnsProxyFixture)
	bySNI := map[string]DNSEndpoint{}
	zones := map[string]bool{}
	for _, ep := range got {
		if ep.SNI != "" {
			bySNI[ep.SNI] = ep
		}
		if ep.Zone != "" {
			zones[ep.Zone] = true
		}
	}
	if ep, ok := bySNI["dns.google"]; !ok || ep.Host != "8.8.8.8" || ep.Port != 853 {
		t.Errorf("строка с sni разобрана как %+v", ep)
	}
	for _, z := range []string{"ru", "su", "xn--p1ai", "xn--80adxhks", "xn--d1acj3b", "xn--p1acf", "tatar"} {
		if !zones[z] {
			t.Errorf("зона %q потеряна: зонность обязана быть представима", z)
		}
	}
}

func TestParseDNSEndpoints_ExplicitPortSurvives(t *testing.T) {
	got := ParseDNSEndpoints(dnsProxyFixture)
	for _, ep := range got {
		if ep.Host == "94.140.14.14" {
			if ep.Port != 853 || ep.SNI != "dns.adguard-dns.com" {
				t.Errorf("явный порт и sni вместе разобраны как %+v", ep)
			}
			return
		}
	}
	t.Error("строка с явным портом и sni не найдена")
}

func TestParseDNSEndpoints_DoHKeepsZoneAndIgnoresDNSM(t *testing.T) {
	got := ParseDNSEndpoints(dnsProxyFixture)
	var zoned, plain int
	for _, ep := range got {
		if ep.Type != "doh" {
			continue
		}
		if ep.Zone == "ru" {
			zoned++
		}
		if ep.URL == "https://cloudflare-dns.com/dns-query" && ep.Zone == "" {
			plain++
		}
	}
	if zoned != 1 || plain != 1 {
		t.Errorf("doh: зонных %d (хотим 1), с dnsm без зоны %d (хотим 1)", zoned, plain)
	}
}

// Мусор в порте обязан отбрасывать строку целиком. Подмена его значением по
// умолчанию означала бы, что проверка идёт не туда, куда написано в конфиге.
func TestParseDNSEndpoints_RejectsGarbagePort(t *testing.T) {
	cfg := `
dns-proxy
    tls upstream 1.1.1.1:abc dnss
    tls upstream 2001:db8::1
!
`
	if eps := ParseDNSEndpoints(cfg); len(eps) != 0 {
		t.Fatalf("строка с мусором в порте принята: %+v", eps)
	}
}

// Ради скобочной формы IPv6 в регулярке переставлен порядок альтернатив --
// значит, она обязана быть покрыта тестом.
func TestParseDNSEndpoints_IPv6BracketForm(t *testing.T) {
	cfg := `
dns-proxy
    tls upstream [2001:db8::1] sni dns.example.com
    tls upstream [2001:db8::2]:853 domain ru
!
`
	eps := ParseDNSEndpoints(cfg)
	if len(eps) != 2 {
		t.Fatalf("разобрано %d строк из 2: %+v", len(eps), eps)
	}
	if eps[0].Host != "[2001:db8::1]" || eps[0].Port != 853 || eps[0].SNI != "dns.example.com" {
		t.Errorf("IPv6 без порта разобран как %+v", eps[0])
	}
	if eps[1].Host != "[2001:db8::2]" || eps[1].Port != 853 || eps[1].Zone != "ru" {
		t.Errorf("IPv6 с портом разобран как %+v", eps[1])
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
