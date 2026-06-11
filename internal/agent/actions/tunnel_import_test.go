package actions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

const awgConf = `
[Interface]
PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
Address = 10.8.1.2/32
Jc = 4
Jmin = 40
Jmax = 70
S1 = 0
S2 = 0
H1 = 1111111111
H2 = 2222222222
H3 = 3333333333
H4 = 4000000000
DNS = 1.1.1.1

[Peer]
PublicKey = BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=
PresharedKey = CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
`

func TestParseWGConf_Happy(t *testing.T) {
	req, err := ParseWGConf(awgConf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if req.Interface.PrivateKey != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" {
		t.Errorf("PrivateKey: %q", req.Interface.PrivateKey)
	}
	if req.Interface.Jc != 4 {
		t.Errorf("Jc: %d", req.Interface.Jc)
	}
	if req.Interface.Jmin != 40 {
		t.Errorf("Jmin: %d", req.Interface.Jmin)
	}
	if req.Interface.Jmax != 70 {
		t.Errorf("Jmax: %d", req.Interface.Jmax)
	}
	if req.Interface.H1 != "1111111111" {
		t.Errorf("H1: %q", req.Interface.H1)
	}
	if req.Interface.H4 != "4000000000" {
		t.Errorf("H4: %q", req.Interface.H4)
	}
	if req.Peer.PublicKey != "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=" {
		t.Errorf("PublicKey: %q", req.Peer.PublicKey)
	}
	if req.Peer.PresharedKey != "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=" {
		t.Errorf("PresharedKey: %q", req.Peer.PresharedKey)
	}
	if req.Peer.Endpoint != "vpn.example.com:51820" {
		t.Errorf("Endpoint: %q", req.Peer.Endpoint)
	}
	if len(req.Peer.AllowedIPs) != 2 {
		t.Errorf("AllowedIPs len: %d, want 2", len(req.Peer.AllowedIPs))
	}
	if req.Peer.AllowedIPs[0] != "0.0.0.0/0" {
		t.Errorf("AllowedIPs[0]: %q", req.Peer.AllowedIPs[0])
	}
	if req.Type != "amnezia_wg" {
		t.Errorf("Type: %q", req.Type)
	}
	if !req.Enabled {
		t.Error("Enabled must be true")
	}
}

func TestParseWGConf_MissingPrivateKey(t *testing.T) {
	conf := strings.ReplaceAll(awgConf, "PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n", "")
	_, err := ParseWGConf(conf)
	if err == nil || !strings.Contains(err.Error(), "PrivateKey") {
		t.Errorf("expected PrivateKey error, got %v", err)
	}
}

func TestParseWGConf_MissingEndpoint(t *testing.T) {
	conf := strings.ReplaceAll(awgConf, "Endpoint = vpn.example.com:51820\n", "")
	_, err := ParseWGConf(conf)
	if err == nil || !strings.Contains(err.Error(), "Endpoint") {
		t.Errorf("expected Endpoint error, got %v", err)
	}
}

func TestParseWGConf_NoPresharedKey(t *testing.T) {
	conf := strings.ReplaceAll(awgConf, "PresharedKey = CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=\n", "")
	req, err := ParseWGConf(conf)
	if err != nil {
		t.Fatal(err)
	}
	if req.Peer.PresharedKey != "" {
		t.Errorf("PresharedKey must be empty, got %q", req.Peer.PresharedKey)
	}
}

func TestSanitizeTunnelName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"awg11", "awg11"},
		{"VPN Config", "vpn-config"},
		{"my_tunnel-1", "my_tunnel-1"},
		{"UPPER", "upper"},
		{"bad!chars@here", "bad-chars-here"},
	}
	for _, tc := range tests {
		got := sanitizeTunnelName(tc.in)
		if got != tc.want {
			t.Errorf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsValidTunnelName(t *testing.T) {
	valid := []string{"awg11", "vpn-1", "my_tunnel", "ab"}
	for _, v := range valid {
		if !isValidTunnelName(v) {
			t.Errorf("%q should be valid", v)
		}
	}
	invalid := []string{"", "A", "1start", "has space", strings.Repeat("x", 33)}
	for _, v := range invalid {
		if isValidTunnelName(v) {
			t.Errorf("%q should be invalid", v)
		}
	}
}

func TestImportVerifyDelaysUseShortBudget(t *testing.T) {
	want := []time.Duration{time.Second, 3 * time.Second, 6 * time.Second}
	if len(importVerifyDelays) != len(want) {
		t.Fatalf("delay count = %d, want %d: %v", len(importVerifyDelays), len(want), importVerifyDelays)
	}
	var total time.Duration
	for i := range want {
		if importVerifyDelays[i] != want[i] {
			t.Fatalf("delay[%d] = %s, want %s", i, importVerifyDelays[i], want[i])
		}
		total += importVerifyDelays[i]
	}
	if total > 10*time.Second {
		t.Fatalf("total verify budget = %s, want <= 10s", total)
	}
}

func TestImportTunnelAddsLiveTunnelToHydraRoutePolicy(t *testing.T) {
	var updated awgmgr.DNSRoute
	updateCalled := false
	tunnelsCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		tunnelsCalls++
		if tunnelsCalls == 1 {
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"tun-new","name":"selfhosted-home","interfaceName":"nwg7","ndmsName":"Wireguard7","enabled":true,"status":"running","backend":"nativewg"}
		]}}`))
	})
	mux.HandleFunc("/api/import/conf", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"tun-new","name":"selfhosted-home","interfaceName":"nwg7","enabled":false}}`))
	})
	mux.HandleFunc("/api/control/start", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "tun-new" {
			t.Fatalf("start id=%q, want tun-new", r.URL.Query().Get("id"))
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:Video","name":"Video","backend":"hydraroute","enabled":true,"routes":null,"hrRouteMode":"policy","hrPolicyName":"HydraRoute","hrPolicyInterfaces":["nwg1"]}
		]}`))
	})
	mux.HandleFunc("/api/dns-routes/update", func(w http.ResponseWriter, r *http.Request) {
		updateCalled = true
		if r.URL.Query().Get("id") != "hr:Video" {
			t.Fatalf("update id=%q, want hr:Video", r.URL.Query().Get("id"))
		}
		if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
			t.Fatalf("decode update body: %v", err)
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var execs []string
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		execs = append(execs, strings.Join(append([]string{name}, args...), " "))
		return []byte("ok"), nil
	}
	noSleep := func(context.Context, time.Duration) error { return nil }
	conf := base64.StdEncoding.EncodeToString([]byte(awgConf))
	out, err := ImportTunnel(context.Background(), awgmgr.New(srv.URL), exec, noSleep, conf, "selfhosted-home", false, "nativewg")
	if err != nil {
		t.Fatalf("ImportTunnel: %v\n%s", err, out)
	}
	if !updateCalled {
		t.Fatal("expected HydraRoute policy update after live tunnel import")
	}
	if got := strings.Join(updated.HRPolicyInterfaces, ","); got != "nwg1,nwg7" {
		t.Fatalf("policy interfaces = %q, want nwg1,nwg7; updated=%+v", got, updated)
	}
	if len(execs) != 1 || execs[0] != "/opt/etc/init.d/S99hrneo restart" {
		t.Fatalf("expected one HR restart, got %+v", execs)
	}
}
