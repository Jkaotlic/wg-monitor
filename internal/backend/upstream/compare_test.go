package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestSoftwareNewerThan(t *testing.T) {
	cases := []struct {
		name      string
		installed string
		candidate string
		want      bool
	}{
		{"plain newer", "2.8.2", "2.9.0", true},
		{"plain older", "2.9.0", "2.8.2", false},
		{"equal", "2.9.0", "2.9.0", false},
		{"strip leading v installed", "v2.8.2", "2.9.0", true},
		{"strip leading v both", "v2.8.2", "v2.9.0", true},
		{"strip leading V capital", "V1.0.0", "v1.0.1", true},
		{"empty installed → no warning", "", "2.9.0", false},
		{"empty candidate → no warning", "2.8.2", "", false},
		{"junk installed → false (no false warnings)", "junk", "2.9.0", false},
		{"junk candidate → false", "2.8.2", "definitely-not-semver", false},
		{"both junk", "x", "y", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SoftwareNewerThan(c.installed, c.candidate); got != c.want {
				t.Errorf("SoftwareNewerThan(%q,%q)=%v want %v", c.installed, c.candidate, got, c.want)
			}
		})
	}
}

// Подсказку про обновление панели читает владелец роутера, а не оператор
// бэкенда: она обязана быть по-русски и говорить последствие. Прежний текст
// («NativeWG update crosses 2.10.6; plan a router reboot…») нарушал оба
// правила и вдобавок опирался на номер релиза, связь которого с модулем ядра
// разведка 12.09.2026 подтвердить не смогла (шаг B9 волны 0).
func TestAwgManagerUpdateHint_SpeaksRussianAboutConsequence(t *testing.T) {
	hint := AwgManagerUpdateHint("2.17.2", "2.18.0")
	for _, want := range []string{"может сменить модуль ядра", "VPN-туннели поднимутся только после перезагрузки роутера"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("подсказка %q не говорит про %q", hint, want)
		}
	}
	for _, forbidden := range []string{"NativeWG", "reboot", "plan a router"} {
		if strings.Contains(hint, forbidden) {
			t.Fatalf("английский текст доехал до владельца: %s", hint)
		}
	}
}

// Риск сменившегося модуля ядра одинаков на обоих движках: живой роутер с
// activeBackend=kernel тоже несёт модуль ядра (замер 12.09.2026: kmod
// 3.1.20260906 на KN-1811). Прежняя привязка подсказки к NativeWG-движку
// молчала бы там, где последствие настоящее.
func TestAwgManagerUpdateHint_NoUpdateNoHint(t *testing.T) {
	if hint := AwgManagerUpdateHint("2.18.0", "2.18.0"); hint != "" {
		t.Fatalf("без обновления подсказки быть не должно, got %q", hint)
	}
	if hint := AwgManagerUpdateHint("", "2.18.0"); hint != "" {
		t.Fatalf("без известной версии на роутере подсказки быть не должно, got %q", hint)
	}
}

// Пустой репозиторий, 403 от GitHub и настоящая свежесть выглядели одинаково.
// Правило «неизвестно -- это ответ» здесь не соблюдалось ни на одной
// поверхности: выключенный источник читался как «всё актуально».
func TestComputeUpdates_UnconfiguredSourceIsUnknownNotFresh(t *testing.T) {
	cache := NewCache(time.Hour, nil) // ни одного источника
	updates, unknown := ComputeUpdates(context.Background(), cache, wire.VersionAudit{AwgmgrVersion: "2.17.2"})
	if len(updates) != 0 {
		t.Errorf("без источника не бывает обновлений: %+v", updates)
	}
	if !hasUnknown(unknown, "awgmgr", ReasonNotConfigured) {
		t.Errorf("причина «источник не настроен» не названа: %+v", unknown)
	}
}

// Недоступный GitHub -- это «мы не знаем», а не «обновлений нет». Молчать
// здесь значит замолчать ровно тогда, когда новость нужнее всего.
func TestComputeUpdates_GitHubErrorIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	cache := NewCache(time.Hour, []Source{{Name: "awgmgr", GitHubRepo: "example/awgmgr"}})
	cache.api = srv.URL + "/%s"

	updates, unknown := ComputeUpdates(context.Background(), cache, wire.VersionAudit{AwgmgrVersion: "2.17.2"})
	if len(updates) != 0 {
		t.Errorf("на отказе апстрима обновлений не бывает: %+v", updates)
	}
	if !hasUnknown(unknown, "awgmgr", ReasonUnavailable) {
		t.Errorf("причина «апстрим недоступен» не названа: %+v", unknown)
	}
}

func TestComputeUpdates_NoSnapshotIsItsOwnReason(t *testing.T) {
	cache := NewCache(time.Hour, []Source{{Name: "awgmgr", GitHubRepo: "example/awgmgr"}})
	_, unknown := ComputeUpdates(context.Background(), cache, wire.VersionAudit{})
	if !hasUnknown(unknown, "awgmgr", ReasonNoSnapshot) {
		t.Errorf("пустой снимок обязан давать свою причину: %+v", unknown)
	}
}

// Старый агент про модуль ядра не сообщает вовсе, и это отдельное состояние:
// не «модуль не загружен» и не «всё в порядке».
func TestComputeUpdates_MissingKmodIsAgentTooOld(t *testing.T) {
	_, unknown := ComputeUpdates(context.Background(), nil, wire.VersionAudit{AwgmgrVersion: "2.17.2"})
	if !hasUnknown(unknown, "kmod", ReasonAgentTooOld) {
		t.Errorf("молчание старого агента о модуле ядра не названо: %+v", unknown)
	}
	_, unknown = ComputeUpdates(context.Background(), nil, wire.VersionAudit{KmodVersion: "3.1.20260906"})
	if hasUnknown(unknown, "kmod", ReasonAgentTooOld) {
		t.Errorf("агент сказал про модуль ядра, а причина всё равно названа: %+v", unknown)
	}
}

// Доступная прошивка приезжает от самого роутера, и GitHub к ней отношения не
// имеет: выключенный источник апстрима не имеет права молчать про прошивку.
func TestComputeUpdates_FirmwareComesFromRouterNotUpstream(t *testing.T) {
	updates, _ := ComputeUpdates(context.Background(), nil, wire.VersionAudit{
		FirmwareCurrent: "5.02.A.8.0-3",
		FirmwareAvail:   "5.02.A.9.0-0",
	})
	var found bool
	for _, u := range updates {
		if u.Name == "KeeneticOS" && u.Available == "5.02.A.9.0-0" {
			found = true
		}
	}
	if !found {
		t.Errorf("новость о прошивке не собралась без апстрима: %+v", updates)
	}
}

// Право сказать «нужна перезагрузка» даёт расхождение установленного и
// загруженного модуля ядра -- без истории снимков, и гаснет оно само.
func TestRebootHint_InstalledVersusLoaded(t *testing.T) {
	got := RebootHint("3.2.20260930", "3.1.20260906")
	if got != "Сменился модуль ядра AmneziaWG — VPN-туннели поднимутся после перезагрузки роутера." {
		t.Fatalf("текст: %q", got)
	}
	for _, forbidden := range []string{"NativeWG", "reboot", "панели"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("в тексте %q: %s", forbidden, got)
		}
	}
	for _, tc := range [][2]string{
		{"3.1.20260906", "3.1.20260906"}, // совпадают -- перезагружен
		{"", "3.1.20260906"},             // установленная неизвестна
		{"3.1.20260906", ""},             // старый агент загруженную не сообщает
	} {
		if got := RebootHint(tc[0], tc[1]); got != "" {
			t.Errorf("RebootHint(%q, %q) = %q, want пусто", tc[0], tc[1], got)
		}
	}
}

func hasUnknown(list []Unknown, component, reason string) bool {
	for _, u := range list {
		if u.Component == component && u.Reason == reason {
			return true
		}
	}
	return false
}

func TestFirmwareNewerThan(t *testing.T) {
	cases := []struct {
		name      string
		installed string
		candidate string
		want      bool
	}{
		// Standard dotted comparisons
		{"older numeric", "4.2.6", "5.0.1", true},
		{"newer numeric", "5.0.1", "4.2.6", false},
		{"equal", "4.2.6", "4.2.6", false},
		// Multi-level dots like the real Keenetic format
		{"keenetic 11→12 patch", "5.00.C.11.0-0", "5.00.C.12.0-0", true},
		{"keenetic 12→11 reverse", "5.00.C.12.0-0", "5.00.C.11.0-0", false},
		{"keenetic equal", "5.00.C.11.0-0", "5.00.C.11.0-0", false},
		// Alpha component (Keenetic uses letters like C/D in build IDs)
		{"alpha C→D", "5.0.A.1", "5.0.B.1", true},
		{"alpha D→C reverse", "5.0.B.1", "5.0.A.1", false},
		// Length differences
		{"longer candidate, same prefix", "5.0", "5.0.1", true},
		{"longer installed, same prefix", "5.0.1", "5.0", false},
		// Empty fallthroughs
		{"empty installed", "", "5.0.1", false},
		{"empty candidate", "4.2.6", "", false},
		{"overflowing installed segment does not look older", "5.999999999999999999999999999999", "5.1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FirmwareNewerThan(c.installed, c.candidate); got != c.want {
				t.Errorf("FirmwareNewerThan(%q,%q)=%v want %v", c.installed, c.candidate, got, c.want)
			}
		})
	}
}

func TestParsePosIntRejectsOverflow(t *testing.T) {
	if n, err := parsePosInt("9223372036854775808"); err == nil {
		t.Fatalf("parsePosInt overflow returned n=%d nil error, want error", n)
	}
}
