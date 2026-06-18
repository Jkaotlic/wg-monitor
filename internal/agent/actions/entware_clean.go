package actions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

const (
	defaultEntwareCleanScriptPath        = "/opt/etc/wg-monitor/entware-cleanup.sh"
	defaultEntwareCleanLogPath           = "/opt/var/log/wg-monitor/entware-cleanup.log"
	defaultEntwareCleanSchedule          = "15 5 * * *"
	defaultEntwareCleanMinFreeKB         = int64(2 * 1024)
	defaultEntwareCleanMinMemAvailableKB = int64(64 * 1024)
	defaultEntwareCleanMaxLogKB          = int64(64)

	entwareCleanBegin = "# wg-monitor entware cleanup begin"
	entwareCleanEnd   = "# wg-monitor entware cleanup end"
)

type EntwareCleanManager struct {
	Exec              ExecFunc
	Now               func() time.Time
	ScriptPath        string
	LogPath           string
	MinFreeKB         int64
	MinMemAvailableKB int64
	MaxLogKB          int64
}

func (m *EntwareCleanManager) Install(ctx context.Context, schedule string) (wire.EntwareCleanStatus, error) {
	sched, err := NormalizeEntwareCleanSchedule(schedule)
	if err != nil {
		return wire.EntwareCleanStatus{}, err
	}
	free, _, err := m.dfOpt(ctx)
	if err != nil {
		return wire.EntwareCleanStatus{}, err
	}
	if free < m.minFreeKB() {
		return wire.EntwareCleanStatus{}, fmt.Errorf("not enough free space on /opt: free %d KB, need at least %d KB", free, m.minFreeKB())
	}
	if _, err := ensureCronInstalled(ctx, m.exec); err != nil {
		return wire.EntwareCleanStatus{}, err
	}
	if err := os.MkdirAll(filepath.Dir(m.scriptPath()), 0o755); err != nil {
		return wire.EntwareCleanStatus{}, fmt.Errorf("create script dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.logPath()), 0o755); err != nil {
		return wire.EntwareCleanStatus{}, fmt.Errorf("create log dir: %w", err)
	}
	if err := os.WriteFile(m.scriptPath(), []byte(m.scriptText()), 0o755); err != nil {
		return wire.EntwareCleanStatus{}, fmt.Errorf("write script: %w", err)
	}
	current, err := m.readCrontab(ctx)
	if err != nil {
		return wire.EntwareCleanStatus{}, err
	}
	next := replaceEntwareCleanCronBlock(current, sched, m.scriptPath())
	if err := m.installCrontab(ctx, next); err != nil {
		return wire.EntwareCleanStatus{}, err
	}
	_, _ = m.exec(ctx, "/opt/etc/init.d/S10cron", "start")
	return m.Status(ctx, 40)
}

func (m *EntwareCleanManager) Status(ctx context.Context, tailLines int) (wire.EntwareCleanStatus, error) {
	free, total, _ := m.dfOpt(ctx)
	memAvailable, memTotal := m.memInfo(ctx)
	crontab, cronErr := m.readCrontab(ctx)
	installed, schedule := parseEntwareCleanCron(crontab, m.scriptPath())
	if _, err := os.Stat(m.scriptPath()); err != nil {
		installed = false
	}
	tail := m.readLogTail(tailLines)
	lastRun, lastStatus, lastFreed := parseEntwareCleanLogTail(tail)
	cronService := "available"
	if cronErr != nil {
		cronService = "unavailable: " + cronErr.Error()
	}
	return wire.EntwareCleanStatus{
		Installed:         installed,
		Schedule:          schedule,
		ScriptPath:        m.scriptPath(),
		CronPath:          "root crontab",
		LogPath:           m.logPath(),
		CronService:       cronService,
		FreeKB:            free,
		TotalKB:           total,
		MinFreeKB:         m.minFreeKB(),
		MemAvailableKB:    memAvailable,
		MemTotalKB:        memTotal,
		MinMemAvailableKB: m.minMemAvailableKB(),
		LastRun:           lastRun,
		LastStatus:        lastStatus,
		LastFreedKB:       lastFreed,
		LogTail:           tail,
	}, nil
}

func (m *EntwareCleanManager) Run(ctx context.Context) (wire.EntwareCleanStatus, error) {
	if _, err := os.Stat(m.scriptPath()); err != nil {
		return wire.EntwareCleanStatus{}, fmt.Errorf("managed cleanup script is not installed: %w", err)
	}
	if out, err := m.exec(ctx, m.scriptPath()); err != nil {
		return wire.EntwareCleanStatus{}, fmt.Errorf("run cleanup script: %w\n%s", err, string(out))
	}
	return m.Status(ctx, 80)
}

func (m *EntwareCleanManager) Logs(ctx context.Context, tailLines int) (wire.EntwareCleanStatus, error) {
	return m.Status(ctx, tailLines)
}

func (m *EntwareCleanManager) Remove(ctx context.Context) (wire.EntwareCleanStatus, error) {
	current, err := m.readCrontab(ctx)
	if err != nil {
		return wire.EntwareCleanStatus{}, err
	}
	next := stripEntwareCleanCronBlock(current)
	if err := m.installCrontab(ctx, next); err != nil {
		return wire.EntwareCleanStatus{}, err
	}
	if err := os.Remove(m.scriptPath()); err != nil && !os.IsNotExist(err) {
		return wire.EntwareCleanStatus{}, fmt.Errorf("remove script: %w", err)
	}
	return m.Status(ctx, 40)
}

func NormalizeEntwareCleanSchedule(in string) (string, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return defaultEntwareCleanSchedule, nil
	}
	if strings.Count(s, ":") == 1 && !strings.Contains(s, " ") {
		parts := strings.Split(s, ":")
		hour, hErr := strconv.Atoi(parts[0])
		min, mErr := strconv.Atoi(parts[1])
		if hErr != nil || mErr != nil || hour < 0 || hour > 23 || min < 0 || min > 59 {
			return "", fmt.Errorf("invalid schedule %q, want HH:MM or five-field cron", in)
		}
		return fmt.Sprintf("%d %d * * *", min, hour), nil
	}
	fields := strings.Fields(s)
	if len(fields) != 5 {
		return "", fmt.Errorf("invalid schedule %q, want HH:MM or five-field cron", in)
	}
	if !cronScheduleFieldsLookSafe(fields) {
		return "", fmt.Errorf("invalid schedule %q, cron fields may contain only digits, '*', '/', '-' and ','", in)
	}
	return strings.Join(fields, " "), nil
}

func (m *EntwareCleanManager) readCrontab(ctx context.Context) (string, error) {
	out, err := m.exec(ctx, "crontab", "-l")
	text := string(out)
	if err == nil {
		return text, nil
	}
	lower := strings.ToLower(text + err.Error())
	if strings.Contains(lower, "no crontab") || strings.Contains(lower, "no cron table") {
		return "", nil
	}
	return text, fmt.Errorf("crontab -l: %w\n%s", err, text)
}

func (m *EntwareCleanManager) installCrontab(ctx context.Context, content string) error {
	tmp, err := os.CreateTemp("", "wg-monitor-entware-clean-*.tab")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	out, err := m.exec(ctx, "crontab", tmp.Name())
	if err != nil {
		return fmt.Errorf("crontab install: %w\n%s", err, string(out))
	}
	return nil
}

func replaceEntwareCleanCronBlock(current, schedule, scriptPath string) string {
	base := strings.TrimRight(stripEntwareCleanCronBlock(current), "\n")
	block := entwareCleanBegin + "\n" + schedule + " " + scriptPath + "\n" + entwareCleanEnd
	if base == "" {
		return block + "\n"
	}
	return base + "\n" + block + "\n"
}

func stripEntwareCleanCronBlock(current string) string {
	var out []string
	inBlock := false
	for _, line := range strings.Split(current, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == entwareCleanBegin {
			inBlock = true
			continue
		}
		if trimmed == entwareCleanEnd {
			inBlock = false
			continue
		}
		if !inBlock {
			out = append(out, line)
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}

func parseEntwareCleanCron(crontab, scriptPath string) (bool, string) {
	inBlock := false
	for _, line := range strings.Split(crontab, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == entwareCleanBegin {
			inBlock = true
			continue
		}
		if trimmed == entwareCleanEnd {
			return false, ""
		}
		if !inBlock || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 6 && fields[5] == scriptPath {
			return true, strings.Join(fields[:5], " ")
		}
	}
	return false, ""
}

func (m *EntwareCleanManager) readLogTail(lines int) string {
	data, err := os.ReadFile(m.logPath())
	if err != nil {
		return ""
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if lines > 0 && len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

func parseEntwareCleanLogTail(tail string) (lastRun, lastStatus string, lastFreedKB int64) {
	lines := strings.Split(strings.TrimSpace(tail), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			if ts, err := time.Parse(time.RFC3339, fields[0]); err == nil {
				lastRun = ts.UTC().Format(time.RFC3339)
			}
		}
		status := "unknown"
		for _, field := range fields[1:] {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch key {
			case "status":
				switch {
				case value == "ok":
					status = "ok"
				case strings.Contains(value, "skipped"):
					status = "skipped"
				case strings.Contains(value, "failed") || strings.Contains(value, "error"):
					status = "err"
				default:
					status = value
				}
			case "freed_kb":
				lastFreedKB, _ = strconv.ParseInt(value, 10, 64)
			}
		}
		lastStatus = status
		return lastRun, lastStatus, lastFreedKB
	}
	return "", "", 0
}

func (m *EntwareCleanManager) dfOpt(ctx context.Context) (free, total int64, err error) {
	out, err := m.exec(ctx, "df", "-k", "/opt")
	if err != nil {
		return 0, 0, fmt.Errorf("df /opt: %w\n%s", err, string(out))
	}
	free, total, err = parseDfOptOutput(out)
	if err != nil {
		return 0, 0, err
	}
	return free, total, nil
}

func (m *EntwareCleanManager) memInfo(ctx context.Context) (available, total int64) {
	out, err := m.exec(ctx, "cat", "/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseInt(fields[1], 10, 64)
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemAvailable":
			available = value
		case "MemTotal":
			total = value
		}
	}
	return available, total
}

func (m *EntwareCleanManager) scriptText() string {
	return fmt.Sprintf(`#!/bin/sh
set -u
PATH=/opt/bin:/opt/sbin:/usr/bin:/usr/sbin:/bin:/sbin
LOG=%q
LOCK=/tmp/wg-monitor-entware-cleanup.lock
MIN_FREE_KB=%d
MIN_MEM_AVAILABLE_KB=%d
MAX_LOG_KB=%d

mkdir -p "$(dirname "$LOG")"

log() {
  printf '%%s %%s\n' "$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)" "$*" >> "$LOG"
}

trim_log() {
  max_bytes=$((MAX_LOG_KB * 1024))
  [ -f "$LOG" ] || return 0
  size=$(wc -c < "$LOG" 2>/dev/null || echo 0)
  [ "$size" -le "$max_bytes" ] && return 0
  tmp="${LOG}.tmp"
  tail -c "$max_bytes" "$LOG" > "$tmp" 2>/dev/null && mv "$tmp" "$LOG"
}

opt_free_kb() {
  df -k /opt | awk 'NR==2 {print $4}'
}

mem_available_kb() {
  awk '/^MemAvailable:/ {print $2}' /proc/meminfo 2>/dev/null
}

clean_dir_children() {
  dir="$1"
  [ -d "$dir" ] || return 0
  find "$dir" -mindepth 1 -maxdepth 1 -mtime +1 -exec rm -rf -- {} + 2>/dev/null || true
}

clean_old_files() {
  dir="$1"
  [ -d "$dir" ] || return 0
  find "$dir" -type f -mtime +1 -delete 2>/dev/null || true
}

if ! mkdir "$LOCK" 2>/dev/null; then
  log "status=locked another run is active"
  trim_log
  exit 0
fi
trap 'rmdir "$LOCK" 2>/dev/null || true' EXIT INT TERM

free="$(opt_free_kb)"
case "$free" in
  ''|*[!0-9]*) log "status=skipped_bad_df free=$free"; trim_log; exit 0 ;;
esac
if [ "$free" -lt "$MIN_FREE_KB" ]; then
  log "status=skipped_low_space free_kb=$free min_free_kb=$MIN_FREE_KB"
  trim_log
  exit 0
fi

before="$(mem_available_kb)"
case "$before" in
  ''|*[!0-9]*) before=0 ;;
esac

log "status=start mem_before_kb=$before opt_free_kb=$free"
clean_dir_children /opt/tmp
clean_dir_children /opt/var/tmp
clean_old_files /opt/var/cache/opkg
clean_old_files /opt/var/opkg-lists

sync
if [ -w /proc/sys/vm/drop_caches ] && [ "$before" -gt 0 ] && [ "$before" -lt "$MIN_MEM_AVAILABLE_KB" ]; then
  echo 3 > /proc/sys/vm/drop_caches 2>/dev/null || true
fi

after="$(mem_available_kb)"
case "$after" in
  ''|*[!0-9]*) after=0 ;;
esac
freed=0
if [ "$before" -gt 0 ] && [ "$after" -gt "$before" ]; then
  freed=$((after - before))
fi
free_after="$(opt_free_kb)"
log "status=ok mem_before_kb=$before mem_after_kb=$after freed_kb=$freed opt_free_kb=$free_after"
trim_log
`, m.logPath(), m.minFreeKB(), m.minMemAvailableKB(), m.maxLogKB())
}

func (m *EntwareCleanManager) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.Exec != nil {
		return m.Exec(ctx, name, args...)
	}
	return DefaultExec(ctx, name, args...)
}

func (m *EntwareCleanManager) scriptPath() string {
	if m.ScriptPath != "" {
		return m.ScriptPath
	}
	return defaultEntwareCleanScriptPath
}

func (m *EntwareCleanManager) logPath() string {
	if m.LogPath != "" {
		return m.LogPath
	}
	return defaultEntwareCleanLogPath
}

func (m *EntwareCleanManager) minFreeKB() int64 {
	if m.MinFreeKB > 0 {
		return m.MinFreeKB
	}
	return defaultEntwareCleanMinFreeKB
}

func (m *EntwareCleanManager) minMemAvailableKB() int64 {
	if m.MinMemAvailableKB > 0 {
		return m.MinMemAvailableKB
	}
	return defaultEntwareCleanMinMemAvailableKB
}

func (m *EntwareCleanManager) maxLogKB() int64 {
	if m.MaxLogKB > 0 {
		return m.MaxLogKB
	}
	return defaultEntwareCleanMaxLogKB
}
