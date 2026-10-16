package callbacks

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/hidemy"
)

const (
	cabinetGoodKey  = "vpn://GOOD-KEY-abcd1234"
	cabinetGoodCode = "123456789012345"
	cabinetBadCode  = "999999999999999"
)

// fakeCabinets -- кабинет HideMy: принимает только «хороший» код. calls
// считает обращения -- код неверного вида до кабинета доходить не должен.
// Путей кабинета Amnezia нет: он отключён (v0.61.3), и любое обращение к
// нему -- ошибка теста.
func fakeCabinets(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		switch r.URL.Path {
		case "/api/serverlist.php":
			_ = r.ParseForm()
			if r.PostFormValue("code") != cabinetGoodCode {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`[{"name":"Нидерланды","name_en":"Netherlands","services":{"wg":{"ip":"198.51.100.10"}}}]`))
		default:
			t.Errorf("неожиданный путь кабинета %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func newCabinetKeysRouter(t *testing.T) (*Router, *int32) {
	t.Helper()
	srv, calls := fakeCabinets(t)
	dir := t.TempDir()
	return &Router{cfg: Config{
		AmneziaBaseURL:     srv.URL,
		AmneziaSecretsPath: filepath.Join(dir, "amnezia-premium.json"),
		HideMyBaseURL:      srv.URL,
		HideMySecretsPath:  filepath.Join(dir, "hidemyname.json"),
	}}, calls
}

func TestCabinetKeysHideMyAddChecksServerList(t *testing.T) {
	r, _ := newCabinetKeysRouter(t)
	ctx := context.Background()
	var rejected *backend.CabinetRejectedError
	if _, err := r.AddSecret(ctx, 7, providerHideMy, cabinetBadCode, ""); !errors.As(err, &rejected) {
		t.Fatalf("кабинет отверг код, а ошибка %v", err)
	}
	if _, err := r.AddSecret(ctx, 7, providerHideMy, "12ab", ""); !errors.Is(err, backend.ErrCabinetSecretInvalid) {
		t.Fatalf("неверный вид: %v", err)
	}
	added, err := r.AddSecret(ctx, 7, providerHideMy, cabinetGoodCode, "")
	if err != nil || added.Label != "Код #1" || added.Mask != "••••2345" {
		t.Fatalf("added = %+v, err=%v", added, err)
	}
}

func TestCabinetKeysActiveDeleteAndRevokeWithoutKey(t *testing.T) {
	r, _ := newCabinetKeysRouter(t)
	ctx := context.Background()
	if err := r.RevokeSlot(ctx, 7, "nl"); !errors.Is(err, backend.ErrAmneziaDisabled) {
		t.Fatalf("отзыв без ключа: %v", err)
	}
	// Ключ, сохранённый до отключения кабинета (v0.61.3): выбрать и удалить
	// его можно, отозвать им страну -- нет.
	added, err := r.addAmneziaKeyLabeled(7, cabinetGoodKey, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetActiveSecret(7, providerAmnezia, "000000000000"); !errors.Is(err, backend.ErrCabinetSecretNotFound) {
		t.Fatalf("активный несуществующий: %v", err)
	}
	if err := r.SetActiveSecret(7, providerAmnezia, added.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.RevokeSlot(ctx, 7, "nl"); !errors.Is(err, backend.ErrAmneziaDisabled) {
		t.Fatalf("отзыв активным ключом: %v", err)
	}
	if err := r.DeleteSecret(7, providerAmnezia, added.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteSecret(7, providerHideMy, "000000000000"); !errors.Is(err, backend.ErrCabinetSecretNotFound) {
		t.Fatalf("удаление несуществующего кода: %v", err)
	}
}

// Кабинет без ключа объясняет себя словами -- и больше не отправляет
// человека «в бот»: бот ключи не принимает.
func TestMiniappCabinetNotesDoNotSendPeopleToBot(t *testing.T) {
	r, _ := newCabinetKeysRouter(t)
	for _, provider := range []string{providerAmnezia, providerHideMy} {
		acc, err := r.Account(context.Background(), 7, provider)
		if err != nil || acc.Connected || acc.Note == "" {
			t.Fatalf("%s: acc=%+v err=%v", provider, acc, err)
		}
		if strings.Contains(strings.ToLower(acc.Note), "бот") {
			t.Fatalf("%s: note отсылает в бот: %q", provider, acc.Note)
		}
	}
}

// Ошибка выпуска уходит в ответ /vpn/issue и в журнал мастера замены: ключа в
// ней быть не должно, даже если кабинет вернул его в теле отказа.
func TestIssueConfigErrorNeverCarriesKey(t *testing.T) {
	const key = "vpn://SECRET-ISSUE-KEY-MUST-NOT-LEAK"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"bad ` + string(body) + `"}`))
	}))
	defer srv.Close()
	r := &Router{cfg: Config{AmneziaBaseURL: srv.URL, AmneziaSecretsPath: filepath.Join(t.TempDir(), "amnezia-premium.json")}}
	if _, err := r.addAmneziaKeyLabeled(7, key, ""); err != nil {
		t.Fatal(err)
	}
	_, err := r.IssueConfig(context.Background(), 7, providerAmnezia, "nl")
	if err == nil || strings.Contains(err.Error(), "SECRET-ISSUE") {
		t.Fatalf("ошибка выпуска: %v", err)
	}
}

// Кабинет callbacks.Router -- источник конфигов для мастера замены и движка
// починки. Своего сервера в нём нет и быть не должно (решение 8).
func TestIssueConfigRefusesSelfHosted(t *testing.T) {
	r, calls := newCabinetKeysRouter(t)
	if _, err := r.IssueConfig(context.Background(), 7, "selfhosted", "home"); err == nil {
		t.Fatal("IssueConfig выпустил конфиг своего сервера")
	}
	if _, err := r.Account(context.Background(), 7, "selfhosted"); err == nil {
		t.Fatal("Account знает свой сервер")
	}
	if atomic.LoadInt32(calls) != 0 {
		t.Fatal("кабинеты не должны были спрашиваться")
	}
}

// То же для HideMy.name: мастер замены пишет текст ошибки выпуска в шаг, и
// кода (даже его части) там быть не должно.
func TestIssueConfigHideMyErrorNeverCarriesCode(t *testing.T) {
	const code = "123456789012345"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"bad code ` + r.PostFormValue("code")[:12] + `"}`))
	}))
	defer srv.Close()
	r := &Router{cfg: Config{HideMyBaseURL: srv.URL, HideMySecretsPath: filepath.Join(t.TempDir(), "hidemyname.json")}}
	if _, err := r.addHideMyCodeLabeled(7, code, ""); err != nil {
		t.Fatal(err)
	}
	_, err := r.IssueConfig(context.Background(), 7, providerHideMy, "a1b2c3d4e5f6")
	if err == nil || strings.Contains(err.Error(), "1234567890") {
		t.Fatalf("ошибка выпуска: %v", err)
	}
}

// HideMy.name не принял код (страница /vpn/fail/): человеку -- что дело в
// коде, а не «кабинет не ответил, повторите позже».
func TestIssueConfigHideMyRejectedCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/serverlist.php":
			_, _ = w.Write([]byte(`{"21":{"name_en":"Netherlands, Amsterdam DEMO","services":{"wg":{"ip":"198.51.100.21","port":38690}}}}`))
		case "/api/vpn_get_config_wg.php":
			_, _ = w.Write([]byte(`<html><head><meta http-equiv="refresh" content="0; url=/vpn/fail/"></head><body>Your code was not found or you have purchased a non-VPN code</body></html>`))
		}
	}))
	defer srv.Close()
	r := &Router{cfg: Config{HideMyBaseURL: srv.URL, HideMySecretsPath: filepath.Join(t.TempDir(), "hidemyname.json")}}
	if _, err := r.addHideMyCodeLabeled(7, "12345678901234", ""); err != nil {
		t.Fatal(err)
	}
	_, err := r.IssueConfig(context.Background(), 7, providerHideMy, hidemy.ServerID("198.51.100.21"))
	if !errors.Is(err, backend.ErrCabinetCodeRejected) {
		t.Fatalf("err = %v, want ErrCabinetCodeRejected", err)
	}
}
