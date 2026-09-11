package alerts

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ParseDiagReport разбирает отчёт awg-manager /api/diagnostics/result
// (версия "1.0") в сводку для владельца роутера. Возвращает:
//   - summary: ответ одной строкой — всё ли в порядке и сколько проверено;
//   - bullets: что именно не так, по VPN-туннелям с их именами;
//   - rawFallback: true, если JSON не разобрался или в нём нет ни одного
//     знакомого поля (вызывающий покажет отчёт сырым).
//
// Инженерия отчёта — версия панели, модуль ядра, интерфейсы WAN, журнал —
// владельцу не адресована и живёт в полном отчёте по кнопке. В сводке
// остаётся то, из чего человек делает вывод или действие.
func ParseDiagReport(raw string) (summary string, bullets []string, rawFallback bool) {
	var rep diagReportV1
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		return "", nil, true
	}
	if !rep.hasAnyDocumentedField() {
		return "", nil, true
	}
	return rep.renderSummary(), rep.renderBullets(), false
}

// diagReportV1 — то, что из отчёта нужно сводке. Форма проверена на живом
// awg-manager 2.18.2 (10.09.2026): проверки лежат плоским списком tests[].
// Прежний разбор ждал выдуманную форму tunnels → {id → {проверка}} и не
// находил ни одной.
type diagReportV1 struct {
	Version     string       `json:"version"`
	GeneratedAt string       `json:"generatedAt"`
	DurationMs  int64        `json:"durationMs"`
	System      diagSystem   `json:"system"`
	Tests       []diagTestV1 `json:"tests"`
}

type diagSystem struct {
	AppVersion   string        `json:"appVersion"`
	KernelModule diagKernelMod `json:"kernelModule"`
}

type diagKernelMod struct {
	Exists bool `json:"exists"`
	Loaded bool `json:"loaded"`
}

// diagTestV1 — одна проверка из tests[]. Проверки VPN-туннеля несут tunnelId
// и tunnelName; общие (связь с провайдером, часы роутера) — нет.
type diagTestV1 struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"` // pass | fail | skip | warn
	Detail      string `json:"detail"`
	TunnelID    string `json:"tunnelId"`
	TunnelName  string `json:"tunnelName"`
}

func (r diagReportV1) hasAnyDocumentedField() bool {
	return r.Version != "" || r.GeneratedAt != "" || r.DurationMs != 0 ||
		r.System.AppVersion != "" || r.System.KernelModule.Exists || r.System.KernelModule.Loaded ||
		len(r.Tests) > 0
}

func (r diagReportV1) renderSummary() string {
	took := ""
	if r.DurationMs > 0 {
		took = fmt.Sprintf(" за %d с", (r.DurationMs+500)/1000)
	}
	if len(r.Tests) == 0 {
		return "отчёт получен" + took
	}
	var passed, failed int
	for _, t := range r.Tests {
		switch normDiagStatus(t.Status) {
		case "ok", "warn":
			passed++
		case "fail":
			failed++
		}
	}
	// Тире, а не двоеточие: карточка уже печатает «Диагностика: …», и два
	// двоеточия подряд читаются как опечатка.
	if failed == 0 {
		return fmt.Sprintf("всё в порядке — проверено %d %s%s", passed, ruPlural(passed, "пункт", "пункта", "пунктов"), took) + r.renderStamp()
	}
	return fmt.Sprintf("нашлись проблемы — %d из %d%s", failed, passed+failed, took) + r.renderStamp()
}

// renderStamp -- когда снят отчёт. Агент v0.30 на «Диагностику» проверяет
// роутер заново, но старый агент или старая панель отдают ПОСЛЕДНИЙ отчёт, а
// он может быть многочасовой давности: без времени сводка выдаёт старое
// показание за свежее. Время -- роутера, в его поясе: так его видит
// владелец в родной панели. Нет времени или не разобралось -- молчим.
func (r diagReportV1) renderStamp() string {
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(r.GeneratedAt))
	if err != nil {
		return ""
	}
	return " · отчёт снят " + at.Format("02.01 в 15:04")
}

// renderBullets — только то, что не так. Что именно увидел роутер, живёт на
// странице проверки, на шаг глубже: в сводке сырая деталь владельцу ничего
// не скажет.
func (r diagReportV1) renderBullets() []string {
	var out []string
	if r.System.KernelModule.Exists && !r.System.KernelModule.Loaded {
		out = append(out, "⚠ модуль AmneziaWG не загружен — может понадобиться перезагрузка роутера")
	}
	for _, t := range r.Tests {
		st := normDiagStatus(t.Status)
		if st != "fail" && st != "warn" {
			continue
		}
		icon := "❌"
		if st == "warn" {
			icon = "⚠"
		}
		line := icon + " " + diagTestLabel(t.Name, t.Description)
		if name := diagTunnelLabel(t); name != "" {
			line += " — VPN-туннель «" + name + "»"
		}
		out = append(out, line)
	}
	return out
}

// TestDetail — одна проверка отчёта, собранная по всем VPN-туннелям. ID —
// имя проверки в отчёте ("awg_handshake"), он же едет в кнопку разбора.
type TestDetail struct {
	ID     string
	Label  string // словами владельца: «Обмен ключами свежий»
	Status string // ok | fail | skip | warn — худший по всем VPN-туннелям
	// Detail — что увидел роутер, у общей проверки без VPN-туннеля.
	Detail    string
	PerTunnel []PerTunnelDetail
}

// PerTunnelDetail — та же проверка на одном VPN-туннеле.
type PerTunnelDetail struct {
	TunnelLabel string // имя VPN-туннеля, каким его назвал владелец
	Status      string
	Reason      string // что увидел роутер
}

// ParseDiagTests собирает проверки отчёта по имени, в порядке отчёта:
// awg-manager ставит их от общего к частному, и этот порядок осмыслен.
// Возвращает nil, если JSON не разобрался; пустой список — если проверок в
// отчёте нет (старая панель).
func ParseDiagTests(raw string) []TestDetail {
	var rep diagReportV1
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		return nil
	}
	var order []string
	byName := map[string]*TestDetail{}
	for _, t := range rep.Tests {
		if t.Name == "" {
			continue
		}
		det, ok := byName[t.Name]
		if !ok {
			det = &TestDetail{ID: t.Name, Label: diagTestLabel(t.Name, t.Description)}
			byName[t.Name] = det
			order = append(order, t.Name)
		}
		st := normDiagStatus(t.Status)
		det.Status = worseDiagStatus(det.Status, st)
		if t.TunnelID == "" && strings.TrimSpace(t.TunnelName) == "" {
			det.Detail = strings.TrimSpace(t.Detail)
			continue
		}
		det.PerTunnel = append(det.PerTunnel, PerTunnelDetail{
			TunnelLabel: diagTunnelLabel(t),
			Status:      st,
			Reason:      strings.TrimSpace(t.Detail),
		})
	}
	out := make([]TestDetail, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out
}

// diagTestLabels — проверки awg-manager словами владельца. Описание из отчёта
// («Handshake свежий (<3 мин)», «Правила iptables») — язык движка; оно
// остаётся запасным для проверок, которых здесь ещё нет.
var diagTestLabels = map[string]string{
	"wan_connectivity":            "Интернет у провайдера",
	"ndms_health":                 "Система роутера отвечает",
	"kernel_module":               "Модуль AmneziaWG",
	"clock_skew":                  "Часы роутера",
	"direct_connectivity":         "Интернет напрямую",
	"singbox_runtime":             "Прокси-движок",
	"singbox_tunnel_connectivity": "Связь через прокси-движок",
	"dns_resolve":                 "Адрес сервера VPN-туннеля находится",
	"endpoint_reachable":          "Сервер VPN-туннеля отвечает",
	"endpoint_route_check":        "Путь до сервера VPN-туннеля",
	"awg_handshake":               "Обмен ключами свежий",
	"tunnel_connectivity":         "Интернет через VPN-туннель",
	"firewall_rules":              "Правила пропуска трафика",
	"config_parse":                "Настройки VPN-туннеля читаются",
	"interface_state_consistency": "Состояние VPN-туннеля согласовано",
	"mtu_check":                   "Размер пакета (MTU)",
	"proxy_health":                "Прокси-модуль AmneziaWG",
	"pingcheck_health":            "Проверка связи",
	"rp_filter":                   "Фильтр обратного пути",
	"route_leak_check":            "Лишние маршруты",
	"dns_leak_check":              "Запросы имён не утекают мимо VPN-туннеля",
	"restart_cycle":               "Перезапуск VPN-туннеля",
}

func diagTestLabel(name, description string) string {
	if l, ok := diagTestLabels[name]; ok {
		return l
	}
	if d := strings.TrimSpace(description); d != "" {
		return d
	}
	return name
}

// diagTunnelLabel — имя VPN-туннеля, каким его назвал владелец; без имени —
// идентификатор: выдумать имя нечем, а промолчать хуже.
func diagTunnelLabel(t diagTestV1) string {
	if n := strings.TrimSpace(t.TunnelName); n != "" {
		return n
	}
	return strings.TrimSpace(t.TunnelID)
}

// normDiagStatus сводит слова отчёта к ok | fail | skip | warn: awg-manager
// пишет pass, а экраны и кнопки говорят ok.
func normDiagStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "pass", "ok", "success":
		return "ok"
	case "fail", "failed", "error":
		return "fail"
	case "warn", "warning":
		return "warn"
	case "skip", "skipped":
		return "skip"
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// worseDiagStatus — худший из двух: провал на одном VPN-туннеле важнее
// успеха на остальных, а пропуск не перекрывает ни того, ни другого.
func worseDiagStatus(a, b string) string {
	rank := map[string]int{"": -1, "skip": 0, "ok": 1, "warn": 2, "fail": 3}
	ra, oka := rank[a]
	rb, okb := rank[b]
	if !oka {
		ra = 1
	}
	if !okb {
		rb = 1
	}
	if rb > ra {
		return b
	}
	return a
}

// ruPlural — русское склонение по числу: 1 пункт, 2 пункта, 5 пунктов.
func ruPlural(n int, one, few, many string) string {
	mod10, mod100 := n%10, n%100
	switch {
	case mod100 >= 11 && mod100 <= 14:
		return many
	case mod10 == 1:
		return one
	case mod10 >= 2 && mod10 <= 4:
		return few
	}
	return many
}
