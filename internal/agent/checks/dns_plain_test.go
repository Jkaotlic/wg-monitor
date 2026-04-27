package checks

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// startMockUDPDNS returns a UDP server that replies with a single A-record
// answer for any A-query. Returns its host:port for use as Server in
// ProbePlainDNS, plus a stop function.
func startMockUDPDNS(t *testing.T, answerIP [4]byte) (string, func()) {
	t.Helper()
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	stop := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			select {
			case <-stop:
				return
			default:
			}
			conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, raddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			var msg dnsmessage.Message
			if err := msg.Unpack(buf[:n]); err != nil {
				continue
			}
			resp := dnsmessage.Message{
				Header: dnsmessage.Header{
					ID:            msg.Header.ID,
					Response:      true,
					Authoritative: true,
				},
				Questions: msg.Questions,
				Answers: []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{
						Name:  msg.Questions[0].Name,
						Type:  dnsmessage.TypeA,
						Class: dnsmessage.ClassINET,
						TTL:   60,
					},
					Body: &dnsmessage.AResource{A: answerIP},
				}},
			}
			out, _ := resp.Pack()
			_, _ = conn.WriteToUDP(out, raddr)
		}
	}()
	return conn.LocalAddr().String(), func() { close(stop); conn.Close() }
}

func TestProbePlainDNS_Answers(t *testing.T) {
	server, stop := startMockUDPDNS(t, [4]byte{93, 184, 216, 34})
	defer stop()

	got, err := ProbePlainDNS(context.Background(), server, "example.com.", nil, 1*time.Second)
	if err != nil {
		t.Fatalf("ProbePlainDNS: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("no answers")
	}
}

func TestProbePlainDNS_Timeout(t *testing.T) {
	// Connect to a port that has no listener — deadline expires.
	_, err := ProbePlainDNS(context.Background(), "127.0.0.1:1", "example.com.", nil, 200*time.Millisecond)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
}
