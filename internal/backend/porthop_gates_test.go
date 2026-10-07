package backend

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// v0.57: смена порта при блокировке и отчёт о месте на /opt. Все пять
// действий -- радиус router-global (сервис на роутере, init-скрипт, перенос
// ручной копии оператора), поэтому круг -- только админ, а старый агент их
// не знает вовсе: пол версии v0.57.0.
var v057MaintenanceActions = []string{
	"porthop_status", "porthop_install", "porthop_remove", "porthop_logs", "space_report",
}

func TestV057ActionsInEveryAllowlist(t *testing.T) {
	for _, a := range v057MaintenanceActions {
		if !miniappCommandAllowlist[a] {
			t.Errorf("%s нет в белом списке мини-аппа", a)
		}
		if !wizardCommandAllowlist[a] {
			t.Errorf("%s нет в белом списке мастера", a)
		}
		if !dashboardCommandAllowlist[a] {
			t.Errorf("%s нет в белом списке дашборда", a)
		}
		if !miniappAdminOnlyActions[a] {
			t.Errorf("%s обязано быть только для админа", a)
		}
		if miniappOwnerOnlyActions[a] {
			t.Errorf("%s -- только админ, а не «владелец и админ»", a)
		}
		if got := miniappActionMinAgentVersion[a]; got != "v0.57.0" {
			t.Errorf("пол %s = %q, want v0.57.0", a, got)
		}
	}
	// entware_clean_run остаётся как был: агент умеет его давно, и пол закрыл
	// бы кнопку очистки исправным роутерам.
	if _, floored := miniappActionMinAgentVersion["entware_clean_run"]; floored {
		t.Error("entware_clean_run получил пол версии -- кнопка очистки закрылась бы старым агентам")
	}
}

// Санитайзер: у каждого из пяти действий своя явная ветка. Ветка default
// возвращает аргументы как есть, и без явной ветки клиентский ввод ушёл бы
// агенту без проверки.
func TestV057SanitizerDropsForeignArgs(t *testing.T) {
	for _, a := range []string{"porthop_status", "porthop_remove", "space_report"} {
		rec := httptest.NewRecorder()
		got, ok := sanitizeWizardCommandArgs(rec, a, map[string]any{"ifaces": []any{"x"}, "cmd": "rm -rf /"})
		if !ok || len(got) != 0 {
			t.Errorf("%s: ok=%v args=%v, ожидались пустые аргументы", a, ok, got)
		}
	}
}

// Журнал смены порта -- та же ветка, что у журналов пакетов и очистки:
// lines 1..300, по умолчанию 80, прочее отброшено.
func TestV057SanitizerPorthopLogsLines(t *testing.T) {
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, "map[lines:80]"},
		{map[string]any{"lines": float64(120), "cmd": "x"}, "map[lines:120]"},
		{map[string]any{"lines": float64(0)}, "map[lines:1]"},
		{map[string]any{"lines": float64(5000)}, "map[lines:300]"},
	} {
		rec := httptest.NewRecorder()
		got, ok := sanitizeWizardCommandArgs(rec, "porthop_logs", tc.args)
		if !ok || fmt.Sprint(got) != tc.want {
			t.Errorf("porthop_logs %v: ok=%v args=%v, want %s", tc.args, ok, got, tc.want)
		}
	}
}

func TestV057SanitizerPorthopInstall(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want string // fmt.Sprint от результата; "" -- ждём отказ
		code string
	}{
		{"пусто -- auto", map[string]any{}, "map[replace_legacy:false]", ""},
		{"nil -- auto", nil, "map[replace_legacy:false]", ""},
		{"явный список и замена", map[string]any{"ifaces": []any{"opkgtun10", "nwg0"}, "replace_legacy": true, "extra": "x"},
			"map[ifaces:[opkgtun10 nwg0] replace_legacy:true]", ""},
		{"пустой список -- auto", map[string]any{"ifaces": []any{}}, "map[replace_legacy:false]", ""},
		{"дубли схлопываются", map[string]any{"ifaces": []any{"opkgtun10", "opkgtun10"}}, "map[ifaces:[opkgtun10] replace_legacy:false]", ""},
		{"имя с пробелом", map[string]any{"ifaces": []any{"opkg tun"}}, "", "invalid_ifaces"},
		{"имя с точкой с запятой", map[string]any{"ifaces": []any{"a;reboot"}}, "", "invalid_ifaces"},
		{"имя с заглавной", map[string]any{"ifaces": []any{"Opkgtun10"}}, "", "invalid_ifaces"},
		{"имя с цифры", map[string]any{"ifaces": []any{"0tun"}}, "", "invalid_ifaces"},
		{"имя длиннее 16", map[string]any{"ifaces": []any{"a" + strings.Repeat("b", 16)}}, "", "invalid_ifaces"},
		{"не строка", map[string]any{"ifaces": []any{12}}, "", "invalid_ifaces"},
		{"не список", map[string]any{"ifaces": "opkgtun10"}, "", "invalid_ifaces"},
		{"больше восьми", map[string]any{"ifaces": []any{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9"}}, "", "invalid_ifaces"},
		{"replace_legacy не bool", map[string]any{"replace_legacy": "true"}, "", "invalid_replace_legacy"},
	} {
		rec := httptest.NewRecorder()
		got, ok := sanitizeWizardCommandArgs(rec, "porthop_install", tc.args)
		if tc.want == "" {
			if ok || !strings.Contains(rec.Body.String(), tc.code) {
				t.Errorf("%s: ok=%v тело %s, ожидался отказ %s", tc.name, ok, rec.Body.String(), tc.code)
			}
			continue
		}
		if !ok || fmt.Sprint(got) != tc.want {
			t.Errorf("%s: ok=%v args=%v, want %s (тело %s)", tc.name, ok, got, tc.want, rec.Body.String())
		}
	}
	// Ровно восемь -- можно.
	rec := httptest.NewRecorder()
	if _, ok := sanitizeWizardCommandArgs(rec, "porthop_install", map[string]any{"ifaces": []any{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8"}}); !ok {
		t.Errorf("восемь имён отвергнуты: %s", rec.Body.String())
	}
}

// Через настоящий обработчик мини-аппа: оператор и владелец получают 404, как
// на других админских действиях; админ на старом агенте -- 409 agent_too_old;
// админ на v0.57.0 ставит в очередь ровно санитизированные аргументы.
func TestV057MiniappGates(t *testing.T) {
	_, ownedID, _, sink, h := maintenanceFleet(t, "v0.56.0")
	for _, a := range v057MaintenanceActions {
		for _, who := range []int64{100, 555} {
			rec := postMiniappCommand(t, h, ownedID, who, fmt.Sprintf(`{"action":%q}`, a))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s от %d: код %d тело %s, ожидался 404", a, who, rec.Code, rec.Body.String())
			}
		}
		rec := postMiniappCommand(t, h, ownedID, 999, fmt.Sprintf(`{"action":%q}`, a))
		if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte("agent_too_old")) {
			t.Errorf("%s на v0.56.0: код %d тело %s, ожидался 409 agent_too_old", a, rec.Code, rec.Body.String())
		}
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("в очередь ушло мимо гейтов: %+v", sink.enqueued)
	}

	d, ownedID, _, sink, h := maintenanceFleet(t, "v0.57.0")
	_ = d
	rec := postMiniappCommand(t, h, ownedID, 999, `{"action":"porthop_install","args":{"ifaces":["opkgtun10"],"replace_legacy":true,"sh":"x"}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("админ на v0.57.0: код %d тело %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 1 || fmt.Sprint(sink.enqueued[0].Args) != "map[ifaces:[opkgtun10] replace_legacy:true]" {
		t.Fatalf("в очереди %+v", sink.enqueued)
	}
	rec = postMiniappCommand(t, h, ownedID, 999, `{"action":"porthop_install","args":{"ifaces":["$(reboot)"]}}`)
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("invalid_ifaces")) {
		t.Fatalf("негодное имя: код %d тело %s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 1 {
		t.Fatal("негодное имя интерфейса ушло в очередь")
	}
}
