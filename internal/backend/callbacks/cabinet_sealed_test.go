package callbacks

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

// sealKeyForTest ставит ключ шифрования процесса на время теста. Тест не
// параллельный: параллельные тесты пакета стартуют после него, когда ключ
// уже снят.
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

func TestAmneziaSecrets_SealedOnDiskWithKey(t *testing.T) {
	sealKeyForTest(t)
	path := filepath.Join(t.TempDir(), "amnezia-premium.json")
	r := &Router{cfg: Config{AmneziaSecretsPath: path}}
	const key = "vpn://sealed-fixture-key-91c2"
	if _, err := r.addAmneziaKeyLabeled(7, key, ""); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), key) || !sealedfile.IsSealed(raw) {
		t.Fatalf("ключ лежит на диске открытым: %q", raw)
	}
	got, err := r.getAmneziaKey(7)
	if err != nil || got != key {
		t.Fatalf("got %q, %v", got, err)
	}
	// Без ключа -- понятная ошибка, файл цел.
	sealedfile.SetKey(nil)
	if _, err := r.listAmneziaKeys(7); !errors.Is(err, sealedfile.ErrKeyMissing) {
		t.Fatalf("без ключа: err = %v", err)
	}
	if _, err := r.addAmneziaKeyLabeled(7, "vpn://another", ""); !errors.Is(err, sealedfile.ErrKeyMissing) {
		t.Fatalf("запись без ключа: err = %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(raw) {
		t.Fatal("файл испорчен")
	}
}

func TestHideMySecrets_SealedOnDiskWithKey(t *testing.T) {
	sealKeyForTest(t)
	path := filepath.Join(t.TempDir(), "hidemyname.json")
	r := &Router{cfg: Config{HideMySecretsPath: path}}
	const code = "98765432109876"
	if _, err := r.addHideMyCodeLabeled(7, code, ""); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), code) || !sealedfile.IsSealed(raw) {
		t.Fatalf("код лежит на диске открытым: %q", raw)
	}
	got, err := r.getHideMyCode(7)
	if err != nil || got != code {
		t.Fatalf("got %q, %v", got, err)
	}
	sealedfile.SetKey(nil)
	if _, err := r.getHideMyCode(7); !errors.Is(err, sealedfile.ErrKeyMissing) {
		t.Fatalf("без ключа: err = %v", err)
	}
}

// Экран кабинетов и выпуск: зашифрованный файл без ключа -- не «ключ не
// сохранён» (человек завёл бы ключ заново поверх целого файла), а слова
// про ключ шифрования.
func TestMiniappCabinet_SealedWithoutKeySpeaksAboutKey(t *testing.T) {
	sealKeyForTest(t)
	dir := t.TempDir()
	r := &Router{cfg: Config{
		AmneziaSecretsPath: filepath.Join(dir, "amnezia-premium.json"),
		HideMySecretsPath:  filepath.Join(dir, "hidemyname.json"),
	}}
	if _, err := r.addAmneziaKeyLabeled(7, "vpn://sealed-account-key", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.addHideMyCodeLabeled(7, "12345678901234", ""); err != nil {
		t.Fatal(err)
	}
	sealedfile.SetKey(nil)
	for _, provider := range []string{providerAmnezia, providerHideMy} {
		acc, err := r.Account(t.Context(), 7, provider)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(acc.Note, "ключ шифрования не найден") {
			t.Fatalf("%s: note = %q", provider, acc.Note)
		}
		if _, err := r.IssueConfig(t.Context(), 7, provider, "de"); !errors.Is(err, sealedfile.ErrKeyMissing) {
			t.Fatalf("%s: выпуск err = %v", provider, err)
		}
	}
}
