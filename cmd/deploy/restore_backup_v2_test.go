package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backup"
)

const restoreTestPass = "restore test passphrase"

// writeEncryptedRestoreArchive -- архив в потоковом формате v2, каким его
// пишет `wg-monitor-backend backup` начиная с v0.53.
func writeEncryptedRestoreArchive(t *testing.T, name string, entries []tarEntryForTest) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	var buf bytes.Buffer
	w, err := backup.NewEncryptWriter(&buf, []byte(restoreTestPass), backup.TestParams())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(makeTGZForTestEntries(t, entries)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func smallArchiveEntries(yaml string) []tarEntryForTest {
	return []tarEntryForTest{
		{"state.db", "sqlite bytes"},
		{"backend.yaml", yaml},
		{"bot-token.txt", "bot"},
		{"agents.csv", "nickname,kind,last_seen_at,last_deployed_version\nalpha,static,,v0.53.0\n"},
		{"manifest.txt", "name=wg-monitor-small-backup\nkind=small\ncreated_utc=20261007T020000Z\nbackend_version=v0.53.0\nhost=pi\nformat=encrypted-small-v2\n"},
		{"amnezia-premium.json", `{"keys":["vpn://K"]}`},
		{"amnezia-selfhosted.json", `{"instances":[]}`},
		{"awg3-panels.json", `{"panels":[]}`},
		{"hidemyname.json", `{"codes":[]}`},
		{"revive.key", "WlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlpaWlo=\n"},
		{"some-future-member.bin", "ignored"},
	}
}

func restoreYAMLWithReviveKey() string {
	return validRestoreBackendYAML() + "revive:\n  key_file: /etc/wg-monitor/revive.key\n"
}

func extrasByName(b *RestoreBackup) map[string]string {
	out := map[string]string{}
	for _, e := range b.Extras {
		out[e.Name] = e.RemotePath
	}
	return out
}

func TestInspectRestoreBackupReadsV2SmallArchiveWithStoresAndKey(t *testing.T) {
	archive := writeEncryptedRestoreArchive(t, "wg-monitor-small-backup-20261007T020000Z.tgz.enc", smallArchiveEntries(restoreYAMLWithReviveKey()))

	b, cleanup, err := InspectRestoreBackupWithPassphrase(archive, restoreTestPass)
	if err != nil {
		t.Fatal(err)
	}
	if b.Manifest["kind"] != "small" || len(b.Agents) != 1 {
		t.Fatalf("манифест или агенты не разобраны: %v %v", b.Manifest, b.Agents)
	}
	want := map[string]string{
		"amnezia-premium.json":    "/var/lib/wg-monitor/amnezia-premium.json",
		"amnezia-selfhosted.json": "/var/lib/wg-monitor/amnezia-selfhosted.json",
		"awg3-panels.json":        "/var/lib/wg-monitor/awg3-panels.json",
		"hidemyname.json":         "/var/lib/wg-monitor/hidemyname.json",
		"revive.key":              "/etc/wg-monitor/revive.key",
	}
	got := extrasByName(b)
	if len(got) != len(want) {
		t.Fatalf("восстанавливаемые файлы: %v", got)
	}
	for name, dest := range want {
		if got[name] != dest {
			t.Errorf("%s -> %q, ждали %q", name, got[name], dest)
		}
	}
	for _, e := range b.Extras {
		info, err := os.Stat(e.LocalPath)
		if err != nil {
			t.Fatalf("%s не извлечён: %v", e.Name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("права извлечённого %s: %o", e.Name, info.Mode().Perm())
		}
	}
	if body, _ := os.ReadFile(b.Extras[0].LocalPath); len(body) == 0 {
		t.Fatal("извлечённый файл пуст")
	}
	if len(b.Warnings) != 0 {
		t.Fatalf("предупреждения: %v", b.Warnings)
	}
	preview := RenderRestoreBackupPreview(b)
	for _, wantLine := range []string{"stores: amnezia-premium.json, amnezia-selfhosted.json, awg3-panels.json, hidemyname.json", "revive.key: yes"} {
		if !strings.Contains(preview, wantLine) {
			t.Errorf("в превью нет %q:\n%s", wantLine, preview)
		}
	}
	// Неизвестный файл из архива никуда не попал.
	if _, err := os.Stat(filepath.Join(b.TempDir, "some-future-member.bin")); err == nil {
		t.Fatal("неизвестный файл архива извлечён")
	}
	tmp := b.TempDir
	cleanup()
	if _, err := os.Stat(tmp); err == nil {
		t.Fatal("временный каталог с секретами не убран")
	}
}

func TestInspectRestoreBackupEncryptedNeedsRightPassphrase(t *testing.T) {
	archive := writeEncryptedRestoreArchive(t, "a.tgz.enc", smallArchiveEntries(restoreYAMLWithReviveKey()))
	if _, _, err := InspectRestoreBackup(archive); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("шифрованный архив без пароля: %v", err)
	}
	if _, _, err := InspectRestoreBackupWithPassphrase(archive, "wrong"); err == nil {
		t.Fatal("неверный пароль принят")
	}
	enc, err := isEncryptedBackupFile(archive)
	if err != nil || !enc {
		t.Fatalf("isEncryptedBackupFile: %v %v", enc, err)
	}
	plain := writeRestoreBackupArchive(t, map[string]string{"state.db": "x", "backend.yaml": validRestoreBackendYAML(), "manifest.txt": "name=x\n"})
	if enc, err := isEncryptedBackupFile(plain); err != nil || enc {
		t.Fatalf("обычный tgz принят за шифрованный: %v %v", enc, err)
	}
	// Пароль к нешифрованному архиву не мешает.
	if _, cleanup, err := InspectRestoreBackupWithPassphrase(plain, restoreTestPass); err != nil {
		t.Fatal(err)
	} else {
		cleanup()
	}
}

func TestInspectRestoreBackupRejectsTruncatedV2Archive(t *testing.T) {
	archive := writeEncryptedRestoreArchive(t, "a.tgz.enc", smallArchiveEntries(restoreYAMLWithReviveKey()))
	blob, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{len(blob) - 1, len(blob) - 40, len(blob) / 2} {
		if err := os.WriteFile(archive, blob[:cut], 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := InspectRestoreBackupWithPassphrase(archive, restoreTestPass); err == nil {
			t.Fatalf("архив, обрезанный до %d байт, принят", cut)
		}
	}
	if err := os.WriteFile(archive, append(blob, 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InspectRestoreBackupWithPassphrase(archive, restoreTestPass); err == nil {
		t.Fatal("архив с хвостом принят")
	}
}

func TestInspectRestoreBackupReadsV1EncryptedArchive(t *testing.T) {
	plain := makeTGZForTest(t, map[string]string{
		"state.db":     "sqlite bytes",
		"backend.yaml": validRestoreBackendYAML(),
		"manifest.txt": "name=wg-monitor-full-backup\nformat=encrypted-full-v1\n",
	})
	enc, err := backup.Encrypt(plain, []byte(restoreTestPass), backup.TestParams())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "old.tgz.enc")
	if err := os.WriteFile(path, enc, 0o600); err != nil {
		t.Fatal(err)
	}
	b, cleanup, err := InspectRestoreBackupWithPassphrase(path, restoreTestPass)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(b.Extras) != 0 || b.Manifest["format"] != "encrypted-full-v1" {
		t.Fatalf("старый архив: %+v", b)
	}
	preview := RenderRestoreBackupPreview(b)
	if strings.Contains(preview, "revive.key: yes") || !strings.Contains(preview, "saved router passwords must be entered again") {
		t.Fatalf("превью архива без ключа: %s", preview)
	}
}

func TestInspectRestoreBackupStoreDestinationsFollowConfig(t *testing.T) {
	yaml := restoreYAMLWithReviveKey() +
		"amnezia_selfhosted:\n  store_path: /etc/wg-monitor/own/own-vps.json\n" +
		"hidemyname:\n  secrets_path: /var/lib/wg-monitor/codes.json\n"
	archive := writeEncryptedRestoreArchive(t, "a.tgz.enc", smallArchiveEntries(yaml))
	b, cleanup, err := InspectRestoreBackupWithPassphrase(archive, restoreTestPass)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	got := extrasByName(b)
	for name, dest := range map[string]string{
		"amnezia-premium.json":    "/var/lib/wg-monitor/amnezia-premium.json",
		"amnezia-selfhosted.json": "/etc/wg-monitor/own/own-vps.json",
		"awg3-panels.json":        "/etc/wg-monitor/own/awg3-panels.json", // панели едут за файлом своих серверов
		"hidemyname.json":         "/var/lib/wg-monitor/codes.json",
	} {
		if got[name] != dest {
			t.Errorf("%s -> %q, ждали %q", name, got[name], dest)
		}
	}
}

func TestInspectRestoreBackupRejectsUnsafeDestinations(t *testing.T) {
	for name, extra := range map[string]string{
		"ключ вне каталогов wg-monitor": "revive:\n  key_file: /etc/cron.d/revive.key\n",
		"обход через ..":                "revive:\n  key_file: /etc/wg-monitor/../cron.d/x\n",
		"относительный путь":            "revive:\n  key_file: secrets/revive.key\n",
		"кавычка в пути":                "revive:\n  key_file: \"/etc/wg-monitor/a'b\"\n",
		"пробел в пути":                 "revive:\n  key_file: \"/etc/wg-monitor/a b\"\n",
		"хранилище вне каталогов":       "revive:\n  key_file: /etc/wg-monitor/revive.key\nhidemyname:\n  secrets_path: /root/.ssh/authorized_keys\n",
		"хранилище поверх базы":         "revive:\n  key_file: /etc/wg-monitor/revive.key\nhidemyname:\n  secrets_path: /var/lib/wg-monitor/state.db\n",
		"ключ поверх конфига":           "revive:\n  key_file: /etc/wg-monitor/backend.yaml\n",
	} {
		t.Run(name, func(t *testing.T) {
			archive := writeEncryptedRestoreArchive(t, "a.tgz.enc", smallArchiveEntries(validRestoreBackendYAML()+extra))
			_, _, err := InspectRestoreBackupWithPassphrase(archive, restoreTestPass)
			if err == nil || !strings.Contains(err.Error(), "restore destination") {
				t.Fatalf("небезопасный путь принят: %v", err)
			}
		})
	}
}

func TestInspectRestoreBackupKeyWithoutConfiguredPathIsWarning(t *testing.T) {
	archive := writeEncryptedRestoreArchive(t, "a.tgz.enc", smallArchiveEntries(validRestoreBackendYAML()))
	b, cleanup, err := InspectRestoreBackupWithPassphrase(archive, restoreTestPass)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, ok := extrasByName(b)["revive.key"]; ok {
		t.Fatal("ключ без пути в конфиге не должен никуда класться")
	}
	if len(b.Extras) != 4 {
		t.Fatalf("хранилища должны восстановиться: %v", extrasByName(b))
	}
	if len(b.Warnings) != 1 || !strings.Contains(b.Warnings[0], "revive.key_file") {
		t.Fatalf("предупреждение о ключе: %v", b.Warnings)
	}
}

func TestInspectRestoreBackupRejectsNestedAndDuplicateExtras(t *testing.T) {
	base := smallArchiveEntries(restoreYAMLWithReviveKey())
	nested := append(append([]tarEntryForTest{}, base...), tarEntryForTest{"nested/revive.key", "x"})
	if _, _, err := InspectRestoreBackupWithPassphrase(writeEncryptedRestoreArchive(t, "a.tgz.enc", nested), restoreTestPass); err == nil || !strings.Contains(err.Error(), "unexpected restore member path") {
		t.Fatalf("вложенный revive.key: %v", err)
	}
	dup := append(append([]tarEntryForTest{}, base...), tarEntryForTest{"awg3-panels.json", "{}"})
	if _, _, err := InspectRestoreBackupWithPassphrase(writeEncryptedRestoreArchive(t, "a.tgz.enc", dup), restoreTestPass); err == nil || !strings.Contains(err.Error(), "duplicate restore member") {
		t.Fatalf("повтор хранилища: %v", err)
	}
	big := append(append([]tarEntryForTest{}, base[:len(base)-2]...), tarEntryForTest{"revive.key", strings.Repeat("A", maxRestoreExtraBytes+1)})
	if _, _, err := InspectRestoreBackupWithPassphrase(writeEncryptedRestoreArchive(t, "a.tgz.enc", big), restoreTestPass); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("огромный revive.key: %v", err)
	}
}

func TestBuildRestoreRemoteScriptInstallsStoresAndKey(t *testing.T) {
	extras := []RestoreExtraFile{
		{Name: "awg3-panels.json", RemotePath: "/var/lib/wg-monitor/awg3-panels.json"},
		{Name: "revive.key", RemotePath: "/etc/wg-monitor/revive.key"},
	}
	script := buildRestoreRemoteScript("20261007T020000Z", extras...)
	for _, want := range []string{
		"test -s '/tmp/wg-monitor-restore/awg3-panels.json'",
		"test -s '/tmp/wg-monitor-restore/revive.key'",
		"if [ -f '/var/lib/wg-monitor/awg3-panels.json' ]; then cp -p '/var/lib/wg-monitor/awg3-panels.json' '/var/lib/wg-monitor/awg3-panels.json.bak.20261007T020000Z'; fi",
		"install -m 600 -o wgmonitor -g wgmonitor '/tmp/wg-monitor-restore/awg3-panels.json' '/var/lib/wg-monitor/awg3-panels.json'",
		"install -m 600 -o wgmonitor -g wgmonitor '/tmp/wg-monitor-restore/revive.key' '/etc/wg-monitor/revive.key'",
		"cp -p '/etc/wg-monitor/revive.key.bak.20261007T020000Z' '/etc/wg-monitor/revive.key'",
		"rm -rf /tmp/wg-monitor-restore",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("в скрипте нет %q:\n%s", want, script)
		}
	}
	stop := strings.Index(script, "systemctl stop wg-monitor-backend")
	start := strings.LastIndex(script, "systemctl start wg-monitor-backend")
	preflight := strings.Index(script, "test -s '/tmp/wg-monitor-restore/revive.key'")
	install := strings.Index(script, "install -m 600 -o wgmonitor -g wgmonitor '/tmp/wg-monitor-restore/revive.key'")
	if preflight > stop {
		t.Error("проверка наличия файлов должна идти до остановки бэкенда")
	}
	if install < stop || install > start {
		t.Error("файлы кладутся на место, пока бэкенд остановлен")
	}
	// Без хранилищ скрипт прежний по смыслу и тоже убирает за собой.
	plain := buildRestoreRemoteScript("20261007T020000Z")
	if strings.Contains(plain, "revive.key") || !strings.Contains(plain, "rm -rf /tmp/wg-monitor-restore") {
		t.Errorf("скрипт без хранилищ:\n%s", plain)
	}
}

// Имена хранилищ в мастере обязаны совпадать с тем, что пишет бэкап.
func TestRestoreStoreNamesMatchBackend(t *testing.T) {
	cfg := &backend.Config{DBPath: "/var/lib/wg-monitor/state.db"}
	backend.ApplyStoreDefaults(cfg)
	var want []string
	for _, st := range cfg.StoreFiles() {
		want = append(want, st.Name)
	}
	slices.Sort(want)
	got := append([]string{}, restoreStoreNames...)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("restoreStoreNames = %v, бэкенд знает %v", got, want)
	}
	// И пути по умолчанию считаются одинаково.
	rc := &restoreBackendConfig{DBPath: cfg.DBPath}
	for _, st := range cfg.StoreFiles() {
		if got := rc.storeDestination(st.Name); got != st.Path {
			t.Errorf("%s: мастер кладёт в %q, бэкенд читает из %q", st.Name, got, st.Path)
		}
	}
}

func TestImportEncryptedFullBackupReadsV2(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WG_NO_SECRET_CACHE", "")
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "local"))
	t.Setenv("APPDATA", filepath.Join(dir, "roaming"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("HOME", dir)

	operatorPlain := makeTGZForTest(t, map[string]string{
		"secrets.env": "WIZARD_TOKEN=abc\n",
		"wizard.toml": "schema_version = 1\n",
	})
	operatorEnc, err := backup.Encrypt(operatorPlain, []byte(restoreTestPass), backup.TestParams())
	if err != nil {
		t.Fatal(err)
	}
	entries := append(smallArchiveEntries(restoreYAMLWithReviveKey()), tarEntryForTest{"operator-secrets.tgz.enc", string(operatorEnc)})
	path := writeEncryptedRestoreArchive(t, "full.tgz.enc", entries)
	if err := ImportEncryptedFullBackup(path, restoreTestPass, true); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(secretsCachePath()); err != nil || string(body) != "WIZARD_TOKEN=abc\n" {
		t.Fatalf("секреты оператора не импортированы: %q %v", body, err)
	}
	if err := ImportEncryptedFullBackup(path, "wrong", true); err == nil {
		t.Fatal("неверный пароль принят")
	}
}
