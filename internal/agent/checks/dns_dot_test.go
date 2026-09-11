package checks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
	"golang.org/x/net/dns/dnsmessage"
)

// dotTestSNI is the only name the test certificate is issued for (plus the
// 127.0.0.1 IP SAN), so any other SNI must fail verification.
const dotTestSNI = "dns.example.com"

// dotReply selects what the fake DoT server answers to every query.
type dotReply int

const (
	dotAnswerA  dotReply = iota // rcode 0 + one A record
	dotNXDOMAIN                 // rcode NXDOMAIN, no answers
	dotNoAnswer                 // rcode 0, empty answer section
)

// dotServer is a local DNS-over-TLS resolver (RFC 7858: TLS + DNS-over-TCP
// with a 2-byte length prefix) behind a self-signed certificate generated
// per test. roots trusts exactly that certificate.
type dotServer struct {
	addr  string
	roots *x509.CertPool

	mu     sync.Mutex
	sni    []string // ServerName of each ClientHello
	qnames []string // question name of each query received
}

func (s *dotServer) seen() (sni, qnames []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sni...), append([]string(nil), s.qnames...)
}

func startDoTServer(t *testing.T, reply dotReply, answer [4]byte) *dotServer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: dotTestSNI},
		DNSNames:              []string{dotTestSNI},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	s := &dotServer{roots: x509.NewCertPool()}
	s.roots.AddCert(leaf)

	cfg := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			s.mu.Lock()
			s.sni = append(s.sni, hello.ServerName)
			s.mu.Unlock()
			return nil, nil
		},
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	s.addr = ln.Addr().String()
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn, reply, answer)
		}
	}()
	return s
}

func (s *dotServer) serve(conn net.Conn, reply dotReply, answer [4]byte) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	for {
		var lenBuf [2]byte
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return // EOF, or the client aborted the handshake
		}
		query := make([]byte, binary.BigEndian.Uint16(lenBuf[:]))
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}
		var msg dnsmessage.Message
		if err := msg.Unpack(query); err != nil || len(msg.Questions) == 0 {
			return
		}
		s.mu.Lock()
		s.qnames = append(s.qnames, msg.Questions[0].Name.String())
		s.mu.Unlock()

		resp := dnsmessage.Message{
			Header: dnsmessage.Header{
				ID:                 msg.Header.ID,
				Response:           true,
				RecursionDesired:   true,
				RecursionAvailable: true,
			},
			Questions: msg.Questions,
		}
		switch reply {
		case dotNXDOMAIN:
			resp.Header.RCode = dnsmessage.RCodeNameError
		case dotAnswerA:
			resp.Answers = []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{
					Name:  msg.Questions[0].Name,
					Type:  dnsmessage.TypeA,
					Class: dnsmessage.ClassINET,
					TTL:   60,
				},
				Body: &dnsmessage.AResource{A: answer},
			}}
		}
		pkt, err := resp.Pack()
		if err != nil || len(pkt) > 0xFFFF { // 2-byte length prefix: never wrap
			return
		}
		out := make([]byte, 2+len(pkt))
		binary.BigEndian.PutUint16(out, uint16(len(pkt)))
		copy(out[2:], pkt)
		if _, err := conn.Write(out); err != nil {
			return
		}
	}
}

func TestProbeDoT_Answers(t *testing.T) {
	srv := startDoTServer(t, dotAnswerA, [4]byte{203, 0, 113, 7})
	cfg := &tls.Config{RootCAs: srv.roots}

	got, err := ProbeDoT(context.Background(), srv.addr, dotTestSNI, "example.com", cfg, 2*time.Second)
	if err != nil {
		t.Fatalf("ProbeDoT: %v", err)
	}
	if len(got) != 1 || got[0] != "203.0.113.7" {
		t.Fatalf("answers = %v, want [203.0.113.7]", got)
	}
	sni, qnames := srv.seen()
	if len(sni) != 1 || sni[0] != dotTestSNI {
		t.Fatalf("server saw SNI %v, want [%s]", sni, dotTestSNI)
	}
	if len(qnames) != 1 || qnames[0] != "example.com." {
		t.Fatalf("server saw questions %v, want [example.com.]", qnames)
	}
	// The DNS check shares one *tls.Config across concurrent probes, so the
	// probe must never write the per-call SNI into the caller's config.
	if cfg.ServerName != "" {
		t.Fatalf("caller's tls.Config mutated: ServerName = %q", cfg.ServerName)
	}
}

func TestProbeDoT_NXDOMAIN(t *testing.T) {
	srv := startDoTServer(t, dotNXDOMAIN, [4]byte{})
	got, err := ProbeDoT(context.Background(), srv.addr, dotTestSNI, "example.com", &tls.Config{RootCAs: srv.roots}, 2*time.Second)
	if err == nil {
		t.Fatalf("expected error on NXDOMAIN, got %v", got)
	}
}

func TestProbeDoT_EmptyAnswer(t *testing.T) {
	srv := startDoTServer(t, dotNoAnswer, [4]byte{})
	got, err := ProbeDoT(context.Background(), srv.addr, dotTestSNI, "example.com", &tls.Config{RootCAs: srv.roots}, 2*time.Second)
	if err == nil {
		t.Fatalf("expected error on rcode 0 with no A records, got %v", got)
	}
}

func TestProbeDoT_WrongSNIFails(t *testing.T) {
	srv := startDoTServer(t, dotAnswerA, [4]byte{203, 0, 113, 7})
	got, err := ProbeDoT(context.Background(), srv.addr, "wrong.example.com", "example.com", &tls.Config{RootCAs: srv.roots}, 2*time.Second)
	if err == nil {
		t.Fatalf("a certificate for %s must not be accepted as wrong.example.com, got %v", dotTestSNI, got)
	}
	var hostErr x509.HostnameError
	if !errors.As(err, &hostErr) {
		t.Fatalf("want a hostname verification error, got %v", err)
	}
	if _, qnames := srv.seen(); len(qnames) != 0 {
		t.Fatalf("no query may be sent over an unverified connection, server saw %v", qnames)
	}
}

// TestDNSCheck_DoTEndpointProbedNotFailed: a DoT upstream discovered from
// running-config used to be scored as unreachable ("dot transport not
// implemented"), so a router on DoT upstreams raised a false `dns` failure.
// It must now be really probed — reachability AND the RKN probe, otherwise a
// live DoT upstream would just move from "unreachable" to "RKN suspect".
func TestDNSCheck_DoTEndpointProbedNotFailed(t *testing.T) {
	srv := startDoTServer(t, dotAnswerA, [4]byte{203, 0, 113, 7})
	host, port := splitHostPort(t, srv.addr)

	chk := DNS{
		// Same shape keenetic.ParseDNSEndpoints produces for `tls upstream <host>:<port>`.
		Endpoints:       []keenetic.DNSEndpoint{{Type: "dot", Host: host, Port: port}},
		TestDomain:      "example.com",
		FailThreshold:   1,
		PerProbeTimeout: 2 * time.Second,
		TLSConfig:       &tls.Config{RootCAs: srv.roots},
		RKNTestDomains:  []string{"example.com"},
	}
	got := chk.Run(context.Background(), Deps{})
	raw, _ := json.Marshal(got.Details)

	if got.Status != "ok" {
		t.Fatalf("a live DoT upstream must not fail the check, got status=%s details=%s", got.Status, raw)
	}
	if got.Details["failed_count"] != 0 {
		t.Fatalf("failed_count = %v, want 0; details=%s", got.Details["failed_count"], raw)
	}
	if got.Details["rkn_probed"] != 1 || got.Details["rkn_suspect"] != 0 {
		t.Fatalf("DoT endpoint must be RKN-probed and clean, details=%s", raw)
	}
	if s := string(raw); strings.Contains(s, "not implemented") || strings.Contains(s, "not supported") {
		t.Fatalf("DoT must be probed, not skipped as unsupported: %s", raw)
	}
	if _, qnames := srv.seen(); len(qnames) < 2 {
		t.Fatalf("want reachability + RKN queries over DoT, server saw %v", qnames)
	}
}

// TestDNSCheck_DoTEndpointDownCountsAsFailed: probing DoT for real must not
// turn a dead DoT upstream into a free pass either.
func TestDNSCheck_DoTEndpointDownCountsAsFailed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	host, port := splitHostPort(t, ln.Addr().String())
	ln.Close() // nothing listens there any more

	chk := DNS{
		Endpoints:       []keenetic.DNSEndpoint{{Type: "dot", Host: host, Port: port}},
		TestDomain:      "example.com",
		FailThreshold:   1,
		PerProbeTimeout: 500 * time.Millisecond,
	}
	got := chk.Run(context.Background(), Deps{})
	raw, _ := json.Marshal(got.Details)
	if got.Status != "fail" || got.Details["failed_count"] != 1 {
		t.Fatalf("dead DoT upstream must count as failed, got status=%s details=%s", got.Status, raw)
	}
	if strings.Contains(string(raw), "not implemented") {
		t.Fatalf("failure must come from a real probe, not the old stub: %s", raw)
	}
}
