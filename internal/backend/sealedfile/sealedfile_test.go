package sealedfile

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
)

func newBox(t *testing.T) *revive.Box {
	t.Helper()
	k := make([]byte, revive.KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	b, err := revive.NewBox(k)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// withKey ставит ключ процесса на время теста. Тесты пакета не параллельны:
// ключ -- общий для процесса.
func withKey(t *testing.T, b *revive.Box) {
	t.Helper()
	prev := state.Load()
	SetKey(b)
	t.Cleanup(func() { state.Store(prev) })
}

const secret = "vpn://fixture-secret-3f9a"

func TestWriteRead_SealedWithKey(t *testing.T) {
	withKey(t, newBox(t))
	path := filepath.Join(t.TempDir(), "amnezia-premium.json")
	plain := []byte(`{"version":1,"k":"` + secret + `"}` + "\n")
	if err := WriteFile(path, DomainAmnezia, plain); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("секрет лежит на диске открытым")
	}
	if !IsSealed(raw) || !strings.HasPrefix(string(raw), Magic) {
		t.Fatalf("нет заголовка версии: %q", raw[:min(len(raw), 40)])
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("права %v", info.Mode().Perm())
	}
	got, err := ReadFile(path, DomainAmnezia)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("got %q", got)
	}
}

func TestWriteRead_PlainWithoutKey(t *testing.T) {
	withKey(t, nil)
	path := filepath.Join(t.TempDir(), "hidemyname.json")
	plain := []byte(`{"version":1}` + "\n")
	if err := WriteFile(path, DomainHideMy, plain); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Equal(raw, plain) {
		t.Fatalf("без ключа файл обязан остаться открытым JSON: %q", raw)
	}
	got, err := ReadFile(path, DomainHideMy)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("got %q, %v", got, err)
	}
}

// С ключом открытый JSON по-прежнему читается: до перешифровки и после
// сбоя перешифровки кабинеты обязаны работать.
func TestRead_PlainWithKey(t *testing.T) {
	withKey(t, newBox(t))
	path := filepath.Join(t.TempDir(), "x.json")
	plain := []byte("  {\"version\":1}\n")
	_ = os.WriteFile(path, plain, 0o600)
	got, err := ReadFile(path, DomainAwg3)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestRead_MissingFileKeepsNotExist(t *testing.T) {
	withKey(t, newBox(t))
	_, err := ReadFile(filepath.Join(t.TempDir(), "nope.json"), DomainAmnezia)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

// Зашифрованный файл без ключа -- понятная ошибка, а запись поверх него
// отказывает: файл остаётся байт в байт прежним.
func TestSealedWithoutKey_ErrorAndNoDamage(t *testing.T) {
	box := newBox(t)
	withKey(t, box)
	path := filepath.Join(t.TempDir(), "amnezia-selfhosted.json")
	if err := WriteFile(path, DomainSelfHosted, []byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	SetKey(nil)
	_, err := ReadFile(path, DomainSelfHosted)
	if !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "ключ шифрования не найден") {
		t.Fatalf("текст: %q", err.Error())
	}
	if err := WriteFile(path, DomainSelfHosted, []byte(`{"version":2}`)); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("запись поверх шифра без ключа: err = %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("файл испорчен")
	}
}

func TestSealedForeignDomainOrKey_Unreadable(t *testing.T) {
	withKey(t, newBox(t))
	dir := t.TempDir()
	a := filepath.Join(dir, "amnezia-premium.json")
	if err := WriteFile(a, DomainAmnezia, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(a, DomainHideMy); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("чужой домен: err = %v", err)
	}
	SetKey(newBox(t))
	if _, err := ReadFile(a, DomainAmnezia); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("чужой ключ: err = %v", err)
	}
}

func TestDecode_GarbageAfterMagic(t *testing.T) {
	withKey(t, newBox(t))
	if _, err := Decode(DomainAmnezia, []byte(Magic+"!!!не base64\n")); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("err = %v", err)
	}
}

// Перешифровка: открытый файл становится шифром, содержимое то же;
// повторный вызов ничего не меняет; временных файлов не остаётся.
func TestReseal_Idempotent(t *testing.T) {
	withKey(t, newBox(t))
	dir := t.TempDir()
	path := filepath.Join(dir, "hidemyname.json")
	plain := []byte(`{"version":1,"code":"1234567890"}` + "\n")
	if err := os.WriteFile(path, plain, 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := Reseal(path, DomainHideMy)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	raw, _ := os.ReadFile(path)
	if !IsSealed(raw) || bytes.Contains(raw, []byte("1234567890")) {
		t.Fatal("файл не зашифрован")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("права %v", info.Mode().Perm())
	}
	got, err := ReadFile(path, DomainHideMy)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("got %q err %v", got, err)
	}
	changed, err = Reseal(path, DomainHideMy)
	if err != nil || changed {
		t.Fatalf("повтор: changed=%v err=%v", changed, err)
	}
	raw2, _ := os.ReadFile(path)
	if !bytes.Equal(raw, raw2) {
		t.Fatal("повторный старт переписал файл")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("в каталоге лишнее: %v", entries)
	}
}

func TestReseal_NoKeyOrNoFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "awg3-panels.json")
	withKey(t, newBox(t))
	if changed, err := Reseal(path, DomainAwg3); err != nil || changed {
		t.Fatalf("нет файла: changed=%v err=%v", changed, err)
	}
	_ = os.WriteFile(path, []byte(`{}`), 0o600)
	SetKey(nil)
	if changed, err := Reseal(path, DomainAwg3); err != nil || changed {
		t.Fatalf("нет ключа: changed=%v err=%v", changed, err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != `{}` {
		t.Fatal("без ключа файл тронут")
	}
}

// Защита в глубину (fix round 1): с ключом запись поверх зашифрованного
// файла, который этим ключом не открывается, отказывает -- иначе чужой ключ
// молча затёр бы файл, записанный прежним.
func TestWrite_RefusesOverSealedFileOfOtherKey(t *testing.T) {
	withKey(t, newBox(t))
	path := filepath.Join(t.TempDir(), "amnezia-premium.json")
	if err := WriteFile(path, DomainAmnezia, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	SetKey(newBox(t))
	if err := WriteFile(path, DomainAmnezia, []byte(`{"a":2}`)); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("err = %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("файл перезаписан чужим ключом")
	}
}

// Fix round 2: режим «ключ не тот». Открытые файлы пишутся открытыми (как
// без ключа), поверх шифра запись отказывает, чтение шифра -- ErrUnreadable.
func TestWrongKeyMode(t *testing.T) {
	a := newBox(t)
	withKey(t, a)
	dir := t.TempDir()
	sealedPath := filepath.Join(dir, "amnezia-premium.json")
	if err := WriteFile(sealedPath, DomainAmnezia, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(sealedPath)
	plainPath := filepath.Join(dir, "hidemyname.json")
	_ = os.WriteFile(plainPath, []byte(`{"h":1}`), 0o600)

	SetWrongKey(newBox(t))
	if Enabled() {
		t.Fatal("в режиме «ключ не тот» новые записи не шифруются")
	}
	if err := WriteFile(plainPath, DomainHideMy, []byte(`{"h":2}`)); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(plainPath)
	if string(raw) != `{"h":2}` {
		t.Fatalf("открытое хранилище зашифровано чужим ключом: %q", raw)
	}
	if _, err := ReadFile(sealedPath, DomainAmnezia); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("чтение шифра: err = %v", err)
	}
	if err := WriteFile(sealedPath, DomainAmnezia, []byte(`{"a":2}`)); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("запись поверх шифра: err = %v", err)
	}
	after, _ := os.ReadFile(sealedPath)
	if !bytes.Equal(before, after) {
		t.Fatal("шифр перезаписан")
	}
	if changed, err := Reseal(plainPath, DomainHideMy); err != nil || changed {
		t.Fatalf("Reseal в режиме «ключ не тот»: changed=%v err=%v", changed, err)
	}
	SetKey(a)
	if _, err := ReadFile(sealedPath, DomainAmnezia); err != nil {
		t.Fatal(err)
	}
}
