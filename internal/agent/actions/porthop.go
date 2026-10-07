package actions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/routerscripts"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Смена исходящего порта VPN-туннеля, чей поток убила блокировка (v0.57).
// Сам сторож -- шелл-скрипт routerscripts/awg-porthop.sh под своим init;
// агент его ставит, снимает и читает журнал. Работает скрипт без агента:
// откат агента или его остановка смену порта не выключают.
const (
	defaultPorthopScriptPath     = "/opt/etc/wg-monitor/awg-porthop.sh"
	defaultPorthopConfPath       = "/opt/etc/wg-monitor/porthop.conf"
	defaultPorthopInitPath       = "/opt/etc/init.d/S99wg-monitor-porthop"
	defaultPorthopLogPath        = "/opt/var/log/wg-monitor/porthop.log"
	defaultPorthopPidPath        = "/tmp/wg-monitor-porthop.pid" // tmpfs: /opt -- флешка
	defaultPorthopLegacyInitPath = "/opt/etc/init.d/S99awg-porthop"
	defaultPorthopLegacyMoveDir  = "/opt/etc/wg-monitor/legacy"

	// porthopScriptName -- имя скрипта и у нас, и у ручной копии оператора
	// (/opt/bin/awg-porthop.sh). Ручная копия -- процесс с этим именем НЕ
	// из нашего пути.
	porthopScriptName = "awg-porthop.sh"
	porthopMaxIfaces  = 8
	porthopAuto       = "auto"
)

// PorthopLegacyRunningCode -- начало текста ошибки porthop_install, когда на
// роутере есть ручная копия, а replace_legacy не задан. Контракт с
// бэкендом и мини-аппом: экран по нему предлагает «Заменить ручную копию».
const PorthopLegacyRunningCode = "legacy_running"

var porthopIfaceRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,15}$`)

// PorthopManager ставит и снимает сторожа смены порта. Пути и Exec
// подменяемы для тестов, как у EntwareCleanManager.
type PorthopManager struct {
	Exec           ExecFunc
	Now            func() time.Time
	Loc            *time.Location // пояс строк журнала без смещения (до v0.57); nil -- time.Local
	Sleep          func(ctx context.Context, d time.Duration) error
	ScriptPath     string
	ConfPath       string
	InitPath       string
	LogPath        string
	PidPath        string
	LegacyInitPath string
	LegacyMoveDir  string
	ProcDir        string // nil-значение -- /proc
}

// Status -- что стоит и что делается сейчас. tailLines > 0 -- приложить
// хвост журнала (porthop_logs), 0 -- без него.
func (m *PorthopManager) Status(ctx context.Context, tailLines int) (wire.PorthopStatus, error) {
	_, scriptErr := os.Stat(m.scriptPath())
	_, initErr := os.Stat(m.initPath())
	installed := scriptErr == nil && initErr == nil
	auto, ifaces := m.readConf()
	if !installed {
		// Не стоит -- показываем, что сторожил бы auto.
		auto, ifaces = true, nil
	}
	st := wire.PorthopStatus{
		Installed:  installed,
		Running:    m.running(),
		Auto:       auto,
		Ifaces:     ifaces,
		Legacy:     m.legacy(),
		ScriptPath: m.scriptPath(),
		ConfPath:   m.confPath(),
		LogPath:    m.logPath(),
	}
	if auto {
		st.Watched = m.autoIfaces(ctx)
	} else {
		st.Watched = append([]string(nil), ifaces...)
	}
	if st.Watched == nil {
		st.Watched = []string{}
	}
	log, _ := os.ReadFile(m.logPath())
	st.Recovered24h, st.Failed24h, st.LastEvent = porthopLogStats(string(log), m.now(), m.loc())
	st.Hops24h = st.Recovered24h + st.Failed24h
	if tailLines > 0 {
		st.LogTail = porthopTail(string(log), tailLines)
	}
	return st, nil
}

// Logs -- статус с хвостом журнала.
func (m *PorthopManager) Logs(ctx context.Context, tailLines int) (wire.PorthopStatus, error) {
	if tailLines <= 0 {
		tailLines = defaultLogTailLines
	}
	return m.Status(ctx, tailLines)
}

// Install ставит скрипт, настройку и init и запускает сторожа. ifaces пуст
// или ["auto"] -- auto. Идемпотентна: те же файлы и работающий процесс --
// ничего не перезапускается.
//
// Ручная копия оператора (её init в init.d или её процесс) без
// replaceLegacy -- отказ PorthopLegacyRunningCode, ничего не меняется: две
// копии дрались бы за один интерфейс. С replaceLegacy -- сначала записать
// свои файлы (не записались -- ручная копия не тронута), затем остановить
// её её же init, перенести init в LegacyMoveDir (имя на S в init.d
// запустилось бы при загрузке снова), добить оставшиеся процессы и только
// потом запустить свою.
// Сам /opt/bin/awg-porthop.sh не трогаем.
func (m *PorthopManager) Install(ctx context.Context, ifaces []string, replaceLegacy bool) (wire.PorthopStatus, error) {
	conf, err := porthopConfText(ifaces)
	if err != nil {
		return wire.PorthopStatus{}, err
	}
	legacy := m.legacy()
	if legacy.Found && !replaceLegacy {
		return wire.PorthopStatus{}, fmt.Errorf("%s: на роутере есть ручная копия смены порта (%s) — две копии дрались бы за один VPN-туннель. Ничего не изменено; чтобы заменить её, повторите установку с заменой ручной копии", PorthopLegacyRunningCode, legacy.Path)
	}
	changed := false
	for _, f := range []struct {
		path string
		body string
		perm os.FileMode
	}{
		{m.scriptPath(), routerscripts.Porthop, 0o755},
		{m.confPath(), conf, 0o644},
		{m.initPath(), routerscripts.PorthopInit, 0o755},
	} {
		c, err := ensureFile(f.path, f.body, f.perm)
		if err != nil {
			return wire.PorthopStatus{}, err
		}
		changed = changed || c
	}
	if err := os.MkdirAll(filepath.Dir(m.logPath()), 0o755); err != nil {
		return wire.PorthopStatus{}, fmt.Errorf("create log dir: %w", err)
	}
	// Ручная копия -- только когда свои файлы уже на месте: не записалось --
	// ручная продолжает работать, роутер не остаётся без сторожа.
	if legacy.Found {
		if err := m.replaceLegacy(ctx); err != nil {
			return wire.PorthopStatus{}, err
		}
	}
	if changed || !m.running() {
		out, err := m.exec(ctx, m.initPath(), "restart")
		if err != nil {
			return wire.PorthopStatus{}, fmt.Errorf("start porthop: %w\n%s", err, strings.TrimSpace(string(out)))
		}
		if !m.waitRunning(ctx) {
			return wire.PorthopStatus{}, fmt.Errorf("porthop did not start: %s", strings.TrimSpace(string(out)))
		}
	}
	return m.Status(ctx, 0)
}

// Remove останавливает сторожа и убирает скрипт, настройку и init. Журнал
// остаётся (история смен), ручная копия -- как была.
func (m *PorthopManager) Remove(ctx context.Context) (wire.PorthopStatus, error) {
	if _, err := os.Stat(m.initPath()); err == nil {
		if out, err := m.exec(ctx, m.initPath(), "stop"); err != nil {
			return wire.PorthopStatus{}, fmt.Errorf("stop porthop: %w\n%s", err, strings.TrimSpace(string(out)))
		}
	} else if pid, ok := m.ourPid(); ok {
		// init уже нет, а процесс жив -- гасим по pid.
		_, _ = m.exec(ctx, "kill", strconv.Itoa(pid))
	}
	for _, p := range []string{m.initPath(), m.scriptPath(), m.confPath(), m.pidPath()} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return wire.PorthopStatus{}, fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return m.Status(ctx, 0)
}

func (m *PorthopManager) replaceLegacy(ctx context.Context) error {
	if _, err := os.Stat(m.legacyInitPath()); err == nil {
		// Её же init: он знает, как её остановить. Ошибку не считаем
		// окончательной -- оставшиеся процессы добиваются ниже.
		_, _ = m.exec(ctx, m.legacyInitPath(), "stop")
		if err := os.MkdirAll(m.legacyMoveDir(), 0o755); err != nil {
			return fmt.Errorf("create legacy dir: %w", err)
		}
		if err := os.Rename(m.legacyInitPath(), m.legacyMovedPath()); err != nil {
			return fmt.Errorf("move legacy init: %w", err)
		}
	}
	for _, p := range m.legacyProcs() {
		_, _ = m.exec(ctx, "kill", strconv.Itoa(p.pid))
	}
	for i := 0; i < 10 && len(m.legacyProcs()) > 0; i++ {
		if err := m.sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	if left := m.legacyProcs(); len(left) > 0 {
		return fmt.Errorf("ручная копия смены порта не остановилась (pid %d, %s); своя не ставилась", left[0].pid, left[0].path)
	}
	return nil
}

// porthopConfText -- содержимое porthop.conf. Имена проверяются здесь же,
// хотя бэкенд проверяет их до отправки: строка уходит в файл, который читает
// шелл, и в имена файлов состояния.
func porthopConfText(ifaces []string) (string, error) {
	if len(ifaces) == 0 || (len(ifaces) == 1 && ifaces[0] == porthopAuto) {
		return "IFACES=" + porthopAuto + "\n", nil
	}
	if len(ifaces) > porthopMaxIfaces {
		return "", fmt.Errorf("porthop: too many interfaces (%d), at most %d", len(ifaces), porthopMaxIfaces)
	}
	for _, i := range ifaces {
		if !porthopIfaceRe.MatchString(i) {
			return "", fmt.Errorf("porthop: bad interface name %q", i)
		}
	}
	return "IFACES=\"" + strings.Join(ifaces, " ") + "\"\n", nil
}

// readConf -- настройка: auto (нет файла, пусто или auto) или список.
func (m *PorthopManager) readConf() (auto bool, ifaces []string) {
	b, err := os.ReadFile(m.confPath())
	if err != nil {
		return true, nil
	}
	val := ""
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "IFACES="); ok {
			val = strings.NewReplacer(`"`, "", `'`, "").Replace(v)
		}
	}
	f := strings.Fields(val)
	if len(f) == 0 || (len(f) == 1 && f[0] == porthopAuto) {
		return true, nil
	}
	return false, f
}

// autoIfaces -- то же правило, что watched() в скрипте: интерфейсы
// `awg show interfaces` с 0.0.0.0/0 в allowed-ips.
func (m *PorthopManager) autoIfaces(ctx context.Context) []string {
	out, err := m.exec(ctx, "awg", "show", "interfaces")
	if err != nil {
		return nil
	}
	var res []string
	for _, i := range strings.Fields(string(out)) {
		ips, err := m.exec(ctx, "awg", "show", i, "allowed-ips")
		if err == nil && porthopFullRoute(string(ips)) {
			res = append(res, i)
		}
	}
	return res
}

// porthopFullRoute -- в выводе `awg show <i> allowed-ips` (строка на пира:
// ключ, затем сети) есть ровно 0.0.0.0/0. Копия awk-правила скрипта;
// совпадение сторожит TestPorthopWatchRuleMatchesScript.
func porthopFullRoute(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		for k := 1; k < len(f); k++ {
			if f[k] == "0.0.0.0/0" {
				return true
			}
		}
	}
	return false
}

// porthopLogLineRe -- «дата время [смещение] iface: текст». Смещение пишет
// скрипт с v0.57 (date '+%F %T %z'); строки без него -- из журнала прежней
// версии, их время считается местным (Loc).
var porthopLogLineRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})(?: ([+-]\d{4}))? \S+: (.*)$`)

// porthopLogStats -- счёт смен порта за сутки по журналу скрипта и его
// последняя строка. Строки скрипта: «порт A -> B, поток ожил», «порт A -> B,
// не ожил», «ОШИБКА смены порта». Предпросмотр (--dry-run) не в счёт.
func porthopLogStats(log string, now time.Time, loc *time.Location) (recovered, failed int, last string) {
	since := now.Add(-24 * time.Hour)
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			last = line
		}
		mm := porthopLogLineRe.FindStringSubmatch(line)
		if mm == nil {
			continue
		}
		var ts time.Time
		var err error
		if mm[2] != "" {
			ts, err = time.Parse("2006-01-02 15:04:05 -0700", mm[1]+" "+mm[2])
		} else {
			ts, err = time.ParseInLocation("2006-01-02 15:04:05", mm[1], loc)
		}
		if err != nil || !ts.After(since) {
			continue
		}
		msg := mm[3]
		switch {
		case strings.HasPrefix(msg, "порт ") && strings.Contains(msg, "поток ожил"):
			recovered++
		case strings.HasPrefix(msg, "порт ") && strings.Contains(msg, "не ожил"):
			failed++
		case strings.HasPrefix(msg, "ОШИБКА смены порта"):
			failed++
		}
	}
	return recovered, failed, last
}

func porthopTail(text string, n int) string {
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return strings.Join(all, "\n")
}

type porthopProc struct {
	pid  int
	path string // путь скрипта из командной строки
}

// procs -- процессы-шеллы, исполняющие awg-porthop.sh (porthopScriptOf).
func (m *PorthopManager) procs() []porthopProc {
	entries, err := os.ReadDir(m.procDir())
	if err != nil {
		return nil
	}
	var res []porthopProc
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		b, err := os.ReadFile(filepath.Join(m.procDir(), e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		if path, ok := porthopScriptOf(bytes.Split(bytes.TrimRight(b, "\x00"), []byte{0})); ok {
			res = append(res, porthopProc{pid: pid, path: path})
		}
	}
	return res
}

// porthopScriptOf -- путь awg-porthop.sh, если процесс -- шелл, который его
// исполняет: argv[0] -- sh/ash (или busybox с sh/ash следом), argv[1] --
// скрипт. Редактор, tail или grep с тем же именем в аргументах -- не он.
// Так запускают и init (`sh <скрипт>`), и шебанг (`/bin/sh <скрипт> ...`).
func porthopScriptOf(argv [][]byte) (string, bool) {
	if len(argv) < 2 {
		return "", false
	}
	i := 0
	switch filepath.Base(string(argv[0])) {
	case "sh", "ash":
	case "busybox":
		if b := filepath.Base(string(argv[1])); b != "sh" && b != "ash" {
			return "", false
		}
		i = 1
	default:
		return "", false
	}
	if i+1 >= len(argv) {
		return "", false
	}
	path := string(argv[i+1])
	if filepath.Base(path) != porthopScriptName {
		return "", false
	}
	return path, true
}

func (m *PorthopManager) legacyProcs() []porthopProc {
	var res []porthopProc
	for _, p := range m.procs() {
		if p.path != m.scriptPath() {
			res = append(res, p)
		}
	}
	return res
}

func (m *PorthopManager) legacy() wire.PorthopLegacy {
	var l wire.PorthopLegacy
	if _, err := os.Stat(m.legacyInitPath()); err == nil {
		l.Found, l.Path = true, m.legacyInitPath()
	}
	if procs := m.legacyProcs(); len(procs) > 0 {
		l.Found, l.Running = true, true
		if l.Path == "" {
			l.Path = procs[0].path
		}
	}
	if _, err := os.Stat(m.legacyMovedPath()); err == nil {
		l.MovedTo = m.legacyMovedPath()
	}
	return l
}

// ourPid -- pid из pid-файла, если это живой процесс нашего скрипта (pid
// мог достаться другому процессу после перезагрузки).
func (m *PorthopManager) ourPid() (int, bool) {
	b, err := os.ReadFile(m.pidPath())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	for _, p := range m.procs() {
		if p.pid == pid && p.path == m.scriptPath() {
			return pid, true
		}
	}
	return 0, false
}

func (m *PorthopManager) running() bool {
	_, ok := m.ourPid()
	return ok
}

func (m *PorthopManager) waitRunning(ctx context.Context) bool {
	for i := 0; i < 6; i++ {
		if m.running() {
			return true
		}
		if err := m.sleep(ctx, 500*time.Millisecond); err != nil {
			return false
		}
	}
	return m.running()
}

// ensureFile кладёт файл атомарно, если содержимое или права другие.
// changed -- файл переписан.
func ensureFile(path, body string, perm os.FileMode) (changed bool, err error) {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == body {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm() == perm {
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create dir for %s: %w", path, err)
	}
	if err := writeFileAtomic(path, []byte(body), perm); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

func (m *PorthopManager) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if m.Exec != nil {
		return m.Exec(ctx, name, args...)
	}
	return DefaultExec(ctx, name, args...)
}

func (m *PorthopManager) sleep(ctx context.Context, d time.Duration) error {
	if m.Sleep != nil {
		return m.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (m *PorthopManager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *PorthopManager) loc() *time.Location {
	if m.Loc != nil {
		return m.Loc
	}
	return time.Local
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func (m *PorthopManager) scriptPath() string {
	return orDefault(m.ScriptPath, defaultPorthopScriptPath)
}
func (m *PorthopManager) confPath() string { return orDefault(m.ConfPath, defaultPorthopConfPath) }
func (m *PorthopManager) initPath() string { return orDefault(m.InitPath, defaultPorthopInitPath) }
func (m *PorthopManager) logPath() string  { return orDefault(m.LogPath, defaultPorthopLogPath) }
func (m *PorthopManager) pidPath() string  { return orDefault(m.PidPath, defaultPorthopPidPath) }
func (m *PorthopManager) procDir() string  { return orDefault(m.ProcDir, "/proc") }
func (m *PorthopManager) legacyInitPath() string {
	return orDefault(m.LegacyInitPath, defaultPorthopLegacyInitPath)
}
func (m *PorthopManager) legacyMoveDir() string {
	return orDefault(m.LegacyMoveDir, defaultPorthopLegacyMoveDir)
}
func (m *PorthopManager) legacyMovedPath() string {
	return filepath.Join(m.legacyMoveDir(), filepath.Base(m.legacyInitPath()))
}
