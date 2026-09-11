package checks

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
	"golang.org/x/net/dns/dnsmessage"
)

// ProbeDoT issues an A-query for `domain` over DNS-over-TLS (RFC 7858) to
// addr ("ip:853") and returns the answer A-record IPs as strings.
//
// The server certificate is verified against sni (empty sni → the host part
// of addr, as crypto/tls does by default). cfg supplies everything else —
// typically nil (system roots); tests pass RootCAs for a self-signed server.
// cfg is cloned, never modified, so one config can be shared by concurrent
// probes.
//
// Wire format: TLS, then DNS-over-TCP — each message prefixed by its 2-byte
// big-endian length. Success = response id matches, rcode 0 and at least one
// A record; anything else is an error, so an empty NOERROR reads as "the
// resolver cannot resolve", same as ProbeDoH.
func ProbeDoT(ctx context.Context, addr, sni, domain string, cfg *tls.Config, timeout time.Duration) ([]string, error) {
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

	tc := &tls.Config{}
	if cfg != nil {
		tc = cfg.Clone()
	}
	tc.ServerName = sni
	conn, err := (&tls.Dialer{Config: tc}).DialContext(cctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dot %s: %w", addr, err)
	}
	defer conn.Close()
	if deadline, ok := cctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	frame := make([]byte, 2+len(pkt))
	binary.BigEndian.PutUint16(frame, uint16(len(pkt)))
	copy(frame[2:], pkt)
	if _, err := conn.Write(frame); err != nil {
		return nil, fmt.Errorf("dot: write: %w", err)
	}

	var lenBuf [2]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("dot: read length: %w", err)
	}
	body := make([]byte, binary.BigEndian.Uint16(lenBuf[:]))
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, fmt.Errorf("dot: read reply: %w", err)
	}

	var msg dnsmessage.Message
	if err := msg.Unpack(body); err != nil {
		return nil, fmt.Errorf("dot: unpack: %w", err)
	}
	if msg.Header.ID != id {
		return nil, fmt.Errorf("dot: response id mismatch: %d != %d", msg.Header.ID, id)
	}
	if msg.Header.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("dot: rcode %v", msg.Header.RCode)
	}
	var out []string
	for _, rr := range msg.Answers {
		if a, ok := rr.Body.(*dnsmessage.AResource); ok {
			out = append(out, net.IP(a.A[:]).String())
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("dot: no A answers")
	}
	return out, nil
}

// dotTarget maps a DoT endpoint from keenetic.ParseDNSEndpoints to ProbeDoT's
// addr and SNI. DNSEndpoint carries no SNI (the parser keeps only host:port),
// so the host itself is the name the certificate is checked against; for an
// IP host crypto/tls verifies the certificate's IP SANs.
func dotTarget(ep keenetic.DNSEndpoint) (addr, sni string) {
	port := ep.Port
	if port == 0 {
		port = 853
	}
	return net.JoinHostPort(ep.Host, strconv.Itoa(port)), ep.Host
}
