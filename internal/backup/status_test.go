package backup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusUpdatePreservesOtherSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), StatusFileName)
	if _, err := LoadStatus(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("нет файла: ждали os.ErrNotExist, получили %v", err)
	}
	if err := UpdateStatus(path, func(s *Status) {
		s.Small = KindStatus{LastOKAt: "2026-10-02T02:00:05Z", LastRunAt: "2026-10-02T02:00:05Z", OK: true, SizeBytes: 4_000_000,
			File: "wg-monitor-small-backup-20261002T020000Z.tgz.enc", Telegram: DeliveryOK}
	}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateStatus(path, func(s *Status) {
		s.Full = KindStatus{LastRunAt: "2026-10-02T02:10:00Z", OK: false, Telegram: DeliveryOff, Offsite: DeliveryError, Error: "сервер не ответил"}
	}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateStatus(path, func(s *Status) {
		s.Verify = VerifyStatus{LastRunAt: "2026-10-04T03:30:00Z", OK: true, Routers: 12}
	}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || !got.Small.OK || got.Small.SizeBytes != 4_000_000 || got.Full.Offsite != DeliveryError || got.Verify.Routers != 12 {
		t.Fatalf("секции потеряны: %+v", got)
	}

	// Точная форма файла -- контракт для бэкенда и мини-аппа.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]map[string]any
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	if string(top["version"]) != "1" {
		t.Fatalf("version: %s", top["version"])
	}
	delete(top, "version")
	doc = map[string]map[string]any{}
	for k, v := range top {
		var m map[string]any
		if err := json.Unmarshal(v, &m); err != nil {
			t.Fatal(err)
		}
		doc[k] = m
	}
	wantKeys := map[string][]string{
		"small":  {"last_ok_at", "last_run_at", "ok", "size_bytes", "file", "telegram", "error"},
		"full":   {"last_ok_at", "last_run_at", "ok", "size_bytes", "file", "telegram", "offsite", "error"},
		"verify": {"last_run_at", "ok", "error", "routers"},
	}
	if len(doc) != len(wantKeys) {
		t.Fatalf("секции: %v", doc)
	}
	for section, keys := range wantKeys {
		if len(doc[section]) != len(keys) {
			t.Errorf("секция %s: поля %v, ждали %v", section, doc[section], keys)
		}
		for _, k := range keys {
			if _, ok := doc[section][k]; !ok {
				t.Errorf("секция %s без поля %s", section, k)
			}
		}
	}
	if doc["full"]["last_ok_at"] != "" {
		t.Errorf("last_ok_at полного должен быть пустой строкой: %v", doc["full"]["last_ok_at"])
	}
}

func TestStatusUpdateIsAtomicAndSurvivesGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, StatusFileName)
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStatus(path); err == nil {
		t.Fatal("битый файл прочитан без ошибки")
	}
	// Битый файл не мешает записать новое состояние.
	if err := UpdateStatus(path, func(s *Status) { s.Verify.OK = true }); err != nil {
		t.Fatal(err)
	}
	got, err := LoadStatus(path)
	if err != nil || !got.Verify.OK {
		t.Fatalf("после перезаписи: %+v %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != StatusFileName {
			t.Fatalf("остался временный файл %s", e.Name())
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Файл читает бэкенд из контейнера под другим пользователем; секретов в нём нет.
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("права файла состояния %o, ждали 644", info.Mode().Perm())
	}
	// Каталога нет -- ошибка, а не паника и не файл в другом месте.
	if err := UpdateStatus(filepath.Join(dir, "missing", StatusFileName), func(*Status) {}); err == nil {
		t.Fatal("запись в несуществующий каталог прошла")
	}
}

func TestStatusErrorTextIsShortAndSingleLine(t *testing.T) {
	long := strings.Repeat("очень длинная ошибка ", 100) + "\nвторая строка"
	got := StatusErrorText(long)
	if len([]rune(got)) > MaxStatusErrorRunes || strings.ContainsAny(got, "\n\r\t") {
		t.Fatalf("текст ошибки не укорочен: %d рун, %q", len([]rune(got)), got)
	}
	if StatusErrorText("  обычная ошибка \n") != "обычная ошибка" {
		t.Fatalf("короткий текст испорчен: %q", StatusErrorText("  обычная ошибка \n"))
	}
}
