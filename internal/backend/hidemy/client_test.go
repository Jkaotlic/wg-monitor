package hidemy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientServerListFiltersWireGuardServers(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasWG := r.URL.Query()["wg"]
		if r.URL.Path != "/api/serverlist.php" || r.URL.Query().Get("out") != "js" || !hasWG {
			t.Fatalf("unexpected endpoint %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		buf, _ := io.ReadAll(r.Body)
		gotBody = string(buf)
		_, _ = w.Write([]byte(`[
			{"name_en":"Germany, Frankfurt","services":{"wg":{"ip":"198.51.100.10"}}},
			{"name_en":"No WireGuard","services":{}},
			{"name":"Netherlands","services":{"wg":{"ip":"198.51.100.20"}}}
		]`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	servers, err := c.ServerList(context.Background(), "123456789012345")
	if err != nil {
		t.Fatal(err)
	}
	if gotBody != "code=123456789012345" {
		t.Fatalf("body = %q", gotBody)
	}
	if len(servers) != 2 {
		t.Fatalf("servers = %+v", servers)
	}
	if servers[0].Name != "Germany, Frankfurt" || servers[0].IP != "198.51.100.10" {
		t.Fatalf("server[0] = %+v", servers[0])
	}
	if servers[1].Name != "Netherlands" || servers[1].IP != "198.51.100.20" {
		t.Fatalf("server[1] = %+v", servers[1])
	}
}

func TestClientDownloadAmneziaWG20Config(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/vpn_get_config_wg.php" {
			t.Fatalf("unexpected endpoint %s", r.URL.Path)
		}
		buf, _ := io.ReadAll(r.Body)
		gotBody = string(buf)
		_, _ = w.Write([]byte("[Interface]\nPrivateKey = x\n[Peer]\nPublicKey = y\nEndpoint = 198.51.100.10:51820\n"))
	}))
	defer srv.Close()

	c := New(srv.URL)
	conf, err := c.DownloadAmneziaWG20Config(context.Background(), "123456789012345", "198.51.100.10")
	if err != nil {
		t.Fatal(err)
	}
	if gotBody != "awg=4&code=123456789012345&server=198.51.100.10" {
		t.Fatalf("body = %q", gotBody)
	}
	if !strings.Contains(string(conf), "[Interface]") {
		t.Fatalf("conf = %q", string(conf))
	}
}

func TestClientRejectsBadAccessCodeShape(t *testing.T) {
	c := New("https://example.invalid")
	if _, err := c.ServerList(context.Background(), "abc"); err == nil {
		t.Fatal("expected bad code error")
	}
}
