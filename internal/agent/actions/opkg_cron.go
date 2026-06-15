package actions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const (
	defaultOpkgCronScriptPath = "/opt/etc/wg-monitor/opkg-auto-upgrade.sh"
	defaultOpkgCronLogPath    = "/opt/var/log/wg-monitor/opkg-auto-upgrade.log"
	defaultOpkgCronSchedule   = "30 4 * * *"
	defaultOpkgCronMinFreeKB  = int64(10 * 1024)
	defaultOpkgCronMaxLogKB   = int64(64)

	opkgCronBegin = "# wg-monitor opkg auto-upgrade begin"
	opkgCronEnd   = "# wg-monitor opkg auto-upgrade end"
)

type OpkgCronManager struct {
	Exec       ExecFunc
	Now        func() time.Time
	ScriptPath string
	LogPath    string
	MinFreeKB  int64
	MaxLogKB   int64
}

func (m *OpkgCronManager) Install(ctx context.Context, schedule string) (wire.OpkgCronStatus, error) {
	sched, err := NormalizeOpkgCronSchedule(schedule)
	if err != nil {
		return wire.OpkgCronStatus{}, err
	}
	free, total, err := m.dfOpt(ctx)
	if err != nil {
		return wire.OpkgCronStatus{}, err
	}
	if free < m.minFreeKB() {
		return wire.OpkgCronStatus{}, fmt.Errorf("not enough free space on /opt: free %d KB, need at least %d KB", free, m.minFreeKB())
	}
	if err := os.MkdirAll(filepath.Dir(m.scriptPath()), 0o755); err != nil {
		return wire.OpkgCronStatus{}, fmt.Errorf("create script dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.logPath()), 0o755); err != nil {
		return wire.OpkgCronStatus{}, fmt.Errorf("create log dir: %w", err)
	}
	if err := os.WriteFile(m.scriptPath(), []byte(m.scriptText()), 0o755); err != nil {
		return wire.OpkgCronStatus{}, fmt.Errorf("write script: %w", err)
	}
	current, err := m.readCrontab(ctx)
	if err != nil {
		return wire.OpkgCronStatus{}, err
	}
	next := replaceManagedCronBlock(current, sched, m.scriptPath())
	if err := m.installCrontab(ctx, next); err != nil {
		return wire.OpkgCronStatus{}, err
	}
	_, _ = m.exec(ctx, "/opt/etc/init.d/S10cron", "start")
	tail := m.readLogTail(40)
	lastRun, lastStatus := parseOpkgCronLogTail(tail)
	return wire.OpkgCronStatus{
		Installed:   true,
		Schedule:    sched,
		ScriptPath:  m.scriptPath(),
		CronPath:    "root crontab",
		LogPath:     m.logPath(),
		CronService: "available",
		FreeKB:      free,
		TotalKB:     total,
		MinFreeKB:   m.minFreeKB(),
		LastRun:     lastRun,
		LastStatus:  lastStatus,
		LogTail:     tail,
	}, nil
}

func (m *OpkgCronManager) Status(ctx context.Context, tailLines int) (wire.OpkgCronStatus, error) {
	free, total, _ := m.dfOpt(ctx)
	crontab, cronErr := m.readCrontab(ctx)
	installed, schedule := parseManagedCron(crontab, m.scriptPath())
	if _, err := os.Stat(m.scriptPath()); err != nil {
		installed = false
	}
	tail := m.readLogTail(tailLines)
	lastRun, lastStatus := parseOpkgCronLogTail(tail)
	cronService := "available"
	if cronErr != nil {
		cronService = "unavailable: " + cronErr.Error()
	}
	return wire.OpkgCronStatus{
		Installed:   installed,
		Schedule:    schedule,
		ScriptPath:  m.scriptPath(),
		CronPath:    "root crontab",
		LogPath:     m.logPath(),
		CronService: cronService,
		FreeKB:      free,
		TotalKB:     total,
		MinFreeKB:   m.minFreeKB(),
		LastRun:     lastRun,
		LastStatus:  lastStatus,
		LogTail:     tail,
	}, nil
}

func (m *OpkgCronManager) Logs(ctx context.Context, tailLines int) (wire.OpkgCronStatus, error) {
	return m.Status(ctx, tailLines)
}

func (m *OpkgCronManager) Remove(ctx context.Context) (wire.OpkgCronStatus, error) {
	current, err := m.readCrontab(ctx)
	if err != nil {
		return wire.OpkgCronStatus{}, err
	}
	next := stripManagedCronBlock(current)
	if err := m.installCrontab(ctx, next); err != nil {
		return wire.OpkgCronStatus{}, err
	}
	if err := os.Remove(m.scriptPath()); err != nil && !os.IsNotExist(err) {
		return wire.OpkgCronStatus{}, fmt.Errorf("remove script: %w", err)
	}
	return m.Status(ctx, 40)
}

func NormalizeOpkgCronSchedule(in string) (string, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return defaultOpkgCronSchedule, nil
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

func cronScheduleFieldsLookSafe(fields []string) bool {
	for _, f := range fields {
		if f == "" {
			return false
		}
		for _, r := range f {
			if (r >= '0' && r <= '9') || r == '*' || r == '/' || r == '-' || r == ',' {
				continue
			}
			return false
		}
	}
	return true
}

func (m *OpkgCronManager) readCrontab(ctx context.Context) (string, error) {
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

func (m *OpkgCronManager) installCrontab(ctx context.Context, content string) error {
	tmp, err := os.CreateTemp("", "wg-monitor-opkg-cron-*.tab")
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

func replaceManagedCronBlock(current, schedule, scriptPath string) string {
	base := strings.TrimRight(stripManagedCronBlock(current), "\n")
	block := opkgCronBegin + "\n" + schedule + " " + scriptPath + "\n" + opkgCronEnd
	if base == "" {
		return block + "\n"
	}
	return base + "\n" + block + "\n"
}

func stripManagedCronBlock(current string) string {
	var out []string
	inBlock := false
	for _, line := range strings.Split(current, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == opkgCronBegin {
			inBlock = true
			continue
		}
		if trimmed == opkgCronEnd {
			inBlock = false
			continue
		}
		if !inBlock {
			out = append(out, line)
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}

func parseManagedCron(crontab, scriptPath string) (bool, string) {
	inBlock := false
	for _, line := range strings.Split(crontab, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == opkgCronBegin {
			inBlock = true
			continue
		}
		if trimmed == opkgCronEnd {
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

func (m *OpkgCronManager) readLogTail(lines int) string {
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

func parseOpkgCronLogTail(tail string) (lastRun, lastStatus string) {
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
		lower := strings.ToLower(line)
		switch {
		case strings.Contains(lower, "status=ok") || strings.HasSuffix(lower, " ok") || strings.Contains(lower, " ok"):
			lastStatus = "ok"
		case strings.Contains(lower, "skipped"):
			lastStatus = "skipped"
		case strings.Contains(lower, "failed") || strings.Contains(lower, "error"):
			lastStatus = "err"
		default:
			lastStatus = "unknown"
		}
		return lastRun, lastStatus
	}
	return "", ""
}

func (m *OpkgCronManager) dfOpt(ctx context.Context) (free, total int64, err error) {
	out, err := m.exec(ctx, "df", "-k", "/opt")
	if err != nil {
		return 0, 0, fmt.Errorf("df /opt: %w\n%s", err, string(out))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, 0, fmt.Errorf("unexpected df output: %s", string(out))
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		return 0, 0, fmt.Errorf("unexpected df fields: %q", lines[1])
	}
	total, _ = strconv.ParseInt(fields[1], 10, 64)
	free, _ = strconv.ParseInt(fields[3], 10, 64)
	return free, total, nil
}

func (m *OpkgCronManager) scriptText() string {
	return fmt.Sprintf(`#!/bin/sh
set -u
PATH=/opt/bin:/opt/sbin:/usr/bin:/usr/sbin:/bin:/sbin
LOG=%q
LOCK=/tmp/wg-monitor-opkg-auto-upgrade.lock
MIN_FREE_KB=%d
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

free_kb() {
  df -k /opt | awk 'NR==2 {print $4}'
}

if ! mkdir "$LOCK" 2>/dev/null; then
  log "status=locked another run is active"
  trim_log
  exit 0
fi
trap 'rmdir "$LOCK" 2>/dev/null || true' EXIT INT TERM

free="$(free_kb)"
case "$free" in
  ''|*[!0-9]*) log "status=skipped_bad_df free=$free"; trim_log; exit 0 ;;
esac
if [ "$free" -lt "$MIN_FREE_KB" ]; then
  log "status=skipped_low_space free_kb=$free min_free_kb=$MIN_FREE_KB"
  trim_log
  exit 0
fi

log "status=start free_kb=$free"
if ! opkg update >> "$LOG" 2>&1; then
  log "status=opkg_update_failed"
  trim_log
  exit 1
fi
if ! opkg upgrade >> "$LOG" 2>&1; then
  log "status=opkg_upgrade_failed"
  trim_log
  exit 1
fi
log "status=ok"
trim_log
`, m.logPath(), m.minFreeKB(), m.maxLogKB())
}

func (m *OpkgCronManager) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.Exec != nil {
		return m.Exec(ctx, name, args...)
	}
	return DefaultExec(ctx, name, args...)
}

func (m *OpkgCronManager) scriptPath() string {
	if m.ScriptPath != "" {
		return m.ScriptPath
	}
	return defaultOpkgCronScriptPath
}

func (m *OpkgCronManager) logPath() string {
	if m.LogPath != "" {
		return m.LogPath
	}
	return defaultOpkgCronLogPath
}

func (m *OpkgCronManager) minFreeKB() int64 {
	if m.MinFreeKB > 0 {
		return m.MinFreeKB
	}
	return defaultOpkgCronMinFreeKB
}

func (m *OpkgCronManager) maxLogKB() int64 {
	if m.MaxLogKB > 0 {
		return m.MaxLogKB
	}
	return defaultOpkgCronMaxLogKB
}
