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
