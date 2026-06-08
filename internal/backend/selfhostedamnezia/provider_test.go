package selfhostedamnezia

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	files map[string][]byte
	calls [][]string
}

func (r *fakeRunner) Run(_ context.Context, args []string, stdin []byte) ([]byte, error) {
	r.calls = append(r.calls, append([]string{}, args...))
	if len(args) >= 2 && args[0] == "cat" {
		return append([]byte{}, r.files[args[1]]...), nil
	}
	if len(args) >= 3 && args[0] == "sh" && strings.Contains(args[2], "cat >") {
		r.files[args[4]] = append([]byte{}, stdin...)
		return nil, nil
	}
	if len(args) >= 3 && args[0] == "mv" {
		r.files[args[2]] = r.files[args[1]]
		delete(r.files, args[1])
		return nil, nil
	}
	return nil, nil
}

func TestIssueWithRunnerGeneratesClientAndMutatesState(t *testing.T) {
	r := &fakeRunner{files: map[string][]byte{
		"/opt/amnezia/awg/awg0.conf": []byte(`[Interface]
PrivateKey = server-private
Address = 10.8.1.0/24
ListenPort = 47567
Jc = 6
Jmin = 10
Jmax = 50
S1 = 59
S2 = 135
S3 = 29
S4 = 7
H1 = 1778706634-1861635010
H2 = 2112724985-2122087405
H3 = 2137253343-2142792754
H4 = 2144447197-2144999433

[Peer]
PublicKey = old
PresharedKey = psk
AllowedIPs = 10.8.1.12/32
`),
		"/opt/amnezia/awg/clientsTable":                    []byte(`[]`),
		"/opt/amnezia/awg/wireguard_server_public_key.key": []byte("server-public\n"),
		"/opt/amnezia/awg/wireguard_psk.key":               []byte("server-psk\n"),
	}}
	cfg := Config{Enabled: true, EndpointHost: "vpn.example.com", EndpointPort: 47567}

	issued, err := IssueWithRunner(context.Background(), cfg, r, "del router", time.Date(2026, 5, 25, 13, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if issued.Name != "del-router" {
		t.Fatalf("name = %q, want sanitized del-router", issued.Name)
	}
	if issued.Address != "10.8.1.13/32" {
		t.Fatalf("address = %q, want next after max existing peer", issued.Address)
	}
	conf := string(issued.Config)
	for _, want := range []string{
		"Address = 10.8.1.13/32",
		"Endpoint = vpn.example.com:47567",
		"PublicKey = server-public",
		"PresharedKey = server-psk",
		"Jc = 6",
		"AllowedIPs = 0.0.0.0/0, ::/0",
	} {
		if !strings.Contains(conf, want) {
			t.Fatalf("client conf missing %q:\n%s", want, conf)
		}
	}
	if !strings.Contains(string(issued.ServerConf), "AllowedIPs = 10.8.1.13/32") {
		t.Fatalf("server conf missing new peer:\n%s", issued.ServerConf)
	}
	if !strings.Contains(string(issued.Clients), `"clientName": "del-router"`) || !strings.Contains(string(issued.Clients), `"allowedIps": "10.8.1.13/32"`) {
		t.Fatalf("clients table missing new client:\n%s", issued.Clients)
	}
	if !hasCall(r.calls, "wg", "set", "awg0", "peer") {
		t.Fatalf("wg set call missing: %+v", r.calls)
	}
}

func TestAllocateAddressRejectsFullPool(t *testing.T) {
	prefix := mustPrefix(t, "10.8.1.0/30")
	used := map[string]bool{"10.8.1.1": true, "10.8.1.2": true}
	usedAddrs := mapAddrSet(t, used)
	if _, err := allocateAddress(prefix, usedAddrs); err == nil {
		t.Fatal("expected no free address")
	}
}

func TestStoreUpsertRoundTripAndProviderConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selfhosted.json")
	store, err := LoadStore(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Instances) != 0 {
		t.Fatalf("new store instances = %d, want 0", len(store.Instances))
	}

	inst := Instance{
		ID:           "home",
		Label:        "Home VPS",
		EndpointHost: "vpn.example.com",
		EndpointPort: 47567,
		DNS:          []string{"1.1.1.1", "8.8.8.8"},
	}
	if err := store.Upsert(inst); err != nil {
		t.Fatal(err)
	}
	if err := SaveStore(path, store); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}

	loaded, err := LoadStore(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Get("home")
	if !ok {
		t.Fatalf("home instance not found: %+v", loaded.Instances)
	}
	if !got.Enabled {
		t.Fatalf("new instance should default to enabled: %+v", got)
	}
	cfg := Config{}.ProviderConfig(got)
	if !cfg.Ready() {
		t.Fatalf("provider config is not ready: %+v", cfg)
	}
	if cfg.Container != "amnezia-awg2" || cfg.Interface != "awg0" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if strings.Join(cfg.DNS, ",") != "1.1.1.1,8.8.8.8" {
		t.Fatalf("dns = %+v", cfg.DNS)
	}
}

func TestStoreUpsertRoundTripAndProviderConfigWithSSHPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selfhosted.json")
	store, err := LoadStore(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	inst := Instance{
		ID:           "home",
		Label:        "Home VPS",
		EndpointHost: "vpn.example.com",
		EndpointPort: 47567,
		SSHHost:      "10.10.10.2",
		SSHPort:      2222,
		SSHUser:      "root",
		SSHPassword:  "secret-pass",
	}
	if err := store.Upsert(inst); err != nil {
		t.Fatal(err)
	}
	if err := SaveStore(path, store); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadStore(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Get("home")
	if !ok {
		t.Fatalf("home instance not found: %+v", loaded.Instances)
	}
	if got.SSHHost != "10.10.10.2" || got.SSHPort != 2222 || got.SSHUser != "root" || got.SSHPassword != "secret-pass" {
		t.Fatalf("SSH fields were not preserved: %+v", got)
	}
	cfg := Config{}.ProviderConfig(got)
	if cfg.SSHHost != "10.10.10.2" || cfg.SSHPort != 2222 || cfg.SSHUser != "root" || cfg.SSHPassword != "secret-pass" {
		t.Fatalf("ProviderConfig did not inherit SSH fields: %+v", cfg)
	}
	if !cfg.Ready() {
		t.Fatalf("provider config should be ready with SSH-backed VPS: %+v", cfg)
	}
}

func TestRunnerForConfigUsesRemoteDockerWhenSSHPasswordConfigured(t *testing.T) {
	cfg := Config{Container: "amnezia-awg2", SSHHost: "10.10.10.2", SSHPort: 2222, SSHUser: "root", SSHPassword: "secret-pass"}
	r := runnerForConfig(cfg)
	remote, ok := r.(RemoteDockerRunner)
	if !ok {
		t.Fatalf("runner type = %T, want RemoteDockerRunner", r)
	}
	if remote.Host != "10.10.10.2" || remote.Port != 2222 || remote.User != "root" || remote.Password != "secret-pass" || remote.Container != "amnezia-awg2" {
		t.Fatalf("bad remote runner: %+v", remote)
	}
}

func hasCall(calls [][]string, parts ...string) bool {
	for _, call := range calls {
		if len(call) < len(parts) {
			continue
		}
		ok := true
		for i, part := range parts {
			if call[i] != part {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func mustPrefix(t *testing.T, raw string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mapAddrSet(t *testing.T, raw map[string]bool) map[netip.Addr]bool {
	t.Helper()
	out := make(map[netip.Addr]bool)
	for k, v := range raw {
		addr, err := netip.ParseAddr(k)
		if err != nil {
			t.Fatal(err)
		}
		out[addr] = v
	}
	return out
}
