package dnswatch

// Fallback defaults mirror the operator's own AdGuard Home `upstream_dns`
// (read live 11.09.2026): Russian zones go to Yandex, everything else goes to
// a foreign pool raced in parallel, and a few CDN zones are pinned to
// Cloudflare. Each entry is a dns-proxy line without the `dns-proxy` prefix,
// exactly as it is added with `ndmc -c "dns-proxy <line>"`.
//
// Google is deliberately absent: it steers CDNs to US edges.
//
// agent.LoadConfig copies these into the agent config when the watchdog is
// enabled and the list is not set; never modify them in place.
var (
	DefaultRUZones = []string{"ru", "su", "xn--p1ai", "xn--80adxhks", "xn--d1acj3b", "xn--p1acf", "tatar"}

	DefaultRUCandidates = []string{
		"tls upstream common.dot.dns.yandex.net",
		"https upstream https://common.dot.dns.yandex.net/dns-query",
	}

	DefaultForeignCandidates = []string{
		"https upstream https://dns.quad9.net/dns-query",
		"https upstream https://freedns.controld.com/p0",
		"https upstream https://cloudflare-dns.com/dns-query",
		"tls upstream 1.1.1.1 sni cloudflare-dns.com",
		"tls upstream 9.9.9.9 sni dns.quad9.net",
	}

	DefaultPinnedZones = []string{"themoviedb.org", "tmdb.org", "b-cdn.net", "phncdn.com", "pornhub.com", "rncdn7.com"}
)

// DefaultPinnedCandidate carries DefaultPinnedZones while it is live.
const DefaultPinnedCandidate = "https upstream https://cloudflare-dns.com/dns-query"
