package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const dohJSONOK = `{"Status":0,"TC":false,"RD":true,"RA":true,"AD":true,"CD":false,
"Question":[{"name":"example.com","type":1}],
"Answer":[{"name":"example.com","type":1,"TTL":83,"data":"93.184.216.34"}]}`

const dohJSONNoAnswer = `{"Status":0,"TC":false,"RD":true,"RA":true,"Question":[{"name":"example.com","type":1}]}`

const dohJSONNXDOMAIN = `{"Status":3,"TC":false,"RD":true,"RA":true,"Question":[{"name":"foo","type":1}]}`

func TestProbeDoH_Answers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "example.com" {
			http.Error(w, "missing name", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/dns-json")
		_, _ = w.Write([]byte(dohJSONOK))
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(dohJSONNoAnswer))
	}))
	defer srv.Close()
	got, err := ProbeDoH(context.Background(), srv.URL, "example.com", srv.Client(), 1*time.Second)
	if err == nil {
		t.Fatalf("expected error on empty answer, got %v", got)
	}
}

func TestProbeDoH_NXDOMAIN(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(dohJSONNXDOMAIN))
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
