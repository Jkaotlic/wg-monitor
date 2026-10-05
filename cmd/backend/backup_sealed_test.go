package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
	"github.com/Jkaotlic/wg-monitor/internal/backend/sealedfile"
)

// sealFixtureStores шифрует хранилища фикстуры её ключом оживления -- так,
// как это делает старт бэкенда (v0.55, B1). Ключ процесса снимается сразу:
// проверка обязана брать ключ сама, из конфига.
func (f *backupFixture) sealFixtureStores(t *testing.T) {
	t.Helper()
	key, err := revive.LoadKey(f.keyPath)
	if err != nil {
		t.Fatal(err)
	}
	box, err := revive.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	sealedfile.SetKey(box)
	defer sealedfile.SetKey(nil)
	for _, name := range []string{"amnezia-premium.json", "amnezia-selfhosted.json", "awg3-panels.json", "hidemyname.json"} {
		changed, err := sealedfile.Reseal(filepath.Join(f.dir, "data", name), name)
		if err != nil || !changed {
			t.Fatalf("%s: changed=%v err=%v", name, changed, err)
		}
	}
}

// Зашифрованные хранилища едут в архив как есть; проверка восстановления
// расшифровывает их ключом этой машины и говорит, что ключа в архиве нет.
func TestBackupVerifySealedStoresWithKey(t *testing.T) {
	setIncludeReviveKey(t, false)
	f := newBackupFixture(t)
	f.sealFixtureStores(t)
	f.backupSmall(t)
	var out bytes.Buffer
	if err := runBackupVerify(context.Background(), f.verifyOpts(&out)); err != nil {
		t.Fatalf("зашифрованные хранилища провалили проверку: %v", err)
	}
	for _, want := range []string{"хранилища кабинетов в архиве зашифрованы", "расшифровываются ключом шифрования этой машины", "без него ключи кабинетов не прочитать"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("в выводе нет %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "PREMIUM-KEY") {
		t.Fatal("секрет в выводе")
	}
}

// Ключа на машине нет -- проверка говорит словами, что кабинеты после
// восстановления не прочитать.
func TestBackupVerifySealedStoresWithoutKey(t *testing.T) {
	setIncludeReviveKey(t, false)
	f := newBackupFixture(t)
	f.sealFixtureStores(t)
	f.backupSmall(t)
	f.writeConfig(t, false)
	var out bytes.Buffer
	if err := runBackupVerify(context.Background(), f.verifyOpts(&out)); err != nil {
		t.Fatalf("проверка: %v", err)
	}
	if !strings.Contains(out.String(), "ключа шифрования на этой машине нет: после восстановления ключи кабинетов не прочитать") {
		t.Fatalf("вывод:\n%s", out.String())
	}
}

// Ключ на машине не тот -- провал словами.
func TestBackupVerifySealedStoresWrongKey(t *testing.T) {
	setIncludeReviveKey(t, false)
	f := newBackupFixture(t)
	f.sealFixtureStores(t)
	f.backupSmall(t)
	mustWrite(t, f.keyPath, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, revive.KeySize))+"\n")
	f.verifyFails(t, "не расшифровывается ключом шифрования этой машины")
	if _, err := os.Stat(f.keyPath); err != nil {
		t.Fatal(err)
	}
}

// Fix round 1: битый ключ на машине (не «нет файла») -- провал проверки, а
// не «ключа нет».
func TestBackupVerifySealedStoresBrokenKey(t *testing.T) {
	setIncludeReviveKey(t, false)
	f := newBackupFixture(t)
	f.sealFixtureStores(t)
	f.backupSmall(t)
	mustWrite(t, f.keyPath, "not-base64-at-all!\n")
	f.verifyFails(t, "ключ шифрования этой машины негоден")
}
