package callbacks

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAmneziaSecretsStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amnezia-premium.json")
	r := &Router{cfg: Config{AmneziaSecretsPath: path}}

	if got, err := r.getAmneziaKey(7); err != nil || got != "" {
		t.Fatalf("missing key = %q, err=%v", got, err)
	}
	if err := r.setAmneziaKey(7, "vpn://secret"); err != nil {
		t.Fatal(err)
	}
	if err := r.setAmneziaKey(8, "vpn://other"); err != nil {
		t.Fatal(err)
	}
	got, err := r.getAmneziaKey(7)
	if err != nil {
		t.Fatal(err)
	}
	if got != "vpn://secret" {
		t.Fatalf("key = %q", got)
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

func TestAmneziaSecretsRejectsNonVPNKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amnezia-premium.json")
	r := &Router{cfg: Config{AmneziaSecretsPath: path}}

	if err := r.setAmneziaKey(7, "not-a-key"); err == nil {
		t.Fatal("expected non-vpn key to fail")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("secret file should not be created, stat err=%v", err)
	}
}
