package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestAwgMarkerSucceedsOnFirstTry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	chk := AwgMarker{URL: srv.URL, MaxRetries: 3, BaseBackoff: time.Millisecond}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "ok" || calls.Load() != 1 {
		t.Fatalf("status=%s calls=%d details=%v", got.Status, calls.Load(), got.Details)
	}
}

func TestAwgMarkerRecoversAfterTwoFails(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(502)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	chk := AwgMarker{URL: srv.URL, MaxRetries: 3, BaseBackoff: time.Millisecond}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "ok" || calls.Load() != 3 {
		t.Fatalf("status=%s calls=%d", got.Status, calls.Load())
	}
	if got.Details["attempts"].(int) != 3 {
		t.Fatalf("attempts=%v", got.Details["attempts"])
	}
}

func TestAwgMarkerExhausts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()
	chk := AwgMarker{URL: srv.URL, MaxRetries: 3, BaseBackoff: time.Millisecond}
	got := chk.Run(context.Background(), Deps{HTTPClient: srv.Client()})
	if got.Status != "fail" || calls.Load() != 3 {
		t.Fatalf("status=%s calls=%d", got.Status, calls.Load())
	}
}
