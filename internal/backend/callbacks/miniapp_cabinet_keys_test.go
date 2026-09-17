package callbacks

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
)

const (
	cabinetGoodKey  = "vpn://GOOD-KEY-abcd1234"
	cabinetBadKey   = "vpn://SECRET-BAD-KEY-MUST-NOT-LEAK"
	cabinetGoodCode = "123456789012345"
	cabinetBadCode  = "999999999999999"
)

// fakeCabinets -- кабинеты Amnezia и HideMy в одном сервере: принимают
// только «хороший» ключ и код. calls считает обращения -- ключ неверного
// вида до кабинета доходить не должен.
func fakeCabinets(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		switch r.URL.Path {
		case "/api/login":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), cabinetGoodKey) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message":"bad key ` + string(body) + `"}`))
				return
			}
			_, _ = w.Write([]byte(`{"message":"ok"}`))
		case "/api/account-info":
			_, _ = w.Write([]byte(`{"data":{"subscription_status":"active","active_device_count":1,"max_device_count":2,"available_countries":[{"server_country_code":"nl","server_country_name":"Netherlands"}],"issued_configs":[]}}`))
		case "/api/revoke-country-config":
			_, _ = w.Write([]byte(`{"message":"ok"}`))
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

func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

func TestCabinetKeysAmneziaAddChecksCabinetAndMasks(t *testing.T) {
	r, calls := newCabinetKeysRouter(t)
	logs := captureDefaultLog(t)
	ctx := context.Background()

	if _, err := r.AddSecret(ctx, 7, providerAmnezia, "not-a-key", ""); !errors.Is(err, backend.ErrCabinetSecretInvalid) {
		t.Fatalf("неверный вид: %v", err)
	}
	if atomic.LoadInt32(calls) != 0 {
		t.Fatal("ключ неверного вида не должен уходить в кабинет")
	}

	var rejected *backend.CabinetRejectedError
	if _, err := r.AddSecret(ctx, 7, providerAmnezia, cabinetBadKey, "Дом"); !errors.As(err, &rejected) || rejected.Reason == "" {
		t.Fatalf("кабинет отверг ключ, а ошибка %v", err)
	}
	if strings.Contains(rejected.Reason, "SECRET-BAD") || strings.Contains(logs.String(), "SECRET-BAD") {
		t.Fatalf("секрет утёк: reason=%q logs=%q", rejected.Reason, logs.String())
	}
	if got, _ := r.Secrets(7, providerAmnezia); len(got) != 0 {
		t.Fatalf("отвергнутый ключ сохранён: %+v", got)
	}

	added, err := r.AddSecret(ctx, 7, providerAmnezia, cabinetGoodKey, "Дом")
	if err != nil || added.Label != "Дом" || added.Mask != "••••1234" || !added.Active || added.ID == "" {
		t.Fatalf("added = %+v, err=%v", added, err)
	}
	got, err := r.Secrets(7, providerAmnezia)
	if err != nil || len(got) != 1 || got[0] != added {
		t.Fatalf("Secrets = %+v, err=%v", got, err)
	}
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
	if err := r.RevokeSlot(ctx, 7, "nl"); !errors.Is(err, backend.ErrCabinetSecretNotFound) {
		t.Fatalf("отзыв без ключа: %v", err)
	}
	added, err := r.AddSecret(ctx, 7, providerAmnezia, cabinetGoodKey, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetActiveSecret(7, providerAmnezia, "000000000000"); !errors.Is(err, backend.ErrCabinetSecretNotFound) {
		t.Fatalf("активный несуществующий: %v", err)
	}
	if err := r.SetActiveSecret(7, providerAmnezia, added.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.RevokeSlot(ctx, 7, "nl"); err != nil {
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
