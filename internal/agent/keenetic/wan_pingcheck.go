package keenetic

import (
	"regexp"
	"strconv"
	"strings"
)

// Ping-Check на линии провайдера (сверка С3, workrouter KeenOS 5.02.A.9,
// 28.09.2026 -- см. task-15-brief.md и task-15a-report.md). Профиль живёт
// внутри блока `interface <NDMS-имя>` вместе с приоритетом линии (`ip
// global <priority>`):
//
//	interface CdcEthernet0
//	    ip global 36405
//	    ping-check profile default
//	!
//
// Строку `ping-check profile <имя>` прошивка пишет, только если профиль
// назначен -- её отсутствие внутри блока с `ip global` значит «профиля нет»,
// не «не знаем». /api/wan/status awg-manager называет те же линии своими
// (kernel) именами и несёт тот же priority (сверено на живом роутере:
// GigabitEthernet1/58248 = eth3, CdcEthernet0/36405 = cdc_br0) -- это и есть
// ключ сопоставления, а не имена интерфейсов, которые на разных прошивках
// пишутся по-разному.
//
// Списки сайтов прошивки (`object-group fqdn`, `dns-proxy route`) в этом
// файле не разбираются -- то отдельная задача следующего цикла.
var (
	reWANIPGlobal  = regexp.MustCompile(`^\s+ip\s+global\s+(\d+)\s*$`)
	reWANPingCheck = regexp.MustCompile(`^\s+ping-check\s+profile\s+(\S+)\s*$`)
)

// ParsePingCheckByPriority walks running-config `interface` blocks and
// returns, for every interface that carries a priority (`ip global N`), its
// bound Ping-Check profile -- "" if the block has no `ping-check profile`
// line. Interfaces without `ip global` (not WAN-participating, e.g. LAN or
// Wireguard* VPN interfaces) are absent from the result. Unrecognised text
// (old firmware, truncated output) simply yields no entries -- it never
// panics.
func ParsePingCheckByPriority(runningConfig string) map[int]string {
	out := map[int]string{}
	priority, profile, have := 0, "", false
	flush := func() {
		if have {
			out[priority] = profile
		}
		priority, profile, have = 0, "", false
	}
	for _, raw := range strings.Split(runningConfig, "\n") {
		line := strings.TrimRight(raw, "\r")
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			// Top-level line: `interface X`, `!`, or an unrelated top-level
			// block -- any of these end the interface block we were in.
			flush()
			continue
		}
		if m := reWANIPGlobal.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				priority, have = n, true
			}
			continue
		}
		if m := reWANPingCheck.FindStringSubmatch(line); m != nil {
			profile = m[1]
		}
	}
	flush()
	return out
}

// WANPingCheckByPriority joins the priority→profile map from running-config
// to WAN link kernel names, given the (kernel name → priority) map the agent
// already builds from /api/wan/status (wanfacts.Links). Only priorities
// present in that map are ever looked up, so OpkgTun*/other non-WAN
// interfaces in running-config -- which also carry `ip global` but whose
// priority never appears in /api/wan/status -- are never joined. Priority 0
// (not participating) is skipped. A link whose priority has no match in
// running-config (unreadable config, no match, old firmware) is simply
// absent from the result -- the caller (wanfacts.Collector) leaves its
// PingCheck nil, never a failure.
func WANPingCheckByPriority(runningConfig string, priorities map[string]int) map[string]string {
	byPriority := ParsePingCheckByPriority(runningConfig)
	out := map[string]string{}
	for name, p := range priorities {
		if p <= 0 {
			continue
		}
		if profile, ok := byPriority[p]; ok {
			out[name] = profile
		}
	}
	return out
}
