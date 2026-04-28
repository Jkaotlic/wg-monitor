package checks

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// buildDoHResponse packs a minimal DNS response message with the given rcode and
// optional A-record answer for `qname`. Caller passes the original query so
// the response echoes the request id and questions.
func buildDoHResponse(t *testing.T, query []byte, rcode dnsmessage.RCode, answerIP [4]byte, withAnswer bool) []byte {
	t.Helper()
	var qmsg dnsmessage.Message
	if err := qmsg.Unpack(query); err != nil {
		t.Fatalf("unpack query: %v", err)
	}
	resp := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:               qmsg.Header.ID,
			Response:         true,
			RecursionDesired: true,
			RCode:            rcode,
		},
		Questions: qmsg.Questions,
	}
	if withAnswer && len(qmsg.Questions) > 0 {
		resp.Answers = []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{
				Name:  qmsg.Questions[0].Name,
				Type:  dnsmessage.TypeA,
				Class: dnsmessage.ClassINET,
				TTL:   60,
			},
			Body: &dnsmessage.AResource{A: answerIP},
		}}
	}
	pkt, err := resp.Pack()
	if err != nil {
		t.Fatalf("pack response: %v", err)
	}
	return pkt
}

func TestProbeDoH_Answers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/dns-message" {
			http.Error(w, "wrong accept: "+got, http.StatusBadRequest)
			return
		}
		raw := r.URL.Query().Get("dns")
		if raw == "" {
			http.Error(w, "missing dns= param", http.StatusBadRequest)
			return
		}
		query, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			http.Error(w, "bad base64url: "+err.Error(), http.StatusBadRequest)
			return
		}
		var qmsg dnsmessage.Message
		if err := qmsg.Unpack(query); err != nil {
			http.Error(w, "bad dns query: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(qmsg.Questions) == 0 || qmsg.Questions[0].Name.String() != "example.com." {
			http.Error(w, "wrong qname", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(buildDoHResponse(t, query, dnsmessage.RCodeSuccess, [4]byte{93, 184, 216, 34}, true))
	}))
	defer srv.Close()

	got, err := ProbeDoH(context.Background(), srv.URL, "example.com", srv.Client(), 1*time.Second)
	if err != nil {
		t.Fatalf("ProbeDoH: %v", err)
	}
	if len(got) != 1 || got[0] != "93.184.216.34" {
		t.Fatalf("answers: %+v", got)
	}
}

func TestProbeDoH_EmptyAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("dns")
		query, _ := base64.RawURLEncoding.DecodeString(raw)
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(buildDoHResponse(t, query, dnsmessage.RCodeSuccess, [4]byte{}, false))
	}))
	defer srv.Close()
	got, err := ProbeDoH(context.Background(), srv.URL, "example.com", srv.Client(), 1*time.Second)
	if err == nil {
		t.Fatalf("expected error on empty answer, got %v", got)
	}
}

func TestProbeDoH_NXDOMAIN(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("dns")
		query, _ := base64.RawURLEncoding.DecodeString(raw)
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(buildDoHResponse(t, query, dnsmessage.RCodeNameError, [4]byte{}, false))
	}))
	defer srv.Close()
	_, err := ProbeDoH(context.Background(), srv.URL, "foo", srv.Client(), 1*time.Second)
	if err == nil {
		t.Fatalf("expected error on NXDOMAIN")
	}
}

func TestProbeDoH_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	_, err := ProbeDoH(context.Background(), srv.URL, "x", srv.Client(), 1*time.Second)
	if err == nil {
		t.Fatalf("expected error on 502")
	}
}
