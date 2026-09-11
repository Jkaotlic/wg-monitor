package dnswatch

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
)

// PlainResolver answers the agent's own lookups of resolver host names. The
// router's resolver may be exactly what is dead, so the watchdog asks Yandex
// plain DNS directly instead of the system resolver.
const PlainResolver = "77.88.8.8:53"

// probeTimeout bounds one lookup and one DoH/DoT probe.
const probeTimeout = 5 * time.Second

// netProber runs the real probes: the own DoH endpoint and fallback
// candidates. Host names are resolved by the agent itself (bootstrap IP or
// plain DNS to PlainResolver) and dialled by address, with the real host name
// kept for SNI and the HTTP Host. IP literals are used as-is.
type netProber struct {
	resolver  string        // "" → PlainResolver
	timeout   time.Duration // 0 → probeTimeout
	tlsConfig *tls.Config   // nil → system roots; tests pass RootCAs
	lookupFn  func(ctx context.Context, host string) (string, error)
	// systemLookupFn is the last resort after plain DNS and the last good
	// address: the system resolver (nil → net.DefaultResolver).
	systemLookupFn func(ctx context.Context, host string) (string, error)
	dotFn          func(ctx context.Context, addr, sni, domain string, cfg *tls.Config, timeout time.Duration) ([]string, error)

	mu       sync.Mutex
	lastGood map[string]string // host → last address that resolved
}

func newNetProber() *netProber { return &netProber{} }

// ProbeOwn probes the own DoH endpoint for domain. bootstrapIP, when set, is
// the endpoint host's address (no lookup at all). The returned error never
// contains the endpoint path — it is a credential.
func (p *netProber) ProbeOwn(ctx context.Context, endpoint, domain, bootstrapIP string) error {
	if err := p.probeDoH(ctx, endpoint, domain, bootstrapIP); err != nil {
		return errors.New(maskSecret(err.Error(), endpoint))
	}
	return nil
}

// ProbeCandidate probes one dns-proxy candidate line for domain:
// `https upstream <URL>` over DoH, `tls upstream <host>[:port] [sni <name>]`
// over DoT (sni defaults to the host). Qualifiers such as `domain` are
// ignored. A line that is neither is an error — never probed in a guessed form.
func (p *netProber) ProbeCandidate(ctx context.Context, line, domain string) error {
	c, err := parseCandidate(line)
	if err != nil {
		return err
	}
	if c.url != "" {
		return p.probeDoH(ctx, c.url, domain, "")
	}
	ip, err := p.resolve(ctx, c.host, "")
	if err != nil {
		return err
	}
	dot := p.dotFn
	if dot == nil {
		dot = checks.ProbeDoT
	}
	_, err = dot(ctx, net.JoinHostPort(ip, c.port), c.sni, domain, p.tlsConfig, p.timeoutOr())
	return err
}

type candidate struct {
	url             string // https upstream: the DoH URL
	host, port, sni string // tls upstream
}

func parseCandidate(line string) (candidate, error) {
	f := strings.Fields(line)
	if len(f) < 3 || f[1] != "upstream" {
		return candidate{}, fmt.Errorf("not a dns-proxy upstream line: %q", line)
	}
	switch f[0] {
	case "https":
		u, err := url.Parse(f[2])
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			return candidate{}, fmt.Errorf("not an https:// DoH URL: %q", f[2])
		}
		return candidate{url: f[2]}, nil
	case "tls":
		host, port := f[2], "853"
		if h, pt, err := net.SplitHostPort(f[2]); err == nil {
			host, port = h, pt
		}
		sni := host
		for i := 3; i+1 < len(f); i++ {
			if f[i] == "sni" {
				sni = f[i+1]
				break
			}
		}
		return candidate{host: host, port: port, sni: sni}, nil
	}
	return candidate{}, fmt.Errorf("not a tls/https upstream line: %q", line)
}

// probeDoH runs checks.ProbeDoH against rawURL with the connection dialled at
// the resolved address; http.Transport keeps the URL host for SNI and Host.
func (p *netProber) probeDoH(ctx context.Context, rawURL, domain, pinnedIP string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return errors.New("not an https:// DoH URL")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	ip, err := p.resolve(ctx, u.Hostname(), pinnedIP)
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(ip, port)
	var tlsCfg *tls.Config
	if p.tlsConfig != nil {
		tlsCfg = p.tlsConfig.Clone()
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		TLSClientConfig:     tlsCfg,
		TLSHandshakeTimeout: p.timeoutOr(),
		DisableKeepAlives:   true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: p.timeoutOr()}
	_, err = checks.ProbeDoH(ctx, rawURL, domain, client, p.timeoutOr())
	return err
}

// resolve returns the address to dial for host: an IP literal as-is, else
// pinned (bootstrap IP), else a lookup through the plain resolver. A failed
// lookup falls back to the last address that resolved, then to the system
// resolver, so a plain resolver that is unreachable does not read as "the
// resolver behind it is dead" — a false positive that would take a working
// own resolver out. The system resolver is the router's dns-proxy: it answers
// through the own resolver while that is alive and fails with it when it is
// dead, so the verdict stays right.
func (p *netProber) resolve(ctx context.Context, host, pinned string) (string, error) {
	if net.ParseIP(host) != nil {
		return host, nil
	}
	if pinned != "" {
		return pinned, nil
	}
	lookup := p.lookupFn
	if lookup == nil {
		lookup = p.plainLookup
	}
	ip, err := lookup(ctx, host)
	p.mu.Lock()
	if err == nil && ip != "" {
		if p.lastGood == nil {
			p.lastGood = make(map[string]string)
		}
		p.lastGood[host] = ip
		p.mu.Unlock()
		return ip, nil
	}
	last, ok := p.lastGood[host]
	p.mu.Unlock()
	if ok {
		return last, nil
	}
	system := p.systemLookupFn
	if system == nil {
		system = p.systemLookup
	}
	if sip, serr := system(ctx, host); serr == nil && sip != "" {
		return sip, nil
	}
	if err == nil {
		err = errors.New("no address")
	}
	return "", fmt.Errorf("resolve %s via %s: %w", host, p.resolverOr(), err)
}

func (p *netProber) systemLookup(ctx context.Context, host string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, p.timeoutOr())
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(cctx, host)
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		if v4 := a.IP.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	return "", errors.New("no A record")
}

func (p *netProber) plainLookup(ctx context.Context, host string) (string, error) {
	ips, err := checks.ProbePlainDNS(ctx, p.resolverOr(), strings.TrimSuffix(host, ".")+".", nil, p.timeoutOr())
	if err != nil {
		return "", err
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	return "", errors.New("no A record")
}

func (p *netProber) timeoutOr() time.Duration {
	if p.timeout > 0 {
		return p.timeout
	}
	return probeTimeout
}

func (p *netProber) resolverOr() string {
	if p.resolver != "" {
		return p.resolver
	}
	return PlainResolver
}

// maskEndpoint shows the endpoint without its secret path: https://host/***.
func maskEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "***"
	}
	return u.Scheme + "://" + u.Host + "/***"
}

// maskSecret removes the endpoint's secret path from s.
func maskSecret(s, endpoint string) string {
	if endpoint == "" {
		return s
	}
	s = strings.ReplaceAll(s, endpoint, maskEndpoint(endpoint))
	if u, err := url.Parse(endpoint); err == nil && len(u.Path) > 1 {
		s = strings.ReplaceAll(s, u.Path, "/***")
	}
	return s
}
