package backend

import (
	"context"
	"errors"
	"testing"
	"time"
)

func resetDashboardLatestCache() {
	dashboardLatestMu.Lock()
	dashboardLatestCached = dashboardLatestCache{}
	dashboardLatestMu.Unlock()
}

// Панель парка не имеет права зависеть от доступности GitHub. Когда сеть до
// api.github.com пропадала, КАЖДЫЙ запрос сводки ждал полный таймаут в 12
// секунд -- и релей рвал соединение раньше, чем бэкенд успевал ответить.
// Дашборд переставал открываться целиком из-за одной справочной строки
// «доступна версия N».
func TestLatestVersionLookup_RemembersFailureAndAnswersFast(t *testing.T) {
	resetDashboardLatestCache()
	t.Cleanup(resetDashboardLatestCache)

	calls := 0
	fetch := func(context.Context) (string, error) {
		calls++
		return "", errors.New("сеть недоступна")
	}

	if _, err := cachedDashboardLatestVersion(context.Background(), fetch, time.Now()); err == nil {
		t.Fatal("первая попытка обязана вернуть ошибку сети")
	}
	// Вторая попытка сразу после провала не идёт в сеть вовсе.
	v, err := cachedDashboardLatestVersion(context.Background(), fetch, time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatalf("после свежего провала ждали тихий пропуск, получили %v", err)
	}
	if v != "" {
		t.Fatalf("версия из ниоткуда: %q", v)
	}
	if calls != 1 {
		t.Fatalf("походов в сеть %d, ждали 1", calls)
	}

	// Когда пауза прошла -- пробуем снова.
	if _, err := cachedDashboardLatestVersion(context.Background(), fetch, time.Now().Add(dashboardLatestFailTTL+time.Second)); err == nil {
		t.Fatal("после паузы ждали новую попытку и её ошибку")
	}
	if calls != 2 {
		t.Fatalf("походов в сеть %d, ждали 2", calls)
	}
}

func TestLatestVersionLookup_ServesCacheWithoutNetwork(t *testing.T) {
	resetDashboardLatestCache()
	t.Cleanup(resetDashboardLatestCache)

	calls := 0
	fetch := func(context.Context) (string, error) {
		calls++
		return "v0.19.0", nil
	}
	now := time.Now()
	if v, err := cachedDashboardLatestVersion(context.Background(), fetch, now); err != nil || v != "v0.19.0" {
		t.Fatalf("первая выборка: %q %v", v, err)
	}
	if v, err := cachedDashboardLatestVersion(context.Background(), fetch, now.Add(time.Minute)); err != nil || v != "v0.19.0" {
		t.Fatalf("из кэша: %q %v", v, err)
	}
	if calls != 1 {
		t.Fatalf("походов в сеть %d, ждали 1", calls)
	}
}

// Пока сеть лежит, панель показывает последнюю известную версию, а не пустоту:
// «доступна v0.19.0» из кэша честнее, чем молчание.
func TestLatestVersionLookup_KeepsLastKnownWhileNetworkIsDown(t *testing.T) {
	resetDashboardLatestCache()
	t.Cleanup(resetDashboardLatestCache)

	now := time.Now()
	ok := func(context.Context) (string, error) { return "v0.19.0", nil }
	if _, err := cachedDashboardLatestVersion(context.Background(), ok, now); err != nil {
		t.Fatal(err)
	}
	bad := func(context.Context) (string, error) { return "", errors.New("сеть недоступна") }
	after := now.Add(dashboardLatestCacheTTL + time.Second)
	if _, err := cachedDashboardLatestVersion(context.Background(), bad, after); err == nil {
		t.Fatal("ждали ошибку сети")
	}
	v, err := cachedDashboardLatestVersion(context.Background(), bad, after.Add(time.Second))
	if err != nil {
		t.Fatalf("после провала ждали тихий ответ, получили %v", err)
	}
	if v != "v0.19.0" {
		t.Fatalf("потеряли последнюю известную версию: %q", v)
	}
}
