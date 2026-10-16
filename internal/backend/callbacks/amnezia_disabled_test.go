package callbacks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
)

// v0.61.3: кабинет Amnezia Premium отключён целиком. Сервер ниже считает
// ЛЮБОЙ запрос -- ни выпуск, ни экран подписки, ни отзыв страны, ни проверка
// ключа при добавлении не должны до него дойти.
func TestAmneziaDisabled_NoCabinetRequestOnAnyPath(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"message":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	r := &Router{cfg: Config{
		AmneziaBaseURL:     srv.URL,
		AmneziaSecretsPath: filepath.Join(dir, "amnezia-premium.json"),
		HideMyBaseURL:      srv.URL,
		HideMySecretsPath:  filepath.Join(dir, "hidemyname.json"),
	}}
	const routerID = 7
	// Ключ, сохранённый до отключения: он лежит в хранилище, но в кабинет
	// с ним больше не ходят.
	stored, err := r.addAmneziaKeyLabeled(routerID, "vpn://OLD-KEY-abcd1234", "старый")
	if err != nil {
		t.Fatalf("старый ключ: %v", err)
	}
	ctx := context.Background()

	if _, err := r.IssueConfig(ctx, routerID, providerAmnezia, "nl"); !errors.Is(err, backend.ErrAmneziaDisabled) {
		t.Errorf("выпуск: ждали ErrAmneziaDisabled, получили %v", err)
	}

	acc, err := r.Account(ctx, routerID, providerAmnezia)
	if err != nil {
		t.Errorf("экран подписки: %v", err)
	}
	if acc.Connected || !acc.Disabled || len(acc.Options) != 0 || acc.Note != backend.AmneziaDisabledText {
		t.Errorf("экран подписки: %+v", acc)
	}

	if err := r.RevokeSlot(ctx, routerID, "nl"); !errors.Is(err, backend.ErrAmneziaDisabled) {
		t.Errorf("отзыв страны: ждали ErrAmneziaDisabled, получили %v", err)
	}

	if _, err := r.AddSecret(ctx, routerID, providerAmnezia, "vpn://NEW-KEY-efgh5678", "новый"); !errors.Is(err, backend.ErrAmneziaDisabled) {
		t.Errorf("добавление ключа: ждали ErrAmneziaDisabled, получили %v", err)
	}
	secrets, err := r.Secrets(routerID, providerAmnezia)
	if err != nil || len(secrets) != 1 || secrets[0].ID != stored.ID {
		t.Errorf("новый ключ не сохраняется, старый на месте: %+v %v", secrets, err)
	}

	// Сохранённый ключ можно удалить -- и это тоже без кабинета.
	if err := r.DeleteSecret(routerID, providerAmnezia, stored.ID); err != nil {
		t.Errorf("удаление ключа: %v", err)
	}
	if secrets, _ := r.Secrets(routerID, providerAmnezia); len(secrets) != 0 {
		t.Errorf("ключ не удалился: %+v", secrets)
	}

	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("в кабинет ушло %d запросов, ждали 0", n)
	}
}

// Пути, где ключа нет вовсе, говорят ту же причину, а не «ключ не сохранён»:
// добавить ключ всё равно нельзя.
func TestAmneziaDisabled_WithoutKeySaysDisabled(t *testing.T) {
	dir := t.TempDir()
	r := &Router{cfg: Config{
		AmneziaBaseURL:     "http://198.51.100.1:1",
		AmneziaSecretsPath: filepath.Join(dir, "amnezia-premium.json"),
	}}
	ctx := context.Background()
	if _, err := r.IssueConfig(ctx, 1, providerAmnezia, "nl"); !errors.Is(err, backend.ErrAmneziaDisabled) {
		t.Errorf("выпуск без ключа: %v", err)
	}
	if err := r.RevokeSlot(ctx, 1, "nl"); !errors.Is(err, backend.ErrAmneziaDisabled) {
		t.Errorf("отзыв без ключа: %v", err)
	}
	if acc, _ := r.Account(ctx, 1, providerAmnezia); acc.Connected || acc.Note != backend.AmneziaDisabledText {
		t.Errorf("экран без ключа: %+v", acc)
	}
	// Ключ неверного вида -- та же причина: дело не в виде ключа.
	if _, err := r.AddSecret(ctx, 1, providerAmnezia, "abc", ""); !errors.Is(err, backend.ErrAmneziaDisabled) {
		t.Errorf("ключ неверного вида: %v", err)
	}
}
