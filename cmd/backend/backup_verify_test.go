package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backup"
)

func (f *backupFixture) verifyOpts(out *bytes.Buffer) backupVerifyOptions {
	return backupVerifyOptions{
		ConfigPath:     f.cfgPath,
		PassphraseFile: f.passPath,
		OutDir:         f.outDir,
		now:            func() time.Time { return f.now.Add(90 * time.Minute) },
		stdout:         out,
	}
}

func (f *backupFixture) backupSmall(t *testing.T) {
	t.Helper()
	if err := runBackup(context.Background(), f.opts("small")); err != nil {
		t.Fatal(err)
	}
}

// verifyFails прогоняет проверку, ждёт провал с текстом want и проверяет,
// что он записан в состояние, а временный каталог убран.
func (f *backupFixture) verifyFails(t *testing.T, want string) {
	t.Helper()
	err := runBackupVerify(context.Background(), f.verifyOpts(&bytes.Buffer{}))
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("ждали провал с %q, получили %v", want, err)
	}
	s := f.status(t)
	if s.Verify.OK || !strings.Contains(s.Verify.Error, want) || s.Verify.LastRunAt != "2026-10-07T03:30:00Z" {
		t.Fatalf("состояние проверки: %+v", s.Verify)
	}
	if strings.Contains(s.Verify.Error, fixturePassphrase) || strings.Contains(err.Error(), fixturePassphrase) {
		t.Fatal("парольная фраза в тексте ошибки")
	}
	f.noVerifyDebris(t)
}

func (f *backupFixture) noVerifyDebris(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(f.outDir); errors.Is(err, os.ErrNotExist) {
		return
	}
	for _, n := range f.outNames(t) {
		if strings.HasPrefix(n, ".tmp-") {
			t.Fatalf("временный каталог проверки не убран: %s", n)
		}
	}
}

func TestBackupVerifyPassesOnFreshSmallArchive(t *testing.T) {
	f := newBackupFixture(t)
	f.backupSmall(t)
	smallBefore := f.status(t).Small
	var out bytes.Buffer
	if err := runBackupVerify(context.Background(), f.verifyOpts(&out)); err != nil {
		t.Fatal(err)
	}
	s := f.status(t)
	if want := (backup.VerifyStatus{LastRunAt: "2026-10-07T03:30:00Z", OK: true, Routers: 3}); s.Verify != want {
		t.Fatalf("состояние проверки: %+v", s.Verify)
	}
	if s.Small != smallBefore {
		t.Fatalf("проверка тронула секцию малого: %+v", s.Small)
	}
	for _, want := range []string{fixtureSmallName, "роутеров 3", "владельцев 2", "операторов 2", "экран пуст до первого отчёта агентов"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("в выводе проверки нет %q:\n%s", want, out.String())
		}
	}
	f.noVerifyDebris(t)
	if got := f.outNames(t); !slices.Equal(got, []string{fixtureSmallName}) {
		t.Fatalf("проверка оставила лишнее: %v", got)
	}
}

func TestBackupVerifyUsesLatestSmallArchive(t *testing.T) {
	f := newBackupFixture(t)
	f.backupSmall(t)
	// Старый битый малый и свежий полный мусорный: брать надо свежий малый.
	mustWrite(t, filepath.Join(f.outDir, "wg-monitor-small-backup-20261001T020000Z.tgz.enc"), "garbage")
	mustWrite(t, filepath.Join(f.outDir, "wg-monitor-full-backup-20261101T020000Z.tgz.enc"), "garbage")
	if err := runBackupVerify(context.Background(), f.verifyOpts(&bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
	// А если свежий малый битый -- провал, старый целый не подменяет его.
	mustWrite(t, filepath.Join(f.outDir, "wg-monitor-small-backup-20261008T020000Z.tgz.enc"), "garbage")
	f.verifyFails(t, "архив не разворачивается")
}

func TestBackupVerifyFailsWithoutArchive(t *testing.T) {
	f := newBackupFixture(t)
	if err := os.MkdirAll(f.outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.verifyFails(t, "малого архива ещё нет")
}

// Проверка сверяет базу из архива со счётчиками, записанными в манифест в
// момент бэкапа, а не с живой базой: роутер, добавленный между ночным
// бэкапом и недельной проверкой, проверку не валит.
func TestBackupVerifyIgnoresChangesInLiveDBAfterBackup(t *testing.T) {
	f := newBackupFixture(t)
	f.backupSmall(t)
	f.exec(t, `INSERT INTO users (nickname, token_hash, expected_exit_ip, awg_iface, telegram_user_id) VALUES ('delta', 'h4', '198.51.100.4', 'nwg0', 1003)`)
	f.exec(t, `INSERT INTO router_operators (user_id, telegram_user_id, granted_by) VALUES (3, 2003, 1002)`)
	var out bytes.Buffer
	if err := runBackupVerify(context.Background(), f.verifyOpts(&out)); err != nil {
		t.Fatalf("роутер, добавленный после бэкапа, провалил проверку: %v", err)
	}
	if got := f.status(t).Verify; !got.OK || got.Routers != 3 {
		t.Fatalf("состояние проверки: %+v", got)
	}
	f.exec(t, `DELETE FROM users WHERE nickname IN ('alpha', 'delta')`)
	if err := runBackupVerify(context.Background(), f.verifyOpts(&out)); err != nil {
		t.Fatalf("удалённый после бэкапа роутер провалил проверку: %v", err)
	}
}

// rewriteLatestSmall разворачивает свежий малый архив, даёт mutate испортить
// файлы и пишет результат новым, ещё более свежим малым архивом.
func (f *backupFixture) rewriteLatestSmall(t *testing.T, mutate func(dir string)) {
	t.Helper()
	dir := t.TempDir()
	names, err := extractEncryptedArchive(context.Background(), filepath.Join(f.outDir, fixtureSmallName), []byte(fixturePassphrase), dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	mutate(dir)
	var members []archiveMember
	for _, n := range names {
		members = append(members, archiveMember{n, filepath.Join(dir, n)})
	}
	dst := filepath.Join(f.outDir, "wg-monitor-small-backup-20261008T020000Z.tgz.enc")
	if err := writeEncryptedArchive(context.Background(), dst, members, []byte(fixturePassphrase), backup.TestParams()); err != nil {
		t.Fatal(err)
	}
}

func TestBackupVerifyDetectsArchiveDifferingFromManifestCounts(t *testing.T) {
	f := newBackupFixture(t)
	f.backupSmall(t)
	f.rewriteLatestSmall(t, func(dir string) {
		d, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if _, err := d.Exec(`DELETE FROM router_operators WHERE telegram_user_id = 2002`); err != nil {
			t.Fatal(err)
		}
	})
	f.verifyFails(t, "операторов 1, в манифесте архива 2")
	if got := f.status(t).Verify.Routers; got != 3 {
		t.Fatalf("routers в состоянии: %d", got)
	}
}

func TestBackupVerifyFailsWhenManifestHasNoCounts(t *testing.T) {
	f := newBackupFixture(t)
	f.backupSmall(t)
	f.rewriteLatestSmall(t, func(dir string) {
		mustWrite(t, filepath.Join(dir, "manifest.txt"), "kind=small\n")
	})
	f.verifyFails(t, "в манифесте архива нет счётчиков")
}

func TestBackupVerifyFailsOnWrongPassphraseAndTruncatedArchive(t *testing.T) {
	f := newBackupFixture(t)
	f.backupSmall(t)
	mustWrite(t, f.passPath, "another passphrase\n")
	f.verifyFails(t, "архив не разворачивается")
	mustWrite(t, f.passPath, fixturePassphrase+"\n")

	path := filepath.Join(f.outDir, fixtureSmallName)
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Отрезан хвост: tar и gzip могли бы этого не заметить, шифр обязан.
	mustWrite(t, path, string(blob[:len(blob)-1]))
	f.verifyFails(t, "архив не разворачивается")
	mustWrite(t, path, string(blob)+"x")
	f.verifyFails(t, "архив не разворачивается")
	mustWrite(t, path, string(blob))
	if err := runBackupVerify(context.Background(), f.verifyOpts(&bytes.Buffer{})); err != nil {
		t.Fatalf("целый архив после восстановления файла: %v", err)
	}
	if !f.status(t).Verify.OK {
		t.Fatal("удачная проверка не сняла прошлую ошибку")
	}
}

func TestBackupVerifyChecksStores(t *testing.T) {
	f := newBackupFixture(t)
	store := filepath.Join(f.dir, "data", "awg3-panels.json")
	mustWrite(t, store, `{"panels":[`)
	f.backupSmall(t)
	f.verifyFails(t, "хранилище awg3-panels.json из архива -- не JSON")

	// Хранилище появилось после бэкапа: в архиве его нет -- провал.
	f2 := newBackupFixture(t)
	hidemy := filepath.Join(f2.dir, "data", "hidemyname.json")
	if err := os.Remove(hidemy); err != nil {
		t.Fatal(err)
	}
	f2.backupSmall(t)
	if err := runBackupVerify(context.Background(), f2.verifyOpts(&bytes.Buffer{})); err != nil {
		t.Fatalf("хранилища нет нигде -- это не ошибка: %v", err)
	}
	mustWrite(t, hidemy, `{}`)
	f2.verifyFails(t, "в архиве нет хранилища hidemyname.json")
}

// По правилу ключа оживления в бэкапе нет: проверка его не требует, а в
// выводе прямо сказано, что пароли роутеров после восстановления вводятся
// заново.
func TestBackupVerifyDoesNotRequireReviveKey(t *testing.T) {
	setIncludeReviveKey(t, false)
	f := newBackupFixture(t) // пароль роутера в базе есть, ключ на диске есть
	f.backupSmall(t)
	var out bytes.Buffer
	if err := runBackupVerify(context.Background(), f.verifyOpts(&out)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ключа оживления в бэкапе нет", "пароли роутеров", "вводятся заново"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("в выводе проверки нет %q:\n%s", want, out.String())
		}
	}
	// И без ключа на диске тоже.
	f.writeConfig(t, false)
	if err := runBackupVerify(context.Background(), f.verifyOpts(&bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
}

func TestBackupVerifyChecksReviveKeyWhenIncluded(t *testing.T) {
	setIncludeReviveKey(t, true)
	t.Run("ключ не тот, которым сохранены пароли", func(t *testing.T) {
		f := newBackupFixture(t)
		mustWrite(t, f.keyPath, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x11}, 32))+"\n")
		f.backupSmall(t)
		f.verifyFails(t, "не расшифровывает ни один из 1 сохранённых паролей")
	})
	t.Run("хватает одного расшифрованного пароля", func(t *testing.T) {
		f := newBackupFixture(t)
		f.saveCredential(t, bytes.Repeat([]byte{0x22}, 32), 2) // сохранён давно другим ключом
		f.backupSmall(t)
		if err := runBackupVerify(context.Background(), f.verifyOpts(&bytes.Buffer{})); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("ключ испорчен", func(t *testing.T) {
		f := newBackupFixture(t)
		mustWrite(t, f.keyPath, "not-base64-at-all!\n")
		f.backupSmall(t)
		f.verifyFails(t, "ключ оживления из архива негоден")
	})
	t.Run("пароли есть, ключа в архиве нет", func(t *testing.T) {
		f := newBackupFixture(t)
		f.writeConfig(t, false)
		f.backupSmall(t)
		f.verifyFails(t, "нет ключа revive.key")
	})
	t.Run("ключ появился после бэкапа", func(t *testing.T) {
		f := newBackupFixture(t)
		f.exec(t, `DELETE FROM router_credentials`)
		key, _ := os.ReadFile(f.keyPath)
		if err := os.Remove(f.keyPath); err != nil {
			t.Fatal(err)
		}
		f.backupSmall(t)
		if err := runBackupVerify(context.Background(), f.verifyOpts(&bytes.Buffer{})); err != nil {
			t.Fatalf("ни ключа, ни паролей -- это не ошибка: %v", err)
		}
		mustWrite(t, f.keyPath, string(key))
		f.verifyFails(t, "в архиве нет ключа оживления")
	})
	t.Run("ключ есть, паролей нет", func(t *testing.T) {
		f := newBackupFixture(t)
		f.exec(t, `DELETE FROM router_credentials`)
		f.backupSmall(t)
		if err := runBackupVerify(context.Background(), f.verifyOpts(&bytes.Buffer{})); err != nil {
			t.Fatal(err)
		}
	})
}

func TestBackupVerifyRejectsHostileArchiveMembers(t *testing.T) {
	f := newBackupFixture(t)
	if err := os.MkdirAll(f.outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	evil := filepath.Join(f.dir, "evil.txt")
	mustWrite(t, evil, "x")
	for _, name := range []string{"../escape.txt", "sub/state.db", "/abs.txt", ".hidden"} {
		path := filepath.Join(f.outDir, fixtureSmallName)
		_ = os.Remove(path)
		if err := writeEncryptedArchive(context.Background(), path, []archiveMember{{name, evil}}, []byte(fixturePassphrase), backup.TestParams()); err != nil {
			t.Fatal(err)
		}
		f.verifyFails(t, "suspect path")
		if _, err := os.Stat(filepath.Join(f.dir, "data", "escape.txt")); err == nil {
			t.Fatal("файл из архива вышел за временный каталог")
		}
	}
}

func TestBackupExtractCommand(t *testing.T) {
	f := newBackupFixture(t)
	f.backupSmall(t)
	to := filepath.Join(f.dir, "restored")
	args := []string{"extract", "--archive", filepath.Join(f.outDir, fixtureSmallName), "--passphrase-file", f.passPath, "--to", to}
	if err := runBackupCommand(args); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.db", "backend.yaml", "awg3-panels.json", "manifest.txt"} {
		info, err := os.Stat(filepath.Join(to, name))
		if err != nil {
			t.Fatalf("%s не развёрнут: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("права %s: %o", name, info.Mode().Perm())
		}
	}
	// В непустой каталог не разворачиваем.
	if err := runBackupCommand(args); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("повторная распаковка в тот же каталог: %v", err)
	}
	// Битый архив не оставляет половины файлов.
	blob, _ := os.ReadFile(filepath.Join(f.outDir, fixtureSmallName))
	broken := filepath.Join(f.dir, "broken.tgz.enc")
	mustWrite(t, broken, string(blob[:len(blob)-20]))
	to2 := filepath.Join(f.dir, "restored2")
	if err := runBackupCommand([]string{"extract", "--archive", broken, "--passphrase-file", f.passPath, "--to", to2}); err == nil {
		t.Fatal("обрезанный архив развёрнут без ошибки")
	}
	if entries, _ := os.ReadDir(to2); len(entries) != 0 {
		t.Fatalf("после провала остались файлы: %v", entries)
	}
	if err := runBackupCommand([]string{"extract", "--archive", broken}); err == nil {
		t.Fatal("extract без обязательных флагов принят")
	}
}

func TestBackupVerifyCommandFlags(t *testing.T) {
	f := newBackupFixture(t)
	f.backupSmall(t)
	if err := runBackupCommand([]string{"verify", "--config", f.cfgPath, "--passphrase-file", f.passPath, "--out-dir", f.outDir}); err != nil {
		t.Fatal(err)
	}
	if !f.status(t).Verify.OK {
		t.Fatal("verify через командную строку не записал состояние")
	}
	if err := runBackupCommand([]string{"verify", "--config", f.cfgPath, "--out-dir", f.outDir}); err == nil {
		t.Fatal("verify без парольной фразы принят")
	}
	if err := runBackupCommand([]string{"verify", "--config", f.cfgPath, "--passphrase-file", f.passPath, "--out-dir", f.outDir, "extra"}); err == nil {
		t.Fatal("лишний аргумент принят")
	}
}
