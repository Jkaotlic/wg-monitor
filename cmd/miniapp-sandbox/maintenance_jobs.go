package main

import (
	"sync"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Пакеты по расписанию на «роутере, которого нет»: состояние в памяти, ответы
// -- настоящими типами wire, пути -- те, что пишет агент
// (internal/agent/actions/opkg_cron.go, entware_clean.go). Обновление пакетов
// выключено, очистка включена -- экрану есть что показать в обоих видах.

type sandboxSchedule struct {
	installed bool
	schedule  string
}

var (
	sandboxScheduleMu sync.Mutex
	sandboxOpkg       = sandboxSchedule{}
	sandboxClean      = sandboxSchedule{installed: true, schedule: "05:15"}
)

func sandboxOpkgCron(action string, args map[string]any) wire.OpkgCronStatus {
	sandboxScheduleMu.Lock()
	defer sandboxScheduleMu.Unlock()
	switch action {
	case "opkg_cron_install":
		sandboxOpkg = sandboxSchedule{installed: true, schedule: argString(args, "schedule", "04:30")}
	case "opkg_cron_remove":
		sandboxOpkg = sandboxSchedule{}
	}
	st := wire.OpkgCronStatus{
		Installed:  sandboxOpkg.installed,
		ScriptPath: "/opt/etc/wg-monitor/opkg-auto-upgrade.sh",
		LogPath:    "/opt/var/log/wg-monitor/opkg-auto-upgrade.log",
		FreeKB:     812000,
		TotalKB:    1900000,
		MinFreeKB:  10 * 1024,
	}
	if sandboxOpkg.installed {
		st.Schedule, st.CronPath, st.CronService = sandboxOpkg.schedule, "root crontab", "running"
		st.LastRun, st.LastStatus = "2026-09-16 04:30:02", "ok"
	}
	if action == "opkg_cron_logs" || (action == "opkg_cron_status" && sandboxOpkg.installed) {
		st.LogTail = "2026-09-16 04:30:02 opkg update: ok\n2026-09-16 04:31:40 opkg upgrade: обновлено 3 пакета"
	}
	return st
}

func sandboxEntwareClean(action string, args map[string]any) wire.EntwareCleanStatus {
	sandboxScheduleMu.Lock()
	defer sandboxScheduleMu.Unlock()
	switch action {
	case "entware_clean_install":
		sandboxClean = sandboxSchedule{installed: true, schedule: argString(args, "schedule", "05:15")}
	case "entware_clean_remove":
		sandboxClean = sandboxSchedule{}
	}
	st := wire.EntwareCleanStatus{
		Installed:         sandboxClean.installed,
		ScriptPath:        "/opt/etc/wg-monitor/entware-cleanup.sh",
		LogPath:           "/opt/var/log/wg-monitor/entware-cleanup.log",
		FreeKB:            812000,
		TotalKB:           1900000,
		MinFreeKB:         2 * 1024,
		MemAvailableKB:    96000,
		MemTotalKB:        256000,
		MinMemAvailableKB: 16000,
	}
	if sandboxClean.installed {
		st.Schedule, st.CronPath, st.CronService = sandboxClean.schedule, "root crontab", "running"
		st.LastRun, st.LastStatus, st.LastFreedKB = "2026-09-16 05:15:01", "ok", 18432
	}
	if action == "entware_clean_run" {
		st.LastRun, st.LastStatus, st.LastFreedKB = "2026-09-17 12:00:00", "ok", 2048
	}
	if action == "entware_clean_logs" || action == "entware_clean_run" {
		st.LogTail = "2026-09-16 05:15:01 очищено 18 МБ\n2026-09-17 12:00:00 очищено 2 МБ"
	}
	return st
}
