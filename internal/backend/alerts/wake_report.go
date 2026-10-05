package alerts

import (
	"fmt"
	"strings"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// RenderWakeReport produces the adaptive wake-card for a mobile router that
// just rejoined (Report.Resumed=true). Health checks all green → one-line
// "🚗 в сети — всё ок"; no health checks yet → "жду проверки сервисов"; any
// failures → "🚗⚠ есть проблемы" with bullet list of failing check names.
// agent_heartbeat is always excluded from the failure tally — it's a transport
// check, not a router-health signal.
func RenderWakeReport(nickname string, checks []wire.Check) Card {
	var failed []wire.Check
	healthChecks := 0
	for _, c := range checks {
		if c.Name == "agent_heartbeat" {
			continue
		}
		healthChecks++
		if c.Status != "ok" {
			failed = append(failed, c)
		}
	}
	// Отчёт уходит владельцу в личку, и подсказка ведёт туда, где он может
	// действовать: приложение, той же кнопкой, что под тревогами. Панелей
	// бота («📊 Что происходит?») у владельца нет.
	if healthChecks == 0 {
		return Card{
			Badge:   "🚗⏳",
			Summary: fmt.Sprintf("%s в сети, жду проверки сервисов", nickname),
			Hint:    "Если через минуту статус не обновится, откройте роутер в приложении.",
		}
	}
	if len(failed) == 0 {
		return Card{
			Badge:   "🚗",
			Summary: fmt.Sprintf("%s в сети — всё ок", nickname),
		}
	}
	var b strings.Builder
	names := make([]string, 0, len(failed))
	for i, c := range failed {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "• %s", wakeCheckLabel(c))
		names = append(names, c.Name)
	}
	if allWarmupFailures(names) {
		return Card{
			Badge:   "🚗⏳",
			Summary: fmt.Sprintf("%s в сети, сервисы ещё поднимаются", nickname),
			Details: b.String(),
			Hint:    "Подождите минуту-другую и откройте роутер в приложении — там видно, что именно не работает.",
		}
	}
	return Card{
		Badge:   "🚗⚠",
		Summary: fmt.Sprintf("%s в сети, есть проблемы", nickname),
		Details: b.String(),
		Hint:    "Откройте приложение — там видно, что именно не работает и что можно починить.",
	}
}

func allWarmupFailures(names []string) bool {
	if len(names) == 0 {
		return false
	}
	for _, name := range names {
		if !isWarmupCheck(name) {
			return false
		}
	}
	return true
}

func isWarmupCheck(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "dns", "dns_via_tunnel", "hydraroute", "awg_manager", "tunnels", "external_reach":
		return true
	default:
		return strings.HasPrefix(name, "dns_") || strings.HasPrefix(name, "tunnel_")
	}
}

// wakeName -- подпись проверки из общей таблицы (check_names.json) для
// середины фразы: своих имён у «ночного отчёта» нет.
func wakeName(key string) string { return lowerFirst(CheckNames[key]) }

// wakeCheckLabel -- что не так, словами приложения: VPN-туннель с именем
// владельца, остальное -- имена из общей таблицы подписей.
func wakeCheckLabel(c wire.Check) string {
	switch c.Name {
	case "tunnels":
		return wakeName("tunnels") + " не читается"
	case "dns_via_tunnel":
		return wakeName("dns") + " не отвечает"
	}
	switch checkCategory(c.Name) {
	case "tunnel":
		if name := strings.TrimSpace(strOrEmpty(c.Details, "tunnel_name")); name != "" {
			return "VPN-туннель «" + name + "» не на связи"
		}
		return "VPN-туннель не на связи"
	case "dns":
		return wakeName("dns") + " не отвечает"
	case "hydraroute":
		return wakeName("hydraroute") + " не работает"
	case "awg_manager", "awgmgr_api":
		return wakeName("awg_manager") + " не отвечает"
	case "external_reach":
		return wakeName("external_reach") + " не работает"
	case "dns_ru":
		return dnsRuHumanName + " не отвечает"
	case "resolver_guard":
		if resolverGuardForeignLeftover(c.Details) {
			return "запасные DNS-серверы не снялись" // отвечает ли свой -- не знаем
		}
		return wakeName("resolver_guard") + " не отвечает"
	default:
		return c.Name
	}
}
