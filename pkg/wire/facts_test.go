package wire

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Отчёт без новых полей обязан выглядеть на проводе ровно как отчёт v0.46:
// бэкенд v0.46 и тесты контракта сравнивают его побайтно по ключам.
func TestReportWithoutFactsOmitsNewKeys(t *testing.T) {
	b, err := json.Marshal(Report{Timestamp: time.Unix(0, 0).UTC(), AgentVersion: "v0.47.0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"facts"`, `"trigger"`} {
		if strings.Contains(string(b), key) {
			t.Fatalf("пустой отчёт несёт %s: %s", key, b)
		}
	}
}

// Бэкенд v0.46 знает только четыре поля отчёта. Отчёт v0.47 с фактами и
// триггером обязан разбираться его типом без ошибки и без потерь в известном.
func TestOldBackendDecodesNewReport(t *testing.T) {
	type v046Check struct {
		Name       string         `json:"name"`
		Status     string         `json:"status"`
		DurationMs int64          `json:"duration_ms"`
		Details    map[string]any `json:"details,omitempty"`
	}
	type v046Report struct {
		Timestamp    time.Time   `json:"ts"`
		AgentVersion string      `json:"agent_version"`
		Checks       []v046Check `json:"checks"`
		Resumed      bool        `json:"resumed,omitempty"`
	}
	changed := true
	b, _ := json.Marshal(Report{
		Timestamp:    time.Date(2026, 9, 28, 7, 0, 0, 0, time.UTC),
		AgentVersion: "v0.47.0",
		Checks:       []Check{{Name: "dns", Status: "ok"}},
		Trigger:      TriggerHook,
		Facts: &ReportFacts{Exit: &ExitFacts{At: time.Now().UTC(), Tunnels: map[string]ExitProbe{
			"awg11": {VPNIP: "203.0.113.7", DirectIP: "198.51.100.4", Changed: &changed, Source: ExitSourceAwgm, At: time.Now().UTC()},
		}}},
	})
	var old v046Report
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatalf("бэкенд v0.46 не разобрал отчёт v0.47: %v", err)
	}
	if old.AgentVersion != "v0.47.0" || len(old.Checks) != 1 || old.Checks[0].Name != "dns" {
		t.Fatalf("известные поля потерялись: %+v", old)
	}
}

// Агент v0.46 разбирает ответ бэкенда v0.47 своим типом: лишний ключ ему не мешает.
func TestReportResponseHookReportsOmitEmpty(t *testing.T) {
	b, _ := json.Marshal(ReportResponse{})
	if string(b) != "{}" {
		t.Fatalf("пустой ответ = %s, хотим {}", b)
	}
	b, _ = json.Marshal(ReportResponse{HookReports: true})
	if !strings.Contains(string(b), `"hook_reports":true`) {
		t.Fatalf("ответ = %s", b)
	}
}

func TestEmptyFacts(t *testing.T) {
	if !(&ReportFacts{}).Empty() {
		t.Fatal("пустой блок обязан быть Empty")
	}
	if (&ReportFacts{PingRuns: []PingRun{{TunnelID: "awg11"}}}).Empty() {
		t.Fatal("блок с сериями не пуст")
	}
}

// Худший роутер: всё сверх потолков. После Clamp блок обязан влезть в 16 КиБ.
func TestFactsClampFitsBudget(t *testing.T) {
	long := strings.Repeat("я", 500)
	now := time.Date(2026, 9, 28, 7, 0, 0, 0, time.UTC)
	f := &ReportFacts{
		Exit:      &ExitFacts{At: now, Tunnels: map[string]ExitProbe{}},
		PingLog:   &PingLogFacts{At: now, State: "ok"},
		WAN:       &WANFacts{At: now},
		NativeDNS: &NativeDNSFacts{At: now},
		Hooks:     &HookFacts{At: now, State: "installed", Err: long},
	}
	changed := false
	for i := 0; i < 30; i++ {
		id := "awg" + strings.Repeat("9", 2) + string(rune('a'+i%26)) + string(rune('a'+i/26))
		f.Exit.Tunnels[id] = ExitProbe{VPNIP: "203.0.113.200", DirectIP: "198.51.100.200", EndpointIP: "203.0.113.201", Changed: &changed, Source: ExitSourceAgent, At: now, Err: long}
		f.PingLog.Tunnels = append(f.PingLog.Tunnels, id)
	}
	for i := 0; i < 50; i++ {
		f.PingRuns = append(f.PingRuns, PingRun{TunnelID: "awg11", TunnelName: long, From: now.Add(time.Duration(i) * time.Minute), To: now, Fails: 99, WentDown: true, Error: long})
		f.NativeDNS.Lists = append(f.NativeDNS.Lists, NativeDNSList{Name: long, Domains: 999, Target: long, TunnelID: "awg11", Mode: "auto", Owner: "firmware", Issue: "target_down"})
	}
	for i := 0; i < 20; i++ {
		f.WAN.Links = append(f.WAN.Links, WANLink{Name: long, Label: long, Role: "backup", Priority: 1})
	}
	f.Clamp()
	// Потолки полей — верхняя граница числа элементов, но не гарантия бюджета
	// байтов: при всех потолках разом заполненными (16 туннелей, 20 серий,
	// 8 линков WAN, 40 списков DNS, тексты по 80 рун кириллицей) сырой JSON
	// уходит далеко за 16 КиБ. Exit/WAN/PingLog в этот бюджет укладываются
	// сами по себе и потолков не теряют; NativeDNS.Lists и PingRuns Clamp
	// досекает с хвоста, пока блок не влезет — для них проверяем «не больше
	// потолка», а не точное равенство.
	if len(f.Exit.Tunnels) != MaxExitTunnels || len(f.PingRuns) > MaxPingRuns ||
		len(f.WAN.Links) != MaxWANLinks || len(f.NativeDNS.Lists) > MaxNativeDNSLists || len(f.PingLog.Tunnels) != MaxExitTunnels {
		t.Fatalf("потолки: exit=%d runs=%d links=%d lists=%d pinglog=%d",
			len(f.Exit.Tunnels), len(f.PingRuns), len(f.WAN.Links), len(f.NativeDNS.Lists), len(f.PingLog.Tunnels))
	}
	b, _ := json.Marshal(f)
	if len(b) > 16*1024 {
		t.Fatalf("блок фактов %d байт > 16 КиБ", len(b))
	}
	if strings.Contains(string(b), long) {
		t.Fatal("длинный текст не обрезан")
	}
}

func TestNewActionsAreValid(t *testing.T) {
	for _, a := range []string{"exit_ip_probe", "awgm_logs"} {
		if !IsValidCommandAction(a) {
			t.Errorf("%s нет в validCommandActions", a)
		}
	}
}

// Clamp обязан не трогать карту вызывающего: агент (Task 2) может держать тот
// же map[string]ExitProbe как свой «последний инвентарь» и передать его прямо
// в Report.Facts.Exit.Tunnels. Ни delete(), ни запись по ключу в исходную
// карту не допускаются (постановление P1) -- Clamp обязан построить новую
// карту для отправляемого блока.
func TestClampExitTunnelsDoesNotMutateCallerMap(t *testing.T) {
	now := time.Date(2026, 9, 28, 7, 0, 0, 0, time.UTC)
	original := make(map[string]ExitProbe, MaxExitTunnels+5)
	for i := 0; i < MaxExitTunnels+5; i++ {
		id := "awg" + string(rune('a'+i))
		original[id] = ExitProbe{VPNIP: "203.0.113.1", Source: ExitSourceAwgm, At: now}
	}
	// Снимок значений исходной карты до Clamp -- сравниваем с ним после.
	wantLen := len(original)
	wantCopy := make(map[string]ExitProbe, len(original))
	for id, p := range original {
		wantCopy[id] = p
	}

	f := &ReportFacts{Exit: &ExitFacts{At: now, Tunnels: original}}
	f.Clamp()

	if len(original) != wantLen {
		t.Fatalf("исходная карта изменила длину: было %d, стало %d", wantLen, len(original))
	}
	for id, want := range wantCopy {
		got, ok := original[id]
		if !ok {
			t.Fatalf("исходная карта потеряла ключ %q", id)
		}
		if got != want {
			t.Fatalf("исходная карта изменила значение %q: было %+v, стало %+v", id, want, got)
		}
	}

	if len(f.Exit.Tunnels) != MaxExitTunnels {
		t.Fatalf("результат Clamp не обрезан: %d туннелей, хотим %d", len(f.Exit.Tunnels), MaxExitTunnels)
	}
}
