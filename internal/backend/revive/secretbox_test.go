package revive

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Пароли фикстуры. Их ищут сторожа во всех выводах пакета.
const (
	fixtureRoot   = "Fixture-Root-9d1e"
	fixtureLogin  = "fixture-admin-4c2b"
	fixturePanel  = "Fixture-Panel-77aa"
	fixtureAPIKey = "fixture-apikey-51f0"
)

func fixtureSecrets() Secrets {
	return Secrets{RootPassword: fixtureRoot, AWGMLogin: fixtureLogin, AWGMPassword: fixturePanel, AWGMAPIKey: fixtureAPIKey}
}

func assertNoFixtureSecret(t *testing.T, where string, got []byte) {
	t.Helper()
	for _, s := range []string{fixtureRoot, fixtureLogin, fixturePanel, fixtureAPIKey} {
		if bytes.Contains(got, []byte(s)) {
			t.Fatalf("%s содержит секрет фикстуры %q", where, s)
		}
	}
}

func testKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func writeKeyFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "revive.key")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadKey(t *testing.T) {
	key := testKey(t)
	good := writeKeyFile(t, base64.StdEncoding.EncodeToString(key)+"\n")
	got, err := LoadKey(good)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("хороший ключ с переводом строки: %v", err)
	}

	if _, err := LoadKey("  "); !errors.Is(err, ErrKeyNotConfigured) {
		t.Fatalf("пустой путь: %v", err)
	}
	if _, err := LoadKey(filepath.Join(t.TempDir(), "missing.key")); err == nil {
		t.Fatal("нет файла -- функция выключена")
	}
	short := writeKeyFile(t, base64.StdEncoding.EncodeToString(key[:16]))
	if _, err := LoadKey(short); err == nil || !strings.Contains(err.Error(), "16") {
		t.Fatalf("ключ 16 байт обязан отвергаться с длиной в тексте: %v", err)
	}
	notB64 := writeKeyFile(t, "это не base64 !!!")
	_, err = LoadKey(notB64)
	if err == nil {
		t.Fatal("не base64 -- ошибка")
	}
	if strings.Contains(err.Error(), "это не base64") {
		t.Fatalf("текст ошибки не должен повторять содержимое файла ключа: %v", err)
	}
}

func TestBox_RoundTrip(t *testing.T) {
	box, err := NewBox(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	nonce, ct, err := box.Seal(7, fixtureSecrets())
	if err != nil {
		t.Fatal(err)
	}
	if len(nonce) != 12 {
		t.Fatalf("nonce %d байт, ждали 12", len(nonce))
	}
	assertNoFixtureSecret(t, "шифртекст", ct)
	got, err := box.Open(7, nonce, ct)
	if err != nil {
		t.Fatal(err)
	}
	if got != fixtureSecrets() {
		t.Fatal("расшифровано не то, что зашифровано")
	}
	// Два шифрования одного и того же -- разные nonce и шифртекст.
	nonce2, ct2, _ := box.Seal(7, fixtureSecrets())
	if bytes.Equal(nonce, nonce2) || bytes.Equal(ct, ct2) {
		t.Fatal("nonce обязан быть случайным на каждую запись")
	}
}

func TestBox_ForeignAADFails(t *testing.T) {
	box, _ := NewBox(testKey(t))
	nonce, ct, _ := box.Seal(7, fixtureSecrets())
	if _, err := box.Open(8, nonce, ct); !errors.Is(err, ErrSecretUnreadable) {
		t.Fatalf("секрет чужого роутера не расшифровывается: %v", err)
	}
}

// Базы без ключа недостаточно: другой ключ (или ключ, потерянный вместе с
// файлом secrets/revive.key) не открывает шифртекст.
func TestBox_WrongKeyFails(t *testing.T) {
	a, _ := NewBox(testKey(t))
	b, _ := NewBox(testKey(t))
	nonce, ct, _ := a.Seal(7, fixtureSecrets())
	if _, err := b.Open(7, nonce, ct); !errors.Is(err, ErrSecretUnreadable) {
		t.Fatalf("чужой ключ: %v", err)
	}
	ct[0] ^= 0xff
	if _, err := a.Open(7, nonce, ct); !errors.Is(err, ErrSecretUnreadable) {
		t.Fatalf("битый шифртекст: %v", err)
	}
	if _, err := a.Open(7, nonce[:5], ct); !errors.Is(err, ErrSecretUnreadable) {
		t.Fatalf("битый nonce: %v", err)
	}
}

func TestNewBox_RejectsBadKeyLength(t *testing.T) {
	if _, err := NewBox(make([]byte, 16)); err == nil {
		t.Fatal("ключ 16 байт -- не AES-256")
	}
}

func TestSecrets_NeverPrintThemselves(t *testing.T) {
	s := fixtureSecrets()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("проверка", "creds", s)
	slog.New(slog.NewTextHandler(&buf, nil)).Info("проверка", "creds", s)
	fmt.Fprintf(&buf, "%v %+v %#v %s", s, s, s, s)
	js, _ := json.Marshal(struct{ S Secrets }{s})
	buf.Write(js)
	assertNoFixtureSecret(t, "печать Secrets", buf.Bytes())
}

// Пре-флайт координатора 15.09: движок переустановки без root-пароля
// отказывает (provision_handler.go:466-471 root_password_required), вход в
// панель -- дополнительный, но не самостоятельный. Usable() требует ровно
// RootPassword != "".
func TestSecrets_Usable(t *testing.T) {
	cases := []struct {
		s    Secrets
		want bool
	}{
		{Secrets{}, false},
		{Secrets{RootPassword: "x"}, true},
		{Secrets{AWGMAPIKey: "k"}, false},
		{Secrets{AWGMLogin: "admin"}, false},
		{Secrets{AWGMLogin: "admin", AWGMPassword: "p"}, false},
		{Secrets{RootPassword: "x", AWGMAPIKey: "k"}, true},
	}
	for i, c := range cases {
		if got := c.s.Usable(); got != c.want {
			t.Fatalf("case %d: Usable=%v, want %v", i, got, c.want)
		}
	}
}
