package backend

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/sealedfile"
)

func writeSealKeyFile(t *testing.T, dir string) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "revive.key")
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(k)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func sealTestStores(dir string) []StoreFile {
	return []StoreFile{
		{Name: sealedfile.DomainAmnezia, Path: filepath.Join(dir, sealedfile.DomainAmnezia)},
		{Name: sealedfile.DomainSelfHosted, Path: filepath.Join(dir, sealedfile.DomainSelfHosted)},
		{Name: sealedfile.DomainAwg3, Path: filepath.Join(dir, sealedfile.DomainAwg3)},
		{Name: sealedfile.DomainHideMy, Path: filepath.Join(dir, sealedfile.DomainHideMy)},
	}
}

func sealLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, nil))
}

// Первый старт с ключом перешифровывает открытые файлы на месте; второй
// старт ничего не трогает; предупреждения в сводке нет.
func TestSealCabinetStores_FirstStartSealsThenIdempotent(t *testing.T) {
	t.Cleanup(func() { sealedfile.SetKey(nil) })
	dir := t.TempDir()
	keyFile := writeSealKeyFile(t, t.TempDir())
	stores := sealTestStores(dir)
	const secret = "vpn://startup-fixture-5e1d"
	plain := `{"version":1,"routers":{"7":"` + secret + `"}}`
	if err := os.WriteFile(stores[0].Path, []byte(plain), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stores[3].Path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	if w := SealCabinetStores(stores, keyFile, sealLogger(&log)); w != "" {
		t.Fatalf("предупреждение при исправном ключе: %q", w)
	}
	raw, _ := os.ReadFile(stores[0].Path)
	if !sealedfile.IsSealed(raw) || bytes.Contains(raw, []byte(secret)) {
		t.Fatalf("файл не перешифрован: %q", raw)
	}
	got, err := sealedfile.ReadFile(stores[0].Path, sealedfile.DomainAmnezia)
	if err != nil || string(got) != plain {
		t.Fatalf("got %q, %v", got, err)
	}
	if strings.Contains(log.String(), secret) {
		t.Fatal("секрет в журнале")
	}
	if _, err := os.Stat(stores[1].Path); !os.IsNotExist(err) {
		t.Fatal("несуществующее хранилище создано")
	}
	sealedfile.SetKey(nil)
	if w := SealCabinetStores(stores, keyFile, sealLogger(&log)); w != "" {
		t.Fatalf("повторный старт: %q", w)
	}
	raw2, _ := os.ReadFile(stores[0].Path)
	if !bytes.Equal(raw, raw2) {
		t.Fatal("повторный старт переписал файл")
	}
}

// Нет ключа: старт не падает, файлы открытые и нетронутые, в журнале и в
// сводке -- предупреждение.
func TestSealCabinetStores_NoKeyWarnsAndLeavesFiles(t *testing.T) {
	t.Cleanup(func() { sealedfile.SetKey(nil) })
	dir := t.TempDir()
	stores := sealTestStores(dir)
	if err := os.WriteFile(stores[2].Path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	w := SealCabinetStores(stores, "", sealLogger(&log))
	if !strings.Contains(w, "открытыми") {
		t.Fatalf("предупреждение сводки: %q", w)
	}
	if !strings.Contains(log.String(), "level=WARN") {
		t.Fatalf("нет предупреждения в журнале: %s", log.String())
	}
	raw, _ := os.ReadFile(stores[2].Path)
	if string(raw) != `{"version":1}` {
		t.Fatal("без ключа файл тронут")
	}
	if sealedfile.Enabled() {
		t.Fatal("ключ процесса поставлен без ключа")
	}
	// Ключ задан, но файла нет -- то же, что нет ключа.
	if w := SealCabinetStores(stores, filepath.Join(dir, "missing.key"), sealLogger(&log)); !strings.Contains(w, "открытыми") {
		t.Fatalf("ключ не прочитан: %q", w)
	}
}

// Нечего защищать -- нечего и говорить.
func TestSealCabinetStores_NoKeyNoFilesNoWarning(t *testing.T) {
	t.Cleanup(func() { sealedfile.SetKey(nil) })
	var log bytes.Buffer
	if w := SealCabinetStores(sealTestStores(t.TempDir()), "", sealLogger(&log)); w != "" {
		t.Fatalf("предупреждение без файлов: %q", w)
	}
}

// Файлы зашифрованы, а ключа больше нет -- сводка говорит, что кабинеты не
// прочитать; файлы не тронуты.
func TestSealCabinetStores_SealedWithoutKey(t *testing.T) {
	t.Cleanup(func() { sealedfile.SetKey(nil) })
	dir := t.TempDir()
	keyFile := writeSealKeyFile(t, t.TempDir())
	stores := sealTestStores(dir)
	_ = os.WriteFile(stores[0].Path, []byte(`{"version":1}`), 0o600)
	var log bytes.Buffer
	SealCabinetStores(stores, keyFile, sealLogger(&log))
	before, _ := os.ReadFile(stores[0].Path)
	sealedfile.SetKey(nil)
	w := SealCabinetStores(stores, "", sealLogger(&log))
	if !strings.Contains(w, "ключ шифрования не найден") {
		t.Fatalf("сводка: %q", w)
	}
	after, _ := os.ReadFile(stores[0].Path)
	if !bytes.Equal(before, after) {
		t.Fatal("файл испорчен")
	}
	// Другой ключ -- «не тот ключ».
	w = SealCabinetStores(stores, writeSealKeyFile(t, t.TempDir()), sealLogger(&log))
	if !strings.Contains(w, "не тот") {
		t.Fatalf("чужой ключ: %q", w)
	}
}

// Предупреждение о ключах кабинетов на диске -- в строке «Бэкенд» сводки
// парка; нет предупреждения -- нет и поля.
func TestMiniappFleetCarriesCabinetSealWarning(t *testing.T) {
	stubLatestVersion(t, "v0.31.0")
	d, _, _, _ := seedMiniappFleet(t)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, CabinetSealWarning: cabinetSealWarnOpen})
	resp := fleetResponse(t, fleetRequest(t, h, 999))
	if resp.Backend.SecretsWarning != cabinetSealWarnOpen {
		t.Fatalf("secrets_warning = %q", resp.Backend.SecretsWarning)
	}
	h = NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	if body := fleetRequest(t, h, 999).Body.String(); strings.Contains(body, "secrets_warning") {
		t.Fatalf("поле без предупреждения: %s", body)
	}
}

// listErrCabinetKeys -- кабинеты, чьё хранилище не читается.
type listErrCabinetKeys struct {
	*fakeCabinetKeys
	err error
}

func (f listErrCabinetKeys) Secrets(int64, string) ([]CabinetSecret, error) { return nil, f.err }

// Зашифрованный файл без ключа -- на экране кабинета понятные слова, а не
// «Не получилось на стороне сервера».
func TestMiniappCabinetStoreKeyErrorsSpeakWords(t *testing.T) {
	missing := fmt.Errorf("read amnezia secrets: %w", sealedfile.ErrKeyMissing)
	wrong := fmt.Errorf("read amnezia secrets: %w", sealedfile.ErrUnreadable)

	env := newCabinetEnv(t, func(d *Deps) {
		d.VPNCabinetKeys = listErrCabinetKeys{fakeCabinetKeys: &fakeCabinetKeys{list: map[string][]CabinetSecret{}}, err: missing}
	})
	rec := env.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/cabinets", "")
	if code, msg, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusServiceUnavailable || code != "cabinet_key_missing" || !strings.Contains(msg, "ключ шифрования не найден") {
		t.Fatalf("список без ключа: %d %s", rec.Code, rec.Body.String())
	}

	env = newCabinetEnv(t)
	env.keys.addErr = wrong
	rec = env.do(t, cabOwner, http.MethodPost, "/v1/miniapp/routers/{id}/cabinets/amnezia/keys", `{"vpn_key":"vpn://seal-key-0001"}`)
	if code, msg, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusServiceUnavailable || code != "cabinet_key_wrong" || !strings.Contains(msg, "не тот") {
		t.Fatalf("добавление с чужим ключом: %d %s", rec.Code, rec.Body.String())
	}

	env.vps.createErr = fmt.Errorf("load: %w", sealedfile.ErrKeyMissing)
	rec = env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/selfhosted", `{"id":"work","endpoint_host":"vpn2.example.com","endpoint_port":1}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusServiceUnavailable || code != "cabinet_key_missing" {
		t.Fatalf("свой сервер без ключа: %d %s", rec.Code, rec.Body.String())
	}

	env.awg3.createErr = fmt.Errorf("чтение: %w", sealedfile.ErrKeyMissing)
	rec = env.do(t, cabAdmin, http.MethodPost, "/v1/miniapp/awg3panels", awg3FormBody)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusServiceUnavailable || code != "cabinet_key_missing" {
		t.Fatalf("панель без ключа: %d %s", rec.Code, rec.Body.String())
	}
}

// Имя хранилища в StoreFiles -- домен шифра (AAD) этого файла: читающий код
// берёт домен из sealedfile, перешифровка на старте -- имя из StoreFiles.
// Разойдутся -- перешифрованный файл перестанет читаться.
func TestStoreFileNamesAreSealDomains(t *testing.T) {
	cfg := loadStoreCfg(t, "")
	want := map[string]bool{sealedfile.DomainAmnezia: true, sealedfile.DomainHideMy: true, sealedfile.DomainSelfHosted: true, sealedfile.DomainAwg3: true}
	stores := cfg.StoreFiles()
	if len(stores) != len(want) {
		t.Fatalf("хранилищ %d, доменов %d", len(stores), len(want))
	}
	for _, st := range stores {
		if !want[st.Name] {
			t.Fatalf("хранилище %q без домена шифра", st.Name)
		}
	}
}

// Fix round 1: одно хранилище зашифровано ключом A, другое открыто. Старт с
// ключом B ничего не шифрует (иначе ни один ключ не читал бы все файлы),
// говорит «не тот ключ»; старт с A после этого читает оба.
func TestSealCabinetStores_WrongKeyDoesNotSealPlainStores(t *testing.T) {
	t.Cleanup(func() { sealedfile.SetKey(nil) })
	dir := t.TempDir()
	keyA := writeSealKeyFile(t, t.TempDir())
	keyB := writeSealKeyFile(t, t.TempDir())
	stores := sealTestStores(dir)
	_ = os.WriteFile(stores[0].Path, []byte(`{"version":1,"a":1}`), 0o600)
	var log bytes.Buffer
	SealCabinetStores(stores[:1], keyA, sealLogger(&log)) // amnezia -- шифр A
	plain := []byte(`{"version":1,"h":1}`)
	_ = os.WriteFile(stores[3].Path, plain, 0o600) // hidemy -- открыт

	log.Reset()
	w := SealCabinetStores(stores, keyB, sealLogger(&log))
	if !strings.Contains(w, "не тот") {
		t.Fatalf("сводка: %q", w)
	}
	if !strings.Contains(log.String(), "level=ERROR") {
		t.Fatalf("нет ERROR в журнале: %s", log.String())
	}
	raw, _ := os.ReadFile(stores[3].Path)
	if !bytes.Equal(raw, plain) {
		t.Fatalf("открытый файл зашифрован чужим ключом: %q", raw)
	}

	if w := SealCabinetStores(stores, keyA, sealLogger(&log)); w != "" {
		t.Fatalf("старт с прежним ключом: %q", w)
	}
	for _, i := range []int{0, 3} {
		if _, err := sealedfile.ReadFile(stores[i].Path, stores[i].Name); err != nil {
			t.Fatalf("%s не читается прежним ключом: %v", stores[i].Name, err)
		}
	}
}

// Оба предупреждения не затирают друг друга.
func TestCabinetSealWarningsJoin(t *testing.T) {
	got := joinSealWarnings([]string{cabinetSealWarnPartlyOpen, cabinetSealWarnWrongKey, cabinetSealWarnPartlyOpen})
	if !strings.Contains(got, cabinetSealWarnPartlyOpen) || !strings.Contains(got, cabinetSealWarnWrongKey) || strings.Count(got, cabinetSealWarnPartlyOpen) != 1 {
		t.Fatalf("got %q", got)
	}
	if joinSealWarnings(nil) != "" {
		t.Fatal("пусто")
	}
}

// На старте убираются временные файлы прерванной записи хранилищ: в них
// секреты, читать их некому.
func TestSealCabinetStores_RemovesStaleTemps(t *testing.T) {
	t.Cleanup(func() { sealedfile.SetKey(nil) })
	dir := t.TempDir()
	stores := sealTestStores(dir)
	stale := filepath.Join(dir, "."+sealedfile.DomainHideMy+".tmp-12345")
	other := filepath.Join(dir, ".unrelated.tmp-1")
	_ = os.WriteFile(stale, []byte(`{"code":"1234567890"}`), 0o600)
	_ = os.WriteFile(other, []byte(`x`), 0o600)
	var log bytes.Buffer
	SealCabinetStores(stores, "", sealLogger(&log))
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("временный файл хранилища не убран")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("чужой файл убран")
	}
}

// Fix round 2: после старта с чужим ключом обычная запись в открытое
// хранилище остаётся открытой; старт с прежним ключом читает оба.
func TestSealCabinetStores_WrongKeyRuntimeWritesStayPlain(t *testing.T) {
	t.Cleanup(func() { sealedfile.SetKey(nil) })
	dir := t.TempDir()
	keyA := writeSealKeyFile(t, t.TempDir())
	keyB := writeSealKeyFile(t, t.TempDir())
	stores := sealTestStores(dir)
	_ = os.WriteFile(stores[0].Path, []byte(`{"version":1,"a":1}`), 0o600)
	var log bytes.Buffer
	SealCabinetStores(stores[:1], keyA, sealLogger(&log))
	_ = os.WriteFile(stores[3].Path, []byte(`{"version":1}`), 0o600)

	if w := SealCabinetStores(stores, keyB, sealLogger(&log)); !strings.Contains(w, "не тот") {
		t.Fatalf("сводка: %q", w)
	}
	want := []byte(`{"version":1,"h":2}`)
	if err := sealedfile.WriteFile(stores[3].Path, sealedfile.DomainHideMy, want); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(stores[3].Path)
	if !bytes.Equal(raw, want) {
		t.Fatalf("запись в рантайме зашифровала открытое хранилище чужим ключом: %q", raw)
	}
	if _, err := sealedfile.ReadFile(stores[0].Path, sealedfile.DomainAmnezia); !errors.Is(err, sealedfile.ErrUnreadable) {
		t.Fatalf("чтение шифра A ключом B: %v", err)
	}

	if w := SealCabinetStores(stores, keyA, sealLogger(&log)); w != "" {
		t.Fatalf("старт с прежним ключом: %q", w)
	}
	for _, i := range []int{0, 3} {
		if _, err := sealedfile.ReadFile(stores[i].Path, stores[i].Name); err != nil {
			t.Fatalf("%s не читается прежним ключом: %v", stores[i].Name, err)
		}
	}
}
