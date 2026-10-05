package awg3panel

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

// v0.55 (B1): пароль панели и закрытый ключ сертификата на диске -- шифром
// revive.key.
func TestStore_SealedOnDiskWithKey(t *testing.T) {
	sealKeyForTest(t)
	path := filepath.Join(t.TempDir(), DefaultStoreName)
	const pw = "Sealed-Panel-Pass-c4d0"
	const keyPEM = "fixture-key-pem-sealed-fixture"
	st := Store{Version: 1, Instances: []Instance{{ID: "nl2", Password: pw, KeyPEM: keyPEM}}}
	if err := SaveStore(path, st); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), pw) || strings.Contains(string(raw), "sealed-fixture") || !sealedfile.IsSealed(raw) {
		t.Fatalf("секрет панели лежит на диске открытым: %q", raw)
	}
	got, err := LoadStore(path)
	if err != nil || len(got.Instances) != 1 || got.Instances[0].Password != pw || got.Instances[0].KeyPEM != keyPEM {
		t.Fatalf("не прочиталось: %v", err)
	}
	sealedfile.SetKey(nil)
	if _, err := LoadStore(path); !errors.Is(err, sealedfile.ErrKeyMissing) {
		t.Fatalf("без ключа: err = %v", err)
	}
	if err := SaveStore(path, st); !errors.Is(err, sealedfile.ErrKeyMissing) {
		t.Fatalf("запись без ключа: err = %v", err)
	}
}
