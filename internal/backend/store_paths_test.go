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

func skipIfRootOrWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("права каталога не ограничивают root и Windows")
	}
}

// Новый каталог не принимает запись: перенос не удался -- предупреждение,
// старый файл цел, старт продолжается.
func TestMigrateLegacyStoresFailureKeepsOldFile(t *testing.T) {
	skipIfRootOrWindows(t)
	legacy, data, stores, logs, logger := migrateEnv(t)
	if err := os.WriteFile(filepath.Join(legacy, "hidemyname.json"), []byte(`{"code":"SECRET-CODE"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(data, 0o500); err != nil { // #nosec G302 -- тест: каталог без права записи
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(data, 0o700) }) // #nosec G302 -- вернуть права, чтобы TempDir убрался
	if moved := MigrateLegacyStores(legacy, stores, logger); len(moved) != 0 {
		t.Fatalf("moved = %v", moved)
	}
	if body, err := os.ReadFile(filepath.Join(legacy, "hidemyname.json")); err != nil || !strings.Contains(string(body), "SECRET-CODE") {
		t.Fatalf("старый файл пострадал: %q %v", body, err)
	}
	out := logs.String()
	if strings.Count(out, "level=WARN") != 1 || !strings.Contains(out, "хранилище не перенесено") || !strings.Contains(out, "hidemyname.json") {
		t.Fatalf("журнал: %s", out)
	}
	if strings.Contains(out, "level=INFO") || strings.Contains(out, "SECRET-CODE") {
		t.Fatalf("журнал: %s", out)
	}
}

// Файл переехал, а старую копию убрать не дали: это «перенесено» плюс
// отдельное предупреждение, а не «не перенесено».
func TestMigrateLegacyStoresOldCopyNotRemoved(t *testing.T) {
	skipIfRootOrWindows(t)
	legacy, data, stores, logs, logger := migrateEnv(t)
	if err := os.WriteFile(filepath.Join(legacy, "hidemyname.json"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(legacy, 0o500); err != nil { // #nosec G302 -- тест: из каталога нельзя удалять
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(legacy, 0o700) }) // #nosec G302 -- вернуть права, чтобы TempDir убрался
	moved := MigrateLegacyStores(legacy, stores, logger)
	if len(moved) != 1 || moved[0] != "hidemyname.json" {
		t.Fatalf("moved = %v", moved)
	}
	if body, _ := os.ReadFile(filepath.Join(data, "hidemyname.json")); string(body) != "old" {
		t.Fatalf("новый файл: %q", body)
	}
	out := logs.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "хранилище перенесено") {
		t.Fatalf("нет записи о переносе: %s", out)
	}
	if strings.Count(out, "level=WARN") != 1 || !strings.Contains(out, "старая копия не удалена") {
		t.Fatalf("нет предупреждения о старой копии: %s", out)
	}
	if strings.Contains(out, "не перенесено") {
		t.Fatalf("удавшийся перенос назван неудачей: %s", out)
	}
	// Следующий старт: новый файл на месте, старый не трогаем и не шумим.
	logs.Reset()
	if again := MigrateLegacyStores(legacy, stores, logger); len(again) != 0 || logs.Len() != 0 {
		t.Fatalf("повтор: %v %s", again, logs.String())
	}
}

// Падение посреди переноса оставляет <имя>.migrate-* с секретами.
func TestMigrateLegacyStoresRemovesStaleTemps(t *testing.T) {
	legacy, data, stores, logs, logger := migrateEnv(t)
	stale := []string{"amnezia-premium.json.migrate-123456", "awg3-panels.json.migrate-9"}
	for _, n := range stale {
		if err := os.WriteFile(filepath.Join(data, n), []byte("vpn://LEFTOVER"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keep := []string{"state.db", "amnezia-premium.json", "other.json.migrate-1", "amnezia-premium.json.tmp"}
	for _, n := range keep {
		if err := os.WriteFile(filepath.Join(data, n), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(data, "hidemyname.json.migrate-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	MigrateLegacyStores(legacy, stores, logger)
	for _, n := range stale {
		if _, err := os.Lstat(filepath.Join(data, n)); !os.IsNotExist(err) {
			t.Fatalf("остаток %s не убран: %v", n, err)
		}
	}
	for _, n := range append(keep, "hidemyname.json.migrate-dir") {
		if _, err := os.Lstat(filepath.Join(data, n)); err != nil {
			t.Fatalf("%s тронут: %v", n, err)
		}
	}
	if strings.Contains(logs.String(), "LEFTOVER") {
		t.Fatalf("содержимое в журнале: %s", logs.String())
	}
}

func TestStoreDefaultsWithRelativeDBPath(t *testing.T) {
	for db, dir := range map[string]string{"state.db": ".", "data/state.db": "data", "./data/state.db": "data"} {
		cfg := &Config{DBPath: db}
		ApplyStoreDefaults(cfg)
		for _, s := range cfg.StoreFiles() {
			if want := filepath.Join(dir, s.Name); s.Path != want || !s.Defaulted {
				t.Fatalf("db_path %q: %+v, ждали %q", db, s, want)
			}
		}
		logs := &bytes.Buffer{}
		WarnStoresOutsideDBDir(cfg, slog.New(slog.NewTextHandler(logs, nil)))
		if logs.Len() != 0 {
			t.Fatalf("db_path %q: лишнее предупреждение: %s", db, logs.String())
		}
	}
}

func TestMigrateLegacyStoresWithRelativeDBPath(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "var-lib")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "hidemyname.json"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	cfg := &Config{DBPath: "data/state.db"} // каталога data ещё нет
	ApplyStoreDefaults(cfg)
	moved := MigrateLegacyStores(legacy, cfg.StoreFiles(), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if len(moved) != 1 {
		t.Fatalf("moved = %v", moved)
	}
	if body, err := os.ReadFile(filepath.Join(root, "data", "hidemyname.json")); err != nil || string(body) != "old" {
		t.Fatalf("%q %v", body, err)
	}
}

func TestWarnStoresOutsideDBDir(t *testing.T) {
	cfg := loadStoreCfg(t, "amnezia_premium:\n  secrets_path: /data/keys.json\namnezia_selfhosted:\n  store_path: /var/lib/wg-monitor/amnezia-selfhosted.json\n")
	logs := &bytes.Buffer{}
	WarnStoresOutsideDBDir(cfg, slog.New(slog.NewTextHandler(logs, nil)))
	out := logs.String()
	// Свои серверы и панели (едут за ними) -- вне /data; ключи Amnezia заданы
	// явно, но внутри /data; HideMy -- по умолчанию.
	if strings.Count(out, "level=WARN") != 2 {
		t.Fatalf("журнал: %s", out)
	}
	for _, want := range []string{
		"хранилище вне каталога базы — при пересоздании контейнера оно пропадёт",
		"path=/var/lib/wg-monitor/amnezia-selfhosted.json",
		"path=/var/lib/wg-monitor/awg3-panels.json",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("нет %q в журнале: %s", want, out)
		}
	}
	if strings.Contains(out, "keys.json") || strings.Contains(out, "hidemyname") {
		t.Fatalf("лишнее предупреждение: %s", out)
	}

	logs.Reset()
	WarnStoresOutsideDBDir(loadStoreCfg(t, ""), slog.New(slog.NewTextHandler(logs, nil)))
	if logs.Len() != 0 {
		t.Fatalf("умолчания не должны предупреждать: %s", logs.String())
	}
}
