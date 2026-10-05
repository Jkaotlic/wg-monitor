package selfhostedamnezia

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
	"github.com/Jkaotlic/wg-monitor/internal/backend/sealedfile"
)

func sealKeyForTest(t *testing.T) {
	t.Helper()
	k := make([]byte, revive.KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	b, err := revive.NewBox(k)
	if err != nil {
		t.Fatal(err)
	}
	sealedfile.SetKey(b)
	t.Cleanup(func() { sealedfile.SetKey(nil) })
}

// v0.55 (B1): SSH-пароли своих серверов на диске -- шифром revive.key.
func TestStore_SealedOnDiskWithKey(t *testing.T) {
	sealKeyForTest(t)
	path := filepath.Join(t.TempDir(), "amnezia-selfhosted.json")
	const pw = "Sealed-SSH-Pass-77b1"
	st := Store{Version: 1, ActiveID: "nl1", Instances: []Instance{{ID: "nl1", Label: "nl1", EndpointHost: "203.0.113.10", EndpointPort: 51820, SSHHost: "203.0.113.10", SSHPassword: pw}}}
	if err := SaveStore(path, st); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), pw) || !sealedfile.IsSealed(raw) {
		t.Fatalf("пароль лежит на диске открытым: %q", raw)
	}
	got, err := LoadStore(path, Config{})
	if err != nil || len(got.Instances) != 1 || got.Instances[0].SSHPassword != pw {
		t.Fatalf("got %+v, %v", got.Instances, err)
	}
	sealedfile.SetKey(nil)
	if _, err := LoadStore(path, Config{}); !errors.Is(err, sealedfile.ErrKeyMissing) {
		t.Fatalf("без ключа: err = %v", err)
	}
	if err := SaveStore(path, st); !errors.Is(err, sealedfile.ErrKeyMissing) {
		t.Fatalf("запись без ключа: err = %v", err)
	}
}
