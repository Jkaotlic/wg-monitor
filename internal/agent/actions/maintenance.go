package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
	"golang.org/x/sync/errgroup"
)

// FirmwareServerSilent -- начало ошибки, когда KeenOS ответил без блока
// local: сервер обновлений Keenetic не ответил. Мини-апп ищет эту строку.
const FirmwareServerSilent = "firmware server did not answer"

// GetFirmwareStatus отвечает, какая прошивка стоит и есть ли новее.
//
// Основной путь -- локальный RCI `components/list`: `ndmc -c "components
// list"` -- «продолжаемая» команда и порой отдаёт пустой ответ за 0 с
// (прод 02.10: «could not extract local.version»). Старый путь ndmc остаётся
// запасным -- только когда RCI на роутере не отвечает вовсе (старый KeenOS).
// rci == nil -- сразу ndmc.
func GetFirmwareStatus(ctx context.Context, exec ExecFunc, rci RCIFunc) (wire.FirmwareStatus, error) {
	if rci != nil {
		fs, err := firmwareStatusRCI(ctx, rci)
		if !errors.Is(err, ErrRCIUnreachable) {
			return fs, err
		}
	}
	return firmwareStatusNdmc(ctx, exec)
}

// firmwareListRetry -- сколько раз спрашивать `components/list`, пока роутер
// отвечает «список ещё загружается» ({"continued": true}), и пауза между
// попытками. 5 раз с паузой 2 с; тесты подменяют sleep.
var firmwareListRetry = firmwareWatchCfg{total: 5, sleep: sleepTwoSeconds}

func sleepTwoSeconds(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return nil
	}
}

// rciComponentsList -- то немногое, что нужно из ответа в ~260 КБ: блок
// component разбором пропускается и никуда не попадает.
type rciComponentsList struct {
	rciAnswer
	Firmware *struct {
		Version string `json:"version"`
	} `json:"firmware"`
	Sandbox string `json:"sandbox"`
	Local   *struct {
		Version string `json:"version"`
	} `json:"local"`
}

func firmwareStatusRCI(ctx context.Context, rci RCIFunc) (wire.FirmwareStatus, error) {
	var fs wire.FirmwareStatus
	for i := 0; i < firmwareListRetry.total; i++ {
		if i > 0 {
			if err := firmwareListRetry.sleep(ctx); err != nil {
				return fs, fmt.Errorf("rci components list: %w", err)
			}
		}
		body, err := rci(ctx, "POST", "/rci/components/list", []byte(`{}`))
		if err != nil {
			return fs, err
		}
		var ans rciComponentsList
		if err := json.Unmarshal(body, &ans); err != nil {
			return fs, fmt.Errorf("rci components list: unexpected answer: %s", answerExcerpt(body))
		}
		if ans.Local != nil && ans.Local.Version != "" {
			fs.Current = ans.Local.Version
			fs.Channel = ans.Sandbox
			if ans.Firmware != nil && ans.Firmware.Version != "" && ans.Firmware.Version != fs.Current {
				fs.Available = ans.Firmware.Version
			}
			return fs, nil
		}
		if msg := ans.errorText(); msg != "" {
			return fs, fmt.Errorf("rci components list: %s", msg)
		}
		if ans.Continued {
			continue // список ещё загружается
		}
		if ans.Firmware != nil {
			// Тот же случай, что `firmware:` без `local:` у ndmc.
			return fs, fmt.Errorf("%s: firmware: %s | sandbox: %s", FirmwareServerSilent,
				keenetic.Excerpt(ans.Firmware.Version, 1), keenetic.Excerpt(ans.Sandbox, 1))
		}
		return fs, fmt.Errorf("could not extract local.version from rci components/list answer")
	}
	return fs, fmt.Errorf("could not extract local.version: rci components/list is still being fetched")
}

// answerExcerpt -- начало неожиданного ответа RCI для текста ошибки: не
// длиннее keenetic.ExcerptMaxRunes, сколько бы ни весил сам ответ.
func answerExcerpt(body []byte) string {
	if len(body) > 4*keenetic.ExcerptMaxRunes {
		body = body[:4*keenetic.ExcerptMaxRunes]
	}
	return keenetic.Excerpt(strings.ToValidUTF8(string(body), ""), 1)
}

// firmwareStatusNdmc -- старый путь: `ndmc -c "components list"` отдаёт два
// YAML-подобных блока, `firmware:` (выпуск на сервере для канала) и `local:`
// (что установлено). Отличаются -- есть обновление.
func firmwareStatusNdmc(ctx context.Context, exec ExecFunc) (wire.FirmwareStatus, error) {
	out, err := exec(ctx, "ndmc", "-c", "components list")
	if err != nil {
		if ex := keenetic.Excerpt(string(out), 3); ex != "" {
			return wire.FirmwareStatus{}, fmt.Errorf("ndmc components list: %w: %s", err, ex)
		}
		return wire.FirmwareStatus{}, fmt.Errorf("ndmc components list: %w", err)
	}
	fs, perr := parseComponentsList(string(out))
	if perr != nil {
		ex := keenetic.Excerpt(string(out), 3)
		if strings.Contains(string(out), "firmware:") && !strings.Contains(string(out), "local:") {
			return fs, fmt.Errorf("%s: %s", FirmwareServerSilent, ex)
		}
		return fs, fmt.Errorf("%w: %s", perr, ex)
	}
	return fs, nil
}

const (
	FirmwareStartedMsg     = "firmware download started; router will reboot when done"
	FirmwareUnconfirmedMsg = "firmware install kicked; not confirmed by router log"
	FirmwareInterrupted    = "firmware update interrupted"
	// FirmwareUpToDate -- ставить нечего: стоит та же версия, что на сервере.
	// После двоеточия -- установленная версия. Мини-апп ищет начало строки.
	FirmwareUpToDate = "firmware is up to date"
	// firmwareViaNdmcNote дописывается к FirmwareUnconfirmedMsg и к
	// FirmwareInterrupted, когда commit ушёл старым путём: начало строки
	// прежнее, а по пометке мини-апп говорит «обновите в веб-панели роутера».
	firmwareViaNdmcNote = "; rci unavailable, started via ndmc"
)

// firmwareWatchCfg -- сколько раз и как часто смотреть журнал после commit.
// По умолчанию 10 раз по 2 с = 20 с; тесты подменяют sleep.
type firmwareWatchCfg struct {
	total int
	sleep func(ctx context.Context) error
}

var firmwareWatch = firmwareWatchCfg{total: 10, sleep: sleepTwoSeconds}

// firmwareWatchRCI -- окно пути RCI: строки о перезагрузке приходят примерно
// через 40 с после «update task started» (роутер, KeenOS 5.02, 02.10), так
// что смотрим 25 раз по 2 с = 50 с. Выход раньше -- по строке о перезагрузке
// или по провалу.
var firmwareWatchRCI = firmwareWatchCfg{total: 25, sleep: sleepTwoSeconds}

// Терминальный провал -- только строка Components:: с этими метками. Строка
// Core::Ndss сама по себе (например, «cannot connect») провалом не считается:
// она лишь объясняет провал Components::.
var firmwareFailMarks = []string{"update interrupted", "request failed"}

// InstallFirmware запускает установку прошивки и сверяется с журналом.
//
// Запуск -- через локальный RCI `components/commit`. `ndmc -c "components
// commit"` -- «продолжаемая» команда: задача живёт, пока жива сессия CLI, а
// `ndmc -c` выходит сразу, и установка обрывается в ту же секунду (прод
// 30.09 и 02.10, KeenOS 5.02: «ok» за 18 мс, в журнале -- Ndss: cannot
// connect / request failed (0) / update interrupted). Через RCI та же
// установка доходит до перезагрузки. ndmc остаётся запасным путём только
// для роутера, где RCI не отвечает вовсе; такой запуск стартом не считается,
// пока журнал не покажет перезагрузку.
//
// Перед запуском через RCI спрашиваем статус: стоит свежая прошивка --
// FirmwareUpToDate и никакого commit.
//
// Дальше судим по новым строкам журнала. Провал возвращается, как только
// замечен; строка о перезагрузке -- сразу «начато» (роутер вот-вот уйдёт,
// ждать нечего); иначе окно отрабатывается целиком: 50 с на пути RCI
// (перезагрузка приходит через ~40 с после старта), 20 с на пути ndmc.
func InstallFirmware(ctx context.Context, exec ExecFunc, rci RCIFunc) (string, error) {
	// Ставить нечего -- commit не шлём: роутер ответил бы «начато» и ничего
	// бы не сделал. Статус не получен (сервер молчит, RCI нет) -- идём как
	// раньше, решит сам роутер.
	if rci != nil {
		if fs, err := firmwareStatusRCI(ctx, rci); err == nil && fs.Available == "" {
			return "", fmt.Errorf("%s: %s", FirmwareUpToDate, keenetic.Excerpt(fs.Current, 1))
		}
	}
	before := map[string]bool{}
	beforeLines, beforeErr := firmwareLogLines(ctx, exec)
	for _, l := range beforeLines {
		before[l] = true
	}
	started, viaNdmc, err := commitFirmware(ctx, exec, rci)
	if err != nil {
		return "", err
	}
	unconfirmed, interrupted, watch := FirmwareUnconfirmedMsg, FirmwareInterrupted, firmwareWatchRCI
	if viaNdmc {
		unconfirmed += firmwareViaNdmcNote
		interrupted += firmwareViaNdmcNote
		watch = firmwareWatch
	}
	if beforeErr != nil {
		// Без снимка «до» старые строки не отличить от новых.
		return unconfirmed, nil
	}
	for i := 0; i < watch.total; i++ {
		if err := watch.sleep(ctx); err != nil {
			break
		}
		lines, err := firmwareLogLines(ctx, exec)
		if err != nil {
			continue // неудачный взгляд = «новых строк нет»
		}
		var fresh []string
		for _, l := range lines {
			if !before[l] {
				fresh = append(fresh, l)
			}
		}
		failed, rebooting := false, false
		for _, l := range fresh {
			switch {
			case isComponentsFailure(l):
				failed = true
			case isFirmwareGoingLine(l):
				rebooting = true
			case !viaNdmc && strings.Contains(l, "update task started"):
				started = true
			}
		}
		if failed {
			var msgs []string
			for _, l := range fresh {
				if isComponentsFailure(l) || strings.Contains(l, "Core::Ndss") {
					msgs = append(msgs, logMessage(l))
				}
			}
			return "", fmt.Errorf("%s: %s", interrupted, strings.Join(msgs, " | "))
		}
		if rebooting {
			return FirmwareStartedMsg, nil
		}
	}
	if started {
		return FirmwareStartedMsg, nil
	}
	return unconfirmed, nil
}

// commitFirmware даёт роутеру команду ставить прошивку. started -- роутер
// сам подтвердил старт в ответе RCI; viaNdmc -- ушли на старый путь.
//
// Ошибка -- только когда установка точно не началась: роутер отказал
// (запись об ошибке в ответе, HTTP 4xx) или не сработал старый путь. Любой
// сбой ПОСЛЕ отправки запроса в RCI (срок, 5xx, оборванное чтение, пустой
// или не-JSON ответ) ошибкой не считается и вторым запуском через ndmc не
// лечится: задача могла стартовать, и роутер через ~40 с уйдёт в
// перезагрузку -- это покажет журнал.
func commitFirmware(ctx context.Context, exec ExecFunc, rci RCIFunc) (started, viaNdmc bool, err error) {
	if rci != nil {
		body, rerr := rci(ctx, "POST", "/rci/components/commit", []byte(`{}`))
		switch {
		case rerr == nil:
			var ans rciAnswer
			if json.Unmarshal(body, &ans) != nil {
				return false, false, nil
			}
			if msg := ans.errorText(); msg != "" {
				return false, false, fmt.Errorf("rci components commit: %s", msg)
			}
			return ans.Continued || ans.hasMessage("update task started"), false, nil
		case errors.Is(rerr, ErrRCIRejected):
			return false, false, rerr
		case !errors.Is(rerr, ErrRCIUnreachable):
			return false, false, nil
		}
	}
	if out, err := exec(ctx, "ndmc", "-c", "components commit"); err != nil {
		if ex := keenetic.ErrExcerpt(string(out)); ex != "" {
			return false, true, fmt.Errorf("ndmc components commit: %w: %s", err, ex)
		}
		return false, true, fmt.Errorf("ndmc components commit: %w", err)
	}
	return false, true, nil
}

// firmwareGoingMarks -- строки журнала перед перезагрузкой на новую прошивку
// (сняты с роутера, KeenOS 5.02, 02.10). После любой из них установка уже
// не отменится. Только эти три: «RebootManager» и «firmware update» сами по
// себе встречаются и в строках, ничего не обещающих.
var firmwareGoingMarks = []string{
	"RebootManager: activated reboot",
	"RebootManager: started a reboot process",
	"Update::RunningConfig: firmware update",
}

func isFirmwareGoingLine(l string) bool {
	for _, m := range firmwareGoingMarks {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

func isComponentsFailure(l string) bool {
	if !strings.Contains(l, "Components::") {
		return false
	}
	for _, m := range firmwareFailMarks {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

// firmwareLogLines -- строки журнала про компоненты, Ndss и перезагрузку, в порядке журнала
// (от него зависит понятный текст ошибки).
func firmwareLogLines(ctx context.Context, exec ExecFunc) ([]string, error) {
	out, err := exec(ctx, "ndmc", "-c", "show log 40")
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, raw := range strings.Split(string(out), "\n") {
		l := strings.TrimSpace(strings.ReplaceAll(raw, "\x1b[K", ""))
		if strings.Contains(l, "Components::") || strings.Contains(l, "Core::Ndss") || isFirmwareGoingLine(l) {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// logMessage -- «E [Sep 30 11:31:54] ndm: Core::Ndss: …» → «Core::Ndss: …».
func logMessage(l string) string {
	if i := strings.Index(l, "] "); i >= 0 {
		l = l[i+2:]
	}
	return strings.TrimPrefix(l, "ndm: ")
}

// parseComponentsList is the format parser, separated for table-driven tests.
//
// Format observed on testkeen (M0 Probe 4): ndmc emits an `\x1b[K` ANSI
// erase-line escape at the start of output, then YAML-ish indented blocks.
// Leading whitespace per line is variable (10-16 spaces); we use suffix
// matching of trimmed prefixes rather than fixed-column parsing.
//
// The full output is ~2700 lines and contains many per-component blocks AFTER
// `local:` (each with its own `version:` line). To avoid those overwriting
// `localVersion`, we (a) reset the block tracker on ANY new top-level block
// header (`component:`, `sandbox:`, etc.), and (b) only capture the FIRST
// `version:` per block.
func parseComponentsList(s string) (wire.FirmwareStatus, error) {
	var fs wire.FirmwareStatus
	var firmwareVersion, localVersion string
	block := "" // "firmware" | "local" | "" (top-level / unknown)
	for _, raw := range strings.Split(s, "\n") {
		// Strip ANSI erase-line escape if present at the very start of a line.
		line := strings.TrimPrefix(raw, "\x1b[K")
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		// Detect block headers — lines like "firmware:" / "local:" / "component:"
		// where the value after `:` is empty.
		if k, v, ok := splitKV(trimmed); ok && v == "" {
			switch k {
			case "firmware":
				block = "firmware"
			case "local":
				block = "local"
			default:
				// Any other header (component, etc.) takes us out of firmware/local.
				block = ""
			}
			continue
		}
		// Within current block, harvest version: / sandbox: keys.
		if k, v, ok := splitKV(trimmed); ok {
			switch {
			case k == "version" && block == "firmware" && firmwareVersion == "":
				firmwareVersion = v
			case k == "version" && block == "local" && localVersion == "":
				localVersion = v
			case k == "sandbox" && block == "local":
				// Ignore local.sandbox — we only want the top-level channel.
			case k == "sandbox" && fs.Channel == "":
				// Top-level sandbox: line (appears between firmware: and local:
				// blocks while block is still "firmware"). Capture once and
				// reset block context so subsequent unrelated keys don't pollute.
				fs.Channel = v
				block = ""
			}
		}
	}
	if localVersion == "" {
		return fs, fmt.Errorf("could not extract local.version from `ndmc components list` output")
	}
	fs.Current = localVersion
	if firmwareVersion != "" && firmwareVersion != localVersion {
		fs.Available = firmwareVersion
	}
	return fs, nil
}

// splitKV splits a "key: value" line, returning ok=false if not formatted that way.
func splitKV(line string) (key, value string, ok bool) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

// AwgInfoClient is the subset of *awgmgr.Client that VersionAudit consumes.
// Defined as an interface so tests can stub the awg-manager API.
type AwgInfoClient interface {
	SystemInfo(ctx context.Context) (*awgmgr.SystemInfo, error)
	HydraRouteStatus(ctx context.Context) (*awgmgr.HydraRouteStatus, error)
}

// VersionAudit collects installed versions and daemon uptimes for the
// Maintenance panel. Sources:
//   - awgmgr version + firmware: awgmgr SystemInfo.
//   - hrneo version: `opkg info hrneo` (no API; HRStatus has no version field).
//   - hrneo uptime, awgmgr uptime: /proc/$pid/stat starttime (jiffies, USER_HZ=100)
//     subtracted from /proc/uptime system uptime.
//   - firmware available: RCI `components/list` (ndmc as fallback) — populated only when the
//     server-side release differs from the locally installed version.
//
// Best-effort everywhere: if hrneo isn't installed, hrneo fields stay empty;
// if a daemon isn't running, its uptime stays empty; only the awgmgr SystemInfo
// fetch is treated as fatal (without it we cannot render anything useful).
//
// Two-phase parallelisation: phase 1 fans out the four independent data
// fetches (SystemInfo, HRStatus, components list, /proc/uptime, opkg info)
// concurrently — they share no dependencies. Phase 2 fans out the two daemon
// uptimes (which depend on phase 1's sysUp). On a healthy router the eight
// sequential exec calls (~600ms wall) collapse to ~150ms.
func VersionAudit(ctx context.Context, awg AwgInfoClient, exec ExecFunc, rci RCIFunc) (wire.VersionAudit, error) {
	var (
		sys      *awgmgr.SystemInfo
		sysErr   error
		hr       *awgmgr.HydraRouteStatus
		hrErr    error
		hrneoVer string
		fs       wire.FirmwareStatus
		fsErr    error
		sysUp    float64
	)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		sys, sysErr = awg.SystemInfo(gctx)
		return nil // best-effort fan-out; surface error after Wait
	})
	g.Go(func() error {
		hr, hrErr = awg.HydraRouteStatus(gctx)
		return nil
	})
	g.Go(func() error {
		// Always probe — opkg returns an error if hrneo is uninstalled,
		// which is gated by HRStatus.Installed below.
		if v, err := opkgHrneoVersion(gctx, exec); err == nil {
			hrneoVer = v
		}
		return nil
	})
	g.Go(func() error {
		fs, fsErr = GetFirmwareStatus(gctx, exec, rci)
		return nil
	})
	g.Go(func() error {
		sysUp = readSystemUptime(gctx, exec)
		return nil
	})
	_ = g.Wait()

	if sysErr != nil {
		return wire.VersionAudit{}, fmt.Errorf("awgmgr SystemInfo: %w", sysErr)
	}
	out := wire.VersionAudit{
		AwgmgrVersion:     sys.Version,
		AwgmgrBackend:     sys.ActiveBackend,
		AwgmgrRunning:     true,
		FirmwareCurrent:   sys.FirmwareVersion,
		KmodVersion:       sys.KernelModuleVersion,
		KmodModel:         sys.KernelModuleModel,
		KmodLoadedVersion: sys.KernelModuleLoadedVersion,
	}
	// SystemInfo получен -- значит про модуль ядра агент СКАЗАЛ, и «не
	// загружен» здесь ответ, а не молчание. Указатель ставим всегда, иначе
	// бэкенд не отличит выключенный модуль от старого агента.
	kmodLoaded := sys.KernelModuleLoaded
	out.KmodLoaded = &kmodLoaded
	if hrErr == nil && hr != nil {
		// Опрос УДАЛСЯ -- значит и «не установлен» здесь ответ, и он уезжает
		// явным false. Не удался -- поле остаётся nil, и снимок в базе
		// сохранит то, что знал раньше, вместо того чтобы затереть его.
		hrneoInstalled := hr.Installed
		out.HrneoInstalled = &hrneoInstalled
		out.HrneoRunning = hr.Running
		if hr.Installed && hrneoVer != "" {
			out.HrneoVersion = hrneoVer
		}
	}
	if fsErr == nil {
		if fs.Current != "" {
			out.FirmwareCurrent = fs.Current
		}
		out.FirmwareAvail = fs.Available
	}

	var hrneoUp, awgmgrUp string
	g2, gctx2 := errgroup.WithContext(ctx)
	if out.HrneoRunning && out.HrneoVersion != "" {
		g2.Go(func() error {
			hrneoUp = daemonUptime(gctx2, exec, sysUp, "hrneo")
			return nil
		})
	}
	g2.Go(func() error {
		awgmgrUp = daemonUptime(gctx2, exec, sysUp, "awg-manager")
		return nil
	})
	_ = g2.Wait()
	out.HrneoUptime = hrneoUp
	out.AwgmgrUptime = awgmgrUp
	return out, nil
}

// EncodeVersionAudit serialises for transport via wire.CommandResult.Output.
func EncodeVersionAudit(va wire.VersionAudit) (string, error) {
	b, err := json.Marshal(va)
	return string(b), err
}

// opkgHrneoVersion reads `opkg info hrneo` and extracts the version, stripping
// the trailing `-N` packager-revision suffix (`2.4.0-1` → `2.4.0`).
func opkgHrneoVersion(ctx context.Context, exec ExecFunc) (string, error) {
	out, err := exec(ctx, "opkg", "info", "hrneo")
	if err != nil {
		return "", fmt.Errorf("opkg info hrneo: %w", err)
	}
	return parseHrneoOpkg(string(out))
}

func parseHrneoOpkg(s string) (string, error) {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "Version:") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
		// Strip packager revision suffix `-N` if present.
		if idx := strings.LastIndex(v, "-"); idx > 0 {
			suffix := v[idx+1:]
			isNumeric := suffix != ""
			for _, r := range suffix {
				if r < '0' || r > '9' {
					isNumeric = false
					break
				}
			}
			if isNumeric {
				v = v[:idx]
			}
		}
		if v == "" {
			return "", fmt.Errorf("empty Version: line in opkg output")
		}
		return v, nil
	}
	return "", fmt.Errorf("no Version: line in opkg output")
}

// readSystemUptime reads /proc/uptime and returns the first field (system
// uptime in seconds). Returns 0 on error — callers should treat 0 as
// "uptime unknown" and skip the daemon-uptime computation.
func readSystemUptime(ctx context.Context, exec ExecFunc) float64 {
	out, err := exec(ctx, "cat", "/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0
	}
	var v float64
	if _, err := fmt.Sscanf(fields[0], "%f", &v); err != nil {
		return 0
	}
	return v
}

// userHZ is the kernel jiffies-per-second constant. Hard-coded to 100, which
// is universal on aarch64 Linux. busybox `getconf` is unavailable on
// Keenetic + Entware, so we cannot read it at runtime.
const userHZ = 100

// daemonUptime returns a humanised uptime for the named daemon, or empty
// string if the daemon is not running / parsing fails. Never returns an
// error — uptime is a "nice to have" cell in the panel.
func daemonUptime(ctx context.Context, exec ExecFunc, sysUp float64, name string) string {
	if sysUp <= 0 {
		return ""
	}
	pidB, err := exec(ctx, "pidof", name)
	if err != nil {
		return ""
	}
	pid := strings.TrimSpace(strings.SplitN(string(pidB), " ", 2)[0])
	if pid == "" {
		return ""
	}
	statB, err := exec(ctx, "cat", "/proc/"+pid+"/stat")
	if err != nil {
		return ""
	}
	starttime, ok := parseProcStatStarttime(string(statB))
	if !ok {
		return ""
	}
	daemonUpSec := int64(sysUp) - (starttime / userHZ)
	if daemonUpSec < 0 {
		return ""
	}
	return humanizeUptime(daemonUpSec)
}

// parseProcStatStarttime extracts field 22 (starttime in jiffies since boot)
// from /proc/$pid/stat. The process name is wrapped in parens and may itself
// contain spaces, so we split on the LAST `)` and tokenise the trailing fields.
func parseProcStatStarttime(s string) (int64, bool) {
	rp := strings.LastIndex(s, ")")
	if rp < 0 || rp+1 >= len(s) {
		return 0, false
	}
	rest := strings.Fields(s[rp+1:])
	// After `)` the fields are: state(1) ppid(2) pgrp(3) session(4) tty(5)
	// tpgid(6) flags(7) minflt(8) cminflt(9) majflt(10) cmajflt(11) utime(12)
	// stime(13) cutime(14) cstime(15) priority(16) nice(17) num_threads(18)
	// itrealvalue(19) starttime(20) ...
	// Index into rest[] is 0-based, so starttime is at index 19.
	if len(rest) < 20 {
		return 0, false
	}
	var v int64
	if _, err := fmt.Sscanf(rest[19], "%d", &v); err != nil {
		return 0, false
	}
	return v, true
}

// humanizeUptime turns a duration in seconds into a Russian short form:
// "3д 4ч" / "5ч 30м" / "1м 23с" / "42с" / "0с".
func humanizeUptime(sec int64) string {
	if sec < 60 {
		return fmt.Sprintf("%dс", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%dм %dс", sec/60, sec%60)
	}
	if sec < 86400 {
		return fmt.Sprintf("%dч %dм", sec/3600, (sec%3600)/60)
	}
	return fmt.Sprintf("%dд %dч", sec/86400, (sec%86400)/3600)
}
