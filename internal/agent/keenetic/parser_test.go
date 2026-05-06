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
