package linkrepair

import (
	"strings"
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
