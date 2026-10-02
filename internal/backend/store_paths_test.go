package backend

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func loadStoreCfg(t *testing.T, extra string) *Config {
	t.Helper()
	dir := t.TempDir()
	tokPath := writeFile(t, dir, "tok", "secret-bot-token-xyz")
	cfgPath := writeFile(t, dir, "c.yaml", "db_path: /data/state.db\ntelegram:\n  bot_token_file: "+tokPath+"\n  admin_user_id: 42\n"+extra)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func storeByName(t *testing.T, cfg *Config, name string) StoreFile {
	t.Helper()
	for _, s := range cfg.StoreFiles() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("хранилища %q нет в %+v", name, cfg.StoreFiles())
	return StoreFile{}
}

func TestStoreDefaultsLiveNextToDB(t *testing.T) {
	cfg := loadStoreCfg(t, "")
	if cfg.Amnezia.SecretsPath != "/data/amnezia-premium.json" {
		t.Fatalf("amnezia: %q", cfg.Amnezia.SecretsPath)
	}
	if cfg.SelfHostedAmnezia.StorePath != "/data/amnezia-selfhosted.json" {
		t.Fatalf("self-hosted: %q", cfg.SelfHostedAmnezia.StorePath)
	}
	if cfg.HideMy.SecretsPath != "/data/hidemyname.json" {
		t.Fatalf("hidemy: %q", cfg.HideMy.SecretsPath)
	}
	want := map[string]string{
		"amnezia-premium.json":    "/data/amnezia-premium.json",
		"amnezia-selfhosted.json": "/data/amnezia-selfhosted.json",
		"awg3-panels.json":        "/data/awg3-panels.json",
		"hidemyname.json":         "/data/hidemyname.json",
	}
	got := cfg.StoreFiles()
	if len(got) != len(want) {
		t.Fatalf("StoreFiles = %+v", got)
	}
	for _, s := range got {
		if want[s.Name] != s.Path || !s.Defaulted {
			t.Fatalf("хранилище %+v, ждали путь %q по умолчанию", s, want[s.Name])
		}
	}
}

func TestStoreExplicitPathsAreKept(t *testing.T) {
	cfg := loadStoreCfg(t, "amnezia_premium:\n  secrets_path: /secrets/a.json\namnezia_selfhosted:\n  store_path: /srv/vps/own.json\nhidemyname:\n  secrets_path: /secrets/h.json\n")
	if cfg.Amnezia.SecretsPath != "/secrets/a.json" || cfg.SelfHostedAmnezia.StorePath != "/srv/vps/own.json" || cfg.HideMy.SecretsPath != "/secrets/h.json" {
		t.Fatalf("явные пути потеряны: %q %q %q", cfg.Amnezia.SecretsPath, cfg.SelfHostedAmnezia.StorePath, cfg.HideMy.SecretsPath)
	}
	for _, s := range cfg.StoreFiles() {
		if s.Defaulted {
			t.Fatalf("явно заданное хранилище помечено как умолчание: %+v", s)
		}
	}
	// Панели живут рядом с файлом своих серверов -- и едут за ним.
	if got := storeByName(t, cfg, "awg3-panels.json").Path; got != "/srv/vps/awg3-panels.json" {
		t.Fatalf("awg3: %q", got)
	}
	// Имя в архиве -- каноническое, каким бы ни был явный путь.
	if got := storeByName(t, cfg, "amnezia-premium.json").Path; got != "/secrets/a.json" {
		t.Fatalf("amnezia: %q", got)
	}
}

func TestApplyStoreDefaultsIsIdempotent(t *testing.T) {
	cfg := &Config{DBPath: "/data/state.db"}
	ApplyStoreDefaults(cfg)
	ApplyStoreDefaults(cfg)
	for _, s := range cfg.StoreFiles() {
		if !s.Defaulted || filepath.Dir(s.Path) != "/data" {
			t.Fatalf("после повторного вызова: %+v", s)
		}
	}
}

func migrateEnv(t *testing.T) (legacy, data string, stores []StoreFile, logs *bytes.Buffer, logger *slog.Logger) {
	t.Helper()
	root := t.TempDir()
	legacy = filepath.Join(root, "var-lib")
	data = filepath.Join(root, "data")
	for _, d := range []string{legacy, data} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &Config{DBPath: filepath.Join(data, "state.db")}
	ApplyStoreDefaults(cfg)
	logs = &bytes.Buffer{}
	return legacy, data, cfg.StoreFiles(), logs, slog.New(slog.NewTextHandler(logs, nil))
}

func TestMigrateLegacyStoresMovesFiles(t *testing.T) {
	legacy, data, stores, logs, logger := migrateEnv(t)
	secret := `{"keys":["vpn://TOP-SECRET-KEY"]}`
	if err := os.WriteFile(filepath.Join(legacy, "amnezia-premium.json"), []byte(secret), 0o644); err != nil { // #nosec G306 -- тест: права обязаны ужаться до 0600
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "awg3-panels.json"), []byte(`{"panels":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	moved := MigrateLegacyStores(legacy, stores, logger)
	if len(moved) != 2 {
		t.Fatalf("moved = %v", moved)
	}
	body, err := os.ReadFile(filepath.Join(data, "amnezia-premium.json"))
	if err != nil || string(body) != secret {
		t.Fatalf("файл не переехал: %q %v", body, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(data, "amnezia-premium.json"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("права %v %v", info.Mode().Perm(), err)
		}
	}
	if _, err := os.Stat(filepath.Join(legacy, "amnezia-premium.json")); !os.IsNotExist(err) {
		t.Fatalf("старый файл остался: %v", err)
	}
	for _, name := range []string{"amnezia-selfhosted.json", "hidemyname.json"} {
		if _, err := os.Stat(filepath.Join(data, name)); !os.IsNotExist(err) {
			t.Fatalf("%s появился из ничего: %v", name, err)
		}
	}
	out := logs.String()
	if strings.Count(out, "level=INFO") != 2 || !strings.Contains(out, "amnezia-premium.json") || !strings.Contains(out, "awg3-panels.json") {
		t.Fatalf("журнал: %s", out)
	}
	if strings.Contains(out, "TOP-SECRET-KEY") {
		t.Fatalf("содержимое утекло в журнал: %s", out)
	}
	entries, _ := os.ReadDir(data)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") || strings.Contains(e.Name(), ".migrate") {
			t.Fatalf("остался временный файл %s", e.Name())
		}
	}

	// Повторный запуск ничего не делает и ничего не пишет.
	logs.Reset()
	if again := MigrateLegacyStores(legacy, stores, logger); len(again) != 0 {
		t.Fatalf("повторный перенос: %v", again)
	}
	if logs.Len() != 0 {
		t.Fatalf("повторный запуск пишет в журнал: %s", logs.String())
	}
}

func TestMigrateLegacyStoresNeverOverwrites(t *testing.T) {
	legacy, data, stores, _, logger := migrateEnv(t)
	if err := os.WriteFile(filepath.Join(legacy, "hidemyname.json"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "hidemyname.json"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if moved := MigrateLegacyStores(legacy, stores, logger); len(moved) != 0 {
		t.Fatalf("moved = %v", moved)
	}
	body, _ := os.ReadFile(filepath.Join(data, "hidemyname.json"))
	if string(body) != "new" {
		t.Fatalf("новый файл затёрт: %q", body)
	}
	old, _ := os.ReadFile(filepath.Join(legacy, "hidemyname.json"))
	if string(old) != "old" {
		t.Fatalf("старый файл тронут: %q", old)
	}
}

func TestMigrateLegacyStoresSkipsExplicitAndSameDir(t *testing.T) {
	legacy, data, _, _, logger := migrateEnv(t)
	if err := os.WriteFile(filepath.Join(legacy, "hidemyname.json"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Явно заданный путь: файл по старому умолчанию к нему отношения не имеет.
	explicit := []StoreFile{{Name: "hidemyname.json", Path: filepath.Join(data, "hidemyname.json"), Defaulted: false}}
	if moved := MigrateLegacyStores(legacy, explicit, logger); len(moved) != 0 {
		t.Fatalf("moved = %v", moved)
	}
	// База лежит в старом каталоге (раскладка VPS): переносить некуда.
	same := []StoreFile{{Name: "hidemyname.json", Path: filepath.Join(legacy, "hidemyname.json"), Defaulted: true}}
	if moved := MigrateLegacyStores(legacy, same, logger); len(moved) != 0 {
		t.Fatalf("moved = %v", moved)
	}
	if body, _ := os.ReadFile(filepath.Join(legacy, "hidemyname.json")); string(body) != "old" {
		t.Fatalf("файл тронут: %q", body)
	}
}

func TestMigrateLegacyStoresIgnoresNonRegular(t *testing.T) {
	legacy, data, stores, _, logger := migrateEnv(t)
	if err := os.Mkdir(filepath.Join(legacy, "hidemyname.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if moved := MigrateLegacyStores(legacy, stores, logger); len(moved) != 0 {
		t.Fatalf("moved = %v", moved)
	}
	if _, err := os.Stat(filepath.Join(data, "hidemyname.json")); !os.IsNotExist(err) {
		t.Fatalf("каталог перенесён как файл: %v", err)
	}
}
