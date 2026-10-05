package linkrepair

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeKV map[string]string

func (f fakeKV) Get(k string) (string, error) { return f[k], nil }
func (f fakeKV) Set(k, v string) error        { f[k] = v; return nil }

func TestAllow_FourthAttemptInSixHoursRefused(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	a := Attempts{KV: fakeKV{}, Now: func() time.Time { return now }}

	for i := 0; i < 3; i++ {
		if ok, why := a.Allow("роутер", "tunnel_awg12"); !ok {
			t.Fatalf("попытка %d должна проходить, отказ: %s", i+1, why)
		}
		if err := a.Record("роутер", "tunnel_awg12", true); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	ok, why := a.Allow("роутер", "tunnel_awg12")
	if ok {
		t.Fatal("четвёртая попытка за 6 часов обязана быть отвергнута")
	}
	if why == "" {
		t.Fatal("отказ обязан объясняться словами -- их увидит человек")
	}
	if !strings.Contains(why, "VPN-туннель") || strings.Contains(strings.ToLower(why), "лини") {
		t.Fatalf("отказ говорит словарём приложения -- VPN-туннель, а не линия: %q", why)
	}
}

// Окно скользящее: спустя 6 часов счётчик перестаёт мешать.
func TestAllow_WindowExpires(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	a := Attempts{KV: fakeKV{}, Now: func() time.Time { return now }}
	for i := 0; i < 3; i++ {
		_, _ = a.Allow("роутер", "tunnel_awg12")
		_ = a.Record("роутер", "tunnel_awg12", true)
	}
	now = now.Add(6*time.Hour + time.Minute)
	if ok, why := a.Allow("роутер", "tunnel_awg12"); !ok {
		t.Fatalf("за пределами окна попытка обязана проходить, отказ: %s", why)
	}
}

// Неудача -- сигнал, что автоматика не справляется. Повторять её на том же
// месте бессмысленно: зовём человека.
func TestAllow_AfterFailureWaitsForHuman(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	a := Attempts{KV: fakeKV{}, Now: func() time.Time { return now }}
	_, _ = a.Allow("роутер", "tunnel_awg12")
	if err := a.Record("роутер", "tunnel_awg12", false); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if ok, _ := a.Allow("роутер", "tunnel_awg12"); ok {
		t.Fatal("после неудачи автопочинка обязана ждать человека")
	}
}

// Разные линии одного роутера считаются порознь.
func TestAllow_PerCheckIndependent(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	a := Attempts{KV: fakeKV{}, Now: func() time.Time { return now }}
	_ = a.Record("роутер", "tunnel_awg12", false)
	if ok, _ := a.Allow("роутер", "tunnel_awg10"); !ok {
		t.Fatal("неудача на одной линии не запрещает чинить другую")
	}
}

// safeKV -- KV под замком: счётчик пишет фоновая починка, читает тест.
type safeKV struct {
	mu sync.Mutex
	m  map[string]string
}

func newSafeKV() *safeKV { return &safeKV{m: map[string]string{}} }

func (s *safeKV) Get(k string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[k], nil
}

func (s *safeKV) Set(k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
	return nil
}

// D1: стоп после провала снимается -- удачной ручной починкой, возвратом
// VPN-туннеля в норму или повторным включением тумблера. Раньше снять его
// было нечем: VPN-туннель больше никогда не чинился сам.
func TestAttempts_ClearUnblocks(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	a := Attempts{KV: fakeKV{}, Now: func() time.Time { return now }}
	if err := a.Record("роутер", "tunnel_awg12", false); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if ok, _ := a.Allow("роутер", "tunnel_awg12"); ok {
		t.Fatal("после провала стоп обязан стоять")
	}
	if blocked, why := a.Blocked("роутер", "tunnel_awg12"); !blocked || why == "" {
		t.Fatalf("Blocked обязан говорить то же, что Allow: %v %q", blocked, why)
	}
	if err := a.Clear("роутер", "tunnel_awg12"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if ok, why := a.Allow("роутер", "tunnel_awg12"); !ok {
		t.Fatalf("Clear обязан снять стоп: %s", why)
	}
}

// Clear снимает стоп, но не окно: флапающий VPN-туннель, который каждый раз
// чинится, всё равно упирается в три попытки за 6 часов.
func TestAttempts_ClearKeepsWindow(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	a := Attempts{KV: fakeKV{}, Now: func() time.Time { return now }}
	for i := 0; i < 3; i++ {
		_ = a.Record("роутер", "tunnel_awg12", true)
	}
	_ = a.Clear("роутер", "tunnel_awg12")
	if ok, _ := a.Allow("роутер", "tunnel_awg12"); ok {
		t.Fatal("Clear не обнуляет счётчик попыток окна")
	}
}

// Clear на пустом месте -- не ошибка и ничего не заводит.
func TestAttempts_ClearNothing(t *testing.T) {
	kv := fakeKV{}
	a := Attempts{KV: kv}
	if err := a.Clear("роутер", "tunnel_awg12"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if ok, _ := a.Allow("роутер", "tunnel_awg12"); !ok {
		t.Fatal("пустой счётчик обязан пускать")
	}
}
