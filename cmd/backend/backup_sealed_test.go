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
	"github.com/Jkaotlic/wg-monitor/internal/backup"
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

// v0.62 (BACK-04): итог пробного открытия зашифрованных хранилищ ложится в
// backup-status.json -- экран видит его до аварии, а не после неё. Секретов
// в файле состояния нет ни в каком исходе.
func TestBackupVerifyRecordsSealedStoresOpen(t *testing.T) {
	setIncludeReviveKey(t, false)
	statusBody := func(t *testing.T, f *backupFixture) string {
		t.Helper()
		body, err := os.ReadFile(f.statusFilePath())
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"PREMIUM-KEY", "203.0.113.7", "panel.example.com", "1234567890", fixturePassphrase} {
			if strings.Contains(string(body), secret) {
				t.Fatalf("секрет %q в файле состояния:\n%s", secret, body)
			}
		}
		return string(body)
	}
	t.Run("ключ тот", func(t *testing.T) {
		f := newBackupFixture(t)
		f.sealFixtureStores(t)
		f.backupSmall(t)
		if err := runBackupVerify(context.Background(), f.verifyOpts(&bytes.Buffer{})); err != nil {
			t.Fatal(err)
		}
		v := f.status(t).Verify
		if v.SealedStoresOpen == nil || !*v.SealedStoresOpen || v.SealedKeyMissing {
			t.Fatalf("состояние: %+v", v)
		}
		if !strings.Contains(v.SealedStoresNote, "открываются текущим ключом") {
			t.Fatalf("заметка: %q", v.SealedStoresNote)
		}
		if !strings.Contains(statusBody(t, f), `"sealed_stores_open": true`) {
			t.Fatal("в файле нет sealed_stores_open: true")
		}
	})
	t.Run("ключа нет на сервере", func(t *testing.T) {
		f := newBackupFixture(t)
		f.sealFixtureStores(t)
		f.backupSmall(t)
		if err := os.Remove(f.keyPath); err != nil {
			t.Fatal(err)
		}
		if err := runBackupVerify(context.Background(), f.verifyOpts(&bytes.Buffer{})); err != nil {
			t.Fatalf("нет ключа -- не провал проверки: %v", err)
		}
		v := f.status(t).Verify
		if v.SealedStoresOpen == nil || *v.SealedStoresOpen || !v.SealedKeyMissing || !v.OK {
			t.Fatalf("состояние: %+v", v)
		}
		if !strings.Contains(v.SealedStoresNote, "ключа нет на сервере") {
			t.Fatalf("заметка: %q", v.SealedStoresNote)
		}
		statusBody(t, f)
	})
	t.Run("ключ не тот", func(t *testing.T) {
		f := newBackupFixture(t)
		f.sealFixtureStores(t)
		f.backupSmall(t)
		mustWrite(t, f.keyPath, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, revive.KeySize))+"\n")
		f.verifyFails(t, "не расшифровывается ключом шифрования этой машины")
		v := f.status(t).Verify
		if v.SealedStoresOpen == nil || *v.SealedStoresOpen || v.SealedKeyMissing {
			t.Fatalf("состояние: %+v", v)
		}
		if !strings.Contains(v.SealedStoresNote, "не открываются текущим ключом") {
			t.Fatalf("заметка: %q", v.SealedStoresNote)
		}
		statusBody(t, f)
	})
	t.Run("ключ испорчен", func(t *testing.T) {
		f := newBackupFixture(t)
		f.sealFixtureStores(t)
		f.backupSmall(t)
		mustWrite(t, f.keyPath, "not-base64-at-all!\n")
		f.verifyFails(t, "ключ шифрования этой машины негоден")
		v := f.status(t).Verify
		if v.SealedStoresOpen == nil || *v.SealedStoresOpen || v.SealedKeyMissing {
			t.Fatalf("состояние: %+v", v)
		}
	})
	t.Run("зашифрованных нет", func(t *testing.T) {
		f := newBackupFixture(t)
		f.backupSmall(t)
		if err := runBackupVerify(context.Background(), f.verifyOpts(&bytes.Buffer{})); err != nil {
			t.Fatal(err)
		}
		v := f.status(t).Verify
		if v.SealedStoresOpen != nil || v.SealedKeyMissing || v.SealedStoresNote != "" {
			t.Fatalf("состояние: %+v", v)
		}
		if strings.Contains(statusBody(t, f), "sealed_stores") {
			t.Fatal("поле sealed_stores_* без зашифрованных хранилищ")
		}
	})
}

// v0.62 (BACK-04): манифест архива с зашифрованными хранилищами честно
// говорит, что без прежнего revive.key они не откроются, -- а не только про
// пароли роутеров. Без зашифрованных хранилищ этой строки нет.
func TestBackupManifestNamesSealedStores(t *testing.T) {
	setIncludeReviveKey(t, false)
	f := newBackupFixture(t)
	f.sealFixtureStores(t)
	f.backupSmall(t)
	manifest := string(tarMember(t, f.decryptArchive(t, fixtureSmallName), "manifest.txt"))
	for _, want := range []string{
		"sealed_stores=amnezia-premium.json,amnezia-selfhosted.json,awg3-panels.json,hidemyname.json\n",
		"sealed_stores_note=без прежнего revive.key не откроются: ключи кабинетов, панелей VPS, своих VPS и коды HideMy\n",
		"revive_key_note=" + reviveKeyNotInBackupNote + "\n",
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("в манифесте нет %q:\n%s", want, manifest)
		}
	}

	plain := newBackupFixture(t)
	plain.backupSmall(t)
	manifest = string(tarMember(t, plain.decryptArchive(t, fixtureSmallName), "manifest.txt"))
	if !strings.Contains(manifest, "sealed_stores=\n") || !strings.Contains(manifest, "sealed_stores_note=\n") {
		t.Fatalf("манифест без зашифрованных хранилищ:\n%s", manifest)
	}
}

// v0.62 (BACK-04): прогон бэкапа и проверка пишут в файл состояния, едет ли
// ключ шифрования в архив, -- карточка «Бэкенд» напоминает сохранить его
// отдельно, пока он не в бэкапе.
func TestBackupRecordsReviveKeyInBackup(t *testing.T) {
	for _, in := range []bool{false, true} {
		setIncludeReviveKey(t, in)
		f := newBackupFixture(t)
		writeBackupStatusFixture(t, f, !in)
		f.backupSmall(t)
		if got := f.status(t).ReviveKeyInBackup; got != in {
			t.Fatalf("после бэкапа revive_key_in_backup=%v, ждали %v", got, in)
		}
		writeBackupStatusFixture(t, f, !in)
		if err := runBackupVerify(context.Background(), f.verifyOpts(&bytes.Buffer{})); err != nil {
			t.Fatal(err)
		}
		if got := f.status(t).ReviveKeyInBackup; got != in {
			t.Fatalf("после проверки revive_key_in_backup=%v, ждали %v", got, in)
		}
	}
}

func writeBackupStatusFixture(t *testing.T, f *backupFixture, keyIn bool) {
	t.Helper()
	if err := backup.UpdateStatus(f.statusFilePath(), func(s *backup.Status) { s.ReviveKeyInBackup = keyIn }); err != nil {
		t.Fatal(err)
	}
}

// v0.62 (ревью): хранилище повреждено (ключ подходит, внутри не JSON) --
// это не беда ключа: sealed_stores_open не ставится, ставится
// sealed_store_damaged; провал проверки говорит «не JSON».
func TestBackupVerifyDamagedSealedStoreIsNotKeyProblem(t *testing.T) {
	setIncludeReviveKey(t, false)
	f := newBackupFixture(t)
	mustWrite(t, filepath.Join(f.dir, "data", "awg3-panels.json"), `{"panels":[`)
	f.sealFixtureStores(t)
	f.backupSmall(t)
	f.verifyFails(t, "хранилище awg3-panels.json из архива -- не JSON после расшифровки")
	v := f.status(t).Verify
	if v.SealedStoresOpen != nil || v.SealedKeyMissing || !v.SealedStoreDamaged {
		t.Fatalf("состояние: %+v", v)
	}
}
