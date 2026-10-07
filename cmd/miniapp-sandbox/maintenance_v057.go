package main

import (
	"encoding/json"
	"sync"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// v0.57 на «роутере, которого нет»: смена порта при блокировке, отчёт о
// месте и эталонный DNS с пробами. Ответы -- настоящими типами wire, пути --
// те, что пишет агент (internal/agent/actions/porthop.go).
//
// Смена порта начинается с ручной копии оператора: экрану есть что показать
// -- предупреждение и «Заменить ручную копию», после замены -- куда копия
// перенесена. Место после «Почистить сейчас» прибавляется, чтобы итог
// «освобождено» было видно.

var (
	sandboxV057Mu     sync.Mutex
	sandboxPorthopOn  bool
	sandboxLegacyGone bool
	sandboxFreeKB     int64 = 41 * 1024
)

const (
	sandboxLegacyInit  = "/opt/etc/init.d/S99awg-porthop"
	sandboxLegacyMoved = "/opt/etc/wg-monitor/legacy/S99awg-porthop"
)

func sandboxPorthop(action string, args map[string]any) (wire.PorthopStatus, string) {
	sandboxV057Mu.Lock()
	defer sandboxV057Mu.Unlock()
	switch action {
	case "porthop_install":
		if !sandboxLegacyGone {
			if replace, _ := args["replace_legacy"].(bool); !replace {
				return wire.PorthopStatus{}, "legacy_running: на роутере есть ручная копия смены порта (" + sandboxLegacyInit + ") — две копии дрались бы за один VPN-туннель. Ничего не изменено; чтобы заменить её, повторите установку с заменой ручной копии"
			}
			sandboxLegacyGone = true
		}
		sandboxPorthopOn = true
	case "porthop_remove":
		sandboxPorthopOn = false
	}
	st := wire.PorthopStatus{
		Installed:  sandboxPorthopOn,
		Running:    sandboxPorthopOn,
		Auto:       true,
		Watched:    []string{"opkgtun10", "opkgtun12"},
		ScriptPath: "/opt/etc/wg-monitor/awg-porthop.sh",
		ConfPath:   "/opt/etc/wg-monitor/porthop.conf",
		LogPath:    "/opt/var/log/wg-monitor/porthop.log",
	}
	if sandboxLegacyGone {
		st.Legacy = wire.PorthopLegacy{Path: sandboxLegacyInit, MovedTo: sandboxLegacyMoved}
	} else {
		st.Legacy = wire.PorthopLegacy{Found: true, Path: sandboxLegacyInit, Running: true}
	}
	if sandboxPorthopOn {
		st.Hops24h, st.Recovered24h, st.Failed24h = 3, 2, 1
		st.LastEvent = "2026-10-07 12:00:00 +0300 opkgtun10: порт 30000 -> 41234, поток ожил (хендшейк 3 с)"
	}
	if action == "porthop_logs" {
		st.LogTail = "2026-10-07 11:40:00 +0300 opkgtun10: listen-port 38211 не сработал (rc=1), пира возвращаю\n" + st.LastEvent
	}
	return st, ""
}

func sandboxSpace() wire.SpaceReport {
	sandboxV057Mu.Lock()
	defer sandboxV057Mu.Unlock()
	return wire.SpaceReport{
		FreeKB:  sandboxFreeKB,
		TotalKB: 1900000,
		Top: []wire.SpaceEntry{
			{Path: "/opt/var/log", KB: 312000},
			{Path: "/opt/lib", KB: 254000},
			{Path: "/opt/var/cache/opkg", KB: 98000},
			{Path: "/opt/tmp", KB: 30000},
		},
	}
}

// sandboxCleanFreed -- «Почистить сейчас» прибавляет место (30 МБ).
func sandboxCleanFreed() {
	sandboxV057Mu.Lock()
	defer sandboxV057Mu.Unlock()
	sandboxFreeKB += 30 * 1024
}

// Пробы эталона: 1.1.1.1 не отвечает -- экран показывает пропущенный сервер.
var sandboxDNSProbes = []wire.DNSProbe{
	{Server: "9.9.9.9", Purpose: "foreign", OK: true},
	{Server: "1.1.1.1", Purpose: "foreign", Error: "нет ответа за 5s"},
	{Server: "common.dot.dns.yandex.net", Purpose: "ru", OK: true},
}

const sandboxDNSProbeText = "проверка доступности эталона (3):\n" +
	"  ✓ 9.9.9.9 — отвечает\n" +
	"  ✗ 1.1.1.1 — не отвечает: нет ответа за 5s\n" +
	"  ✓ common.dot.dns.yandex.net — отвечает\n" +
	"сервер не отвечает, не ставим (1):\n" +
	"  · tls upstream 1.1.1.1 sni cloudflare-dns.com\n\n"

func sandboxDNSReset(args map[string]any) string {
	if dry, _ := args["dry_run"].(bool); dry {
		return "Предпросмотр сброса DNS — ничего не изменено\n\n" + sandboxDNSProbeText +
			"Уберём (1):\n  − tls upstream 8.8.8.8 sni dns.google\n\n" +
			"Заменим на эталонные (7):\n  + tls upstream 9.9.9.9 sni dns.quad9.net\n"
	}
	return "DNS reset → reference DoT\n\n" + sandboxDNSProbeText + "снимок «до»: /opt/etc/wg-monitor/dns-before-1791360000.txt\n"
}

// sandboxResult -- статус, вывод и Payload ответа: часть команд v0.57
// отказывает (legacy_running) или несёт Payload (dns_reset).
func sandboxResult(routerID int64, action string, args map[string]any) (string, string, json.RawMessage) {
	switch action {
	case "porthop_status", "porthop_install", "porthop_remove", "porthop_logs":
		st, refused := sandboxPorthop(action, args)
		if refused != "" {
			return "err", refused, nil
		}
		return "ok", mustJSON(st), nil
	case "space_report":
		return "ok", mustJSON(sandboxSpace()), nil
	case "dns_reset":
		payload, _ := json.Marshal(wire.DNSResetResult{Probes: sandboxDNSProbes})
		return "ok", sandboxDNSReset(args), payload
	case "entware_clean_run":
		sandboxCleanFreed()
	}
	return "ok", sandboxOutput(routerID, action, args), nil
}
