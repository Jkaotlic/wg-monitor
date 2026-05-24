package amnezia

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientLoginAndAccountInfo(t *testing.T) {
	var sawCookie bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			if r.Method != http.MethodPost {
				t.Errorf("login method = %s", r.Method)
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "ok"})
			_, _ = w.Write([]byte(`{"message":"ok"}`))
		case "/api/account-info":
			if ck, err := r.Cookie("session"); err == nil && ck.Value == "ok" {
				sawCookie = true
			}
			_, _ = w.Write([]byte(`{"data":{"subscription_status":"active","active_device_count":1,"max_device_count":7,"available_countries":[{"server_country_code":"de","server_country_name":"Germany"}],"issued_configs":[]}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	if err := c.Login(context.Background(), "vpn://abc"); err != nil {
		t.Fatal(err)
	}
	info, err := c.AccountInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !sawCookie {
		t.Fatal("account-info request did not carry login cookie")
	}
	if info.ActiveDeviceCount != 1 || info.MaxDeviceCount != 7 {
		t.Fatalf("counts = %d/%d", info.ActiveDeviceCount, info.MaxDeviceCount)
	}
	if got := info.AvailableCountries[0].Code; got != "de" {
		t.Fatalf("country code = %q", got)
	}
}

func TestDownloadAndRevokePayloads(t *testing.T) {
	var gotDownloadBody, gotRevokeBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			_, _ = w.Write([]byte(`{"message":"ok"}`))
		case "/api/download-config":
			buf, _ := io.ReadAll(r.Body)
			gotDownloadBody = string(buf)
			_, _ = w.Write([]byte("[Interface]\nPrivateKey = x\n"))
		case "/api/revoke-country-config":
			buf, _ := io.ReadAll(r.Body)
			gotRevokeBody = string(buf)
			_, _ = w.Write([]byte(`{"message":"ok"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	data, err := c.DownloadConfig(context.Background(), "DE")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "PrivateKey") {
		t.Fatalf("download body = %q", string(data))
	}
	if gotDownloadBody != `{"countryCode":"de"}` {
		t.Fatalf("download payload = %q", gotDownloadBody)
	}
	if err := c.RevokeCountryConfig(context.Background(), "DE"); err != nil {
		t.Fatal(err)
	}
	if gotRevokeBody != `{"countryCode":"de"}` {
		t.Fatalf("revoke payload = %q", gotRevokeBody)
	}
}

func TestResolveMirrorTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<meta name="mirror-to" data-link="https://m-example.run.app/">`))
	}))
	defer srv.Close()

	got, err := ResolveMirrorTarget(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://m-example.run.app" {
		t.Fatalf("target = %q", got)
	}
}
