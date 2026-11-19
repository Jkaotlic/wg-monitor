package premiumfiles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCabinetSessionAndExplicitDownload(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "" {
			t.Error("missing same-origin request header")
		}
		switch r.URL.Path {
		case "/api/login":
			var b struct {
				VPNKey   string `json:"vpnKey"`
				Remember bool   `json:"remember"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			if b.VPNKey != "vpn://synthetic" || b.Remember {
				t.Errorf("unexpected login")
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "test", Path: "/"})
			w.Write([]byte(`{}`))
		case "/api/account-info":
			if _, e := r.Cookie("session"); e != nil {
				t.Error("session lost")
			}
			w.Write([]byte(`{"data":{"available_countries":[{"server_country_code":"nl","server_country_name":"Нидерланды"}],"max_device_count":5,"active_device_count":1}}`))
		case "/api/download-config":
			calls++
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			if b["countryCode"] != "nl" || b["declaredCountryCode"] != "ru" {
				t.Errorf("download arguments: %v", b)
			}
			w.Write([]byte("# original header\n[Interface]\nPrivateKey = synthetic\n[Peer]\nPublicKey = synthetic\n"))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := New(srv.URL)
	if e := c.Login(context.Background(), "vpn://synthetic"); e != nil {
		t.Fatal(e)
	}
	a, e := c.Account(context.Background())
	if e != nil || len(a.Countries) != 1 {
		t.Fatalf("account %v %v", a, e)
	}
	if calls != 0 {
		t.Fatal("reading account generated config")
	}
	b, e := c.Download(context.Background(), "nl", "ru")
	if e != nil || !strings.HasPrefix(string(b), "# original header") {
		t.Fatalf("download %v", e)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestErrorContainsNoCabinetBodyAndDoesNotRetry(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(409)
		w.Write([]byte("vpn://private-do-not-leak"))
	}))
	defer srv.Close()
	_, e := New(srv.URL).Download(context.Background(), "nl", "ru")
	if !errors.Is(e, ErrSlotsFull) || strings.Contains(e.Error(), "private") || n != 1 {
		t.Fatalf("calls=%d error=%v", n, e)
	}
}

func TestLoginDoesNotFollowRedirectWithSecret(t *testing.T) {
	reached := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	if err := New(source.URL).Login(context.Background(), "vpn://synthetic"); err == nil || reached {
		t.Fatalf("redirect followed=%t err=%v", reached, err)
	}
}
