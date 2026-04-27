package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const cdnCgiTraceMatch = `fl=1179f36
h=1.1.1.1
ip=198.51.100.21
ts=1777301814.000
visit_scheme=https
uag=curl/8.15.0
colo=AMS
http=http/2
tls=TLSv1.3
`

const cdnCgiTraceMismatch = `fl=1179f36
h=1.1.1.1
ip=1.2.3.4
ts=1777301814.000
`

const cdnCgiTraceMissingIP = `fl=1179f36
h=1.1.1.1
ts=1777301814.000
`

func TestAwgRoutingMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(cdnCgiTraceMatch))
	}))
	defer srv.Close()

	chk := AwgRouting{Iface: "ignored", URL: srv.URL, Expected: "198.51.100.21"}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "ok" {
		t.Fatalf("got %+v", got)
	}
	if got.Details["got_ip"] != "198.51.100.21" {
		t.Fatalf("details: %+v", got.Details)
	}
}

func TestAwgRoutingMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(cdnCgiTraceMismatch))
	}))
	defer srv.Close()

	chk := AwgRouting{URL: srv.URL, Expected: "198.51.100.21"}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "fail" || got.Details["got_ip"] != "1.2.3.4" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestAwgRoutingMissingIPLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(cdnCgiTraceMissingIP))
	}))
	defer srv.Close()

	chk := AwgRouting{URL: srv.URL, Expected: "198.51.100.21"}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "fail" {
		t.Fatalf("expected fail on missing ip= line, got %+v", got)
	}
}

func TestAwgRoutingHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	chk := AwgRouting{URL: srv.URL, Expected: "198.51.100.21"}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "fail" {
		t.Fatalf("expected fail on 502, got %+v", got)
	}
}
