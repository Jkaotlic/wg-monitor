package actions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/routerscripts"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const (
	defaultEntwareCleanScriptPath        = "/opt/etc/wg-monitor/entware-cleanup.sh"
	defaultEntwareCleanLogPath           = "/opt/var/log/wg-monitor/entware-cleanup.log"
	defaultEntwareCleanSchedule          = "15 5 * * *"
	defaultEntwareCleanMinFreeKB         = int64(2 * 1024)
	defaultEntwareCleanMinMemAvailableKB = int64(64 * 1024)
	defaultEntwareCleanMaxLogKB          = int64(64)
	// entwareCleanInstallMinKB -- места под скрипт (~4 КБ) и crontab с запасом.
	entwareCleanInstallMinKB = int64(64)

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
	// AGENT-13: чистка нужнее всего на забитом /opt, поэтому отказ -- только
	// когда не лезет даже сам скрипт с crontab (entwareCleanInstallMinKB).
	// MinFreeKB остаётся порогом «мало места» в статусе, а не запретом.
	if free < entwareCleanInstallMinKB {
		return wire.EntwareCleanStatus{}, fmt.Errorf("not enough free space on /opt: free %d KB, need at least %d KB for the cleanup script", free, entwareCleanInstallMinKB)
	}
	if _, err := ensureCronInstalled(ctx, m.exec); err != nil {
		return wire.EntwareCleanStatus{}, err
	}
	if err := m.writeScript(); err != nil {
		return wire.EntwareCleanStatus{}, err
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
	// v0.57: без включённого расписания скрипта нет -- пишем его сами, без
	// cron. Раньше здесь был отказ, а чистка нужнее всего, когда места нет.
	if _, err := os.Stat(m.scriptPath()); errors.Is(err, fs.ErrNotExist) {
		if err := m.writeScript(); err != nil {
			return wire.EntwareCleanStatus{}, err
		}
	} else if err != nil {
		return wire.EntwareCleanStatus{}, fmt.Errorf("stat cleanup script: %w", err)
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

// writeScript кладёт скрипт очистки на место: временный файл рядом и
// переименование, чтобы cron не застал недописанный скрипт.
func (m *EntwareCleanManager) writeScript() error {
	if err := os.MkdirAll(filepath.Dir(m.scriptPath()), 0o755); err != nil {
		return fmt.Errorf("create script dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.logPath()), 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	text, err := m.scriptText()
	if err != nil {
		return err
	}
	if err := writeFileAtomic(m.scriptPath(), []byte(text), 0o755); err != nil {
		return fmt.Errorf("write script: %w", err)
	}
	return nil
}

// scriptText -- скрипт очистки из routerscripts с подставленными значениями.
// Сам текст живёт файлом (v0.57): тот же скрипт можно поставить руками.
// Ошибка -- поломка сборки (ключ пропал из файла), а не роутера: её ловит
// тест; на роутер в этом случае не уходит ничего.
func (m *EntwareCleanManager) scriptText() (string, error) {
	text, err := routerscripts.Render(routerscripts.EntwareCleanup, [][2]string{
		{"LOG", strconv.Quote(m.logPath())},
		{"MIN_FREE_KB", strconv.FormatInt(m.minFreeKB(), 10)},
		{"MIN_MEM_AVAILABLE_KB", strconv.FormatInt(m.minMemAvailableKB(), 10)},
		{"MAX_LOG_KB", strconv.FormatInt(m.maxLogKB(), 10)},
	})
	if err != nil {
		return "", err
	}
	return text, nil
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
