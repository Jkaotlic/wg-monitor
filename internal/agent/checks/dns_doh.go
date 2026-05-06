package checks

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// ProbeDoH issues an A-query for `domain` against the DoH endpoint at `url`
// using RFC 8484 binary `application/dns-message` over HTTP GET (?dns=<base64url>).
// Returns the answer A-record IPs as strings. Errors on non-2xx, non-success
// rcode, parse failure, or zero A answers.
//
// RFC 8484 chosen over the older Cloudflare/Google JSON syntax because it is
// the canonical DoH protocol and is the only one supported by self-hosted
// resolvers like AdGuard Home, Pi-hole, and dnscrypt-proxy.
func ProbeDoH(ctx context.Context, url, domain string, client *http.Client, timeout time.Duration) ([]string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name, err := dnsmessage.NewName(dnsFQDN(domain))
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
	encoded := base64.RawURLEncoding.EncodeToString(pkt)

	full := url
	if strings.Contains(url, "?") {
		full += "&dns=" + encoded
	} else {
		full += "?dns=" + encoded
	}
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, fmt.Errorf("doh request: %w", err)
	}
	req.Header.Set("Accept", "application/dns-message")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("doh: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("doh: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("doh: read body: %w", err)
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(body); err != nil {
		return nil, fmt.Errorf("doh: unpack: %w", err)
	}
	if msg.Header.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("doh: rcode %v", msg.Header.RCode)
	}
	var out []string
	for _, rr := range msg.Answers {
		if a, ok := rr.Body.(*dnsmessage.AResource); ok {
			out = append(out, net.IP(a.A[:]).String())
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("doh: no A answers")
	}
	return out, nil
}

// dnsFQDN normalizes a domain to a fully-qualified form ("example.com.")
// expected by dnsmessage.NewName.
func dnsFQDN(d string) string {
	if strings.HasSuffix(d, ".") {
		return d
	}
	return d + "."
}
