package awgmgr

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Форма ответа -- как у UI awg-manager 2.19.x: latency бывает null, когда
// связи нет; null обязан остаться «неизвестно», а не превратиться в 0 мс.
func TestClient_TestConnectivity_Disconnected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/test/connectivity" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("id"); got != "awg11" {
			t.Errorf("id: %q", got)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"connected":false,"latency":null,"reason":"timeout"}}`))
	}))
	defer srv.Close()

	got, err := New(srv.URL).TestConnectivity(context.Background(), "awg11")
	if err != nil {
		t.Fatal(err)
	}
	if got.Connected || got.Latency != nil || got.Reason != "timeout" {
		t.Fatalf("got %+v", got)
	}
}

func TestClient_TestConnectivity_ConnectedWithLatency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"connected":true,"latency":64}}`))
	}))
	defer srv.Close()

	got, err := New(srv.URL).TestConnectivity(context.Background(), "awg11")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Connected || got.Latency == nil || *got.Latency != 64 {
		t.Fatalf("got %+v", got)
	}
}

// Сборка без эндпоинта отвечает 404: это «не умеет», а не «связи нет» --
// вызывающий обязан отличить одно от другого.
func TestClient_TestConnectivity_MissingEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("404 page not found"))
	}))
	defer srv.Close()

	_, err := New(srv.URL).TestConnectivity(context.Background(), "awg11")
	if !errors.Is(err, ErrUnsupportedByRouter) {
		t.Fatalf("err = %v, want ErrUnsupportedByRouter", err)
	}
}

// Проверка мёртвого туннеля у awg-manager идёт дольше обычного запроса: обрезать
// её общим коротким таймаутом клиента значило бы вместо «Нет связи» показать
// «awg-manager не ответил». Срок задаёт только ctx вызывающего.
func TestClient_TestConnectivity_DeadlineIsCallersCtx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte(`{"success":true,"data":{"connected":false,"latency":null,"reason":"timeout"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.HTTP.Timeout = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := c.TestConnectivity(ctx, "awg11")
	if err != nil {
		t.Fatalf("короткий таймаут клиента оборвал проверку связи: %v", err)
	}
	if got.Connected {
		t.Fatalf("got %+v", got)
	}
}

func TestClient_TestConnectivity_SuccessFalseIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL).TestConnectivity(context.Background(), "awg11")
	if err == nil || errors.Is(err, ErrUnsupportedByRouter) {
		t.Fatalf("success=false -- сбой, а не «нет эндпоинта»: %v", err)
	}
}
