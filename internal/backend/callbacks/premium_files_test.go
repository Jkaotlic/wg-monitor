package callbacks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestPremiumFileSessionKeepsKeyAndDoesNotGenerate(t *testing.T) {
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			w.Write([]byte(`{}`))
		case "/api/account-info":
			w.Write([]byte(`{"data":{"available_countries":[{"server_country_code":"nl","server_country_name":"Нидерланды"}],"max_device_count":5}}`))
		case "/api/download-config":
			downloads.Add(1)
			w.Write([]byte("# vpn://provider-header\n[Interface]\nPrivateKey=synthetic\n[Peer]\n"))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	r := &Router{cfg: Config{AmneziaSecretsPath: filepath.Join(t.TempDir(), "premium.json"), AmneziaFilesBaseURL: server.URL}}
	account, id, err := r.OpenPremiumFiles(context.Background(), 7, "vpn://synthetic", "")
	if err != nil || id == "" || len(account.Countries) != 1 || downloads.Load() != 0 {
		t.Fatalf("open %v %d", err, downloads.Load())
	}
	if key, err := r.getAmneziaKeyByID(7, id); err != nil || key != "vpn://synthetic" {
		t.Fatal("key not saved")
	}
	if _, err := r.DownloadPremiumFile(context.Background(), 7, id, "de", "ru"); err == nil || downloads.Load() != 0 {
		t.Fatal("download of absent country")
	}
	if b, err := r.DownloadPremiumFile(context.Background(), 7, id, "nl", "ru"); err != nil || len(b) == 0 || downloads.Load() != 1 {
		t.Fatalf("download %v", err)
	}
	if _, err := r.DownloadPremiumFile(context.Background(), 8, id, "nl", "ru"); err == nil || downloads.Load() != 1 {
		t.Fatal("another router accessed the key")
	}
}
