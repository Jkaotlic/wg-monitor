package callbacks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAmneziaSecretsStoreMultipleKeysAndActive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amnezia-premium.json")
	r := &Router{cfg: Config{AmneziaSecretsPath: path}}

	if got, err := r.getAmneziaKey(7); err != nil || got != "" {
		t.Fatalf("missing key = %q, err=%v", got, err)
	}
	first, err := r.addAmneziaKey(7, "vpn://first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.addAmneziaKey(7, "vpn://second")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("key ids must differ: %q", first.ID)
	}
	keys, err := r.listAmneziaKeys(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys.Keys) != 2 {
		t.Fatalf("keys len = %d, want 2", len(keys.Keys))
	}
	if keys.ActiveID != second.ID {
		t.Fatalf("active id = %q, want second %q", keys.ActiveID, second.ID)
	}
	got, err := r.getAmneziaKeyByID(7, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != "vpn://first" {
		t.Fatalf("key = %q", got)
	}
	if err := r.deleteAmneziaKey(7, second.ID); err != nil {
		t.Fatal(err)
	}
	keys, err = r.listAmneziaKeys(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys.Keys) != 1 || keys.ActiveID != first.ID {
		t.Fatalf("after delete = %+v, want only first active", keys)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != 0o600 {
			t.Fatalf("secret file mode = %o, want 600", got)
		}
	}
}

func TestAmneziaSecretsReadsLegacySingleKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amnezia-premium.json")
	if err := os.WriteFile(path, []byte(`{"7":"vpn://legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Router{cfg: Config{AmneziaSecretsPath: path}}

	keys, err := r.listAmneziaKeys(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys.Keys) != 1 || keys.Keys[0].VPNKey != "vpn://legacy" || keys.ActiveID == "" {
		t.Fatalf("legacy keys = %+v", keys)
	}
	if got, err := r.getAmneziaKey(7); err != nil || got != "vpn://legacy" {
		t.Fatalf("active legacy key = %q, err=%v", got, err)
	}
	if _, err := r.addAmneziaKey(7, "vpn://new"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var env amneziaSecretFile
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	if env.Version != 1 || len(env.Routers["7"].Keys) != 2 {
		t.Fatalf("migrated file = %+v", env)
	}
}

func TestAmneziaSecretsRejectsNonVPNKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amnezia-premium.json")
	r := &Router{cfg: Config{AmneziaSecretsPath: path}}

	if _, err := r.addAmneziaKey(7, "not-a-key"); err == nil {
		t.Fatal("expected non-vpn key to fail")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("secret file should not be created, stat err=%v", err)
	}
}
