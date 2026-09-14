package upstream

import (
	"context"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// UpdateInfo is the canonical "X needs an update" record used by both the
// smart-reply Updates section and the Maintenance panel rendering. Both UI
// surfaces project this into their own struct (LOGIC-09).
type UpdateInfo struct {
	// Component -- устойчивый ключ ("awgmgr" | "hrneo" | "firmware"), тот же,
	// которым говорят Unknown и состояние новости в базе. Name -- подпись для
	// человека, и опираться на неё в коде нельзя: переименование подписи молча
	// разъехалось бы с ключом новости, и «отложить» перестало бы попадать в ту
	// строку, которую человек видел.
	Component string
	Name      string
	Installed string
	Available string
	Hint      string
}

// Причины, по которым про компонент нельзя сказать ничего. Список закрытый:
// неизвестной причины наружу быть не может, иначе экран снова начнёт молчать
// вместо ответа.
//
//   - ReasonNotConfigured -- источник обновлений выключен конфигом (пустой
//     репозиторий в cmd/backend/main.go:172-181 молча не заводит источник);
//   - ReasonUnavailable   -- источник настроен, но не ответил (403, сеть);
//   - ReasonNoSnapshot    -- роутер ещё не рассказал, что у него стоит;
//   - ReasonAgentTooOld   -- агент старый и про модуль ядра не сообщает.
const (
	ReasonNotConfigured = "upstream_not_configured"
	ReasonUnavailable   = "upstream_unavailable"
	ReasonNoSnapshot    = "no_snapshot"
	ReasonAgentTooOld   = "agent_too_old"
)

// Unknown -- «про этот компонент мы не знаем, и вот почему».
//
// Component: "awgmgr" | "hrneo" | "firmware" | "kmod".
type Unknown struct {
	Component string
	Reason    string
}

// ComputeUpdates возвращает и «что вышло», и «почему неизвестно».
//
// Два возвращаемых значения, а не одно, потому что «обновлений нет» и «мы не
// знаем» -- разные ответы, и молчать вторым нельзя. Пустой репозиторий, 403 от
// GitHub и настоящая свежесть выглядели на экране одинаково: блока не было ни
// в одном из трёх случаев. Теперь у каждой неизвестности есть имя, а у имени --
// своя строка на экране.
//
// nil-safe для cache: cache == nil означает, что источников нет вовсе, то есть
// ReasonNotConfigured, а не «всё актуально».
func ComputeUpdates(ctx context.Context, cache *Cache, va wire.VersionAudit) ([]UpdateInfo, []Unknown) {
	var out []UpdateInfo
	var unknown []Unknown

	// Прошивку приносит сам роутер (ndmc components list), и GitHub к ней
	// отношения не имеет: выключенный источник апстрима про неё молчать не
	// должен.
	switch {
	case va.FirmwareCurrent == "":
		unknown = append(unknown, Unknown{Component: "firmware", Reason: ReasonNoSnapshot})
	case va.FirmwareAvail != "" && FirmwareNewerThan(va.FirmwareCurrent, va.FirmwareAvail):
		out = append(out, UpdateInfo{
			Component: "firmware",
			Name:      "KeeneticOS",
			Installed: va.FirmwareCurrent,
			Available: va.FirmwareAvail,
		})
	}

	// Модуль ядра ни с чем не сравнивается: сравнения по парку сегодня нет, и
	// право сказать «нужна перезагрузка» даёт только наблюдаемая смена поля
	// между снимками (RebootHint). Здесь остаётся единственный честный ответ --
	// агент про модуль вовсе не сообщает.
	if va.KmodVersion == "" {
		unknown = append(unknown, Unknown{Component: "kmod", Reason: ReasonAgentTooOld})
	}

	if avail, reason := latestFor(ctx, cache, "awgmgr", va.AwgmgrVersion); reason != "" {
		unknown = append(unknown, Unknown{Component: "awgmgr", Reason: reason})
	} else if SoftwareNewerThan(va.AwgmgrVersion, avail) {
		out = append(out, UpdateInfo{
			Component: "awgmgr",
			Name:      "awg-manager",
			Installed: va.AwgmgrVersion,
			Available: avail,
			Hint:      AwgManagerUpdateHint(va.AwgmgrVersion, avail),
		})
	}

	if avail, reason := latestFor(ctx, cache, "hrneo", va.HrneoVersion); reason != "" {
		unknown = append(unknown, Unknown{Component: "hrneo", Reason: reason})
	} else if SoftwareNewerThan(va.HrneoVersion, avail) {
		out = append(out, UpdateInfo{
			Component: "hrneo",
			Name:      "HydraRoute-Neo",
			Installed: va.HrneoVersion,
			Available: avail,
		})
	}

	return out, unknown
}

// latestFor отвечает либо доступной версией, либо причиной, по которой её
// узнать не удалось. Пустая причина означает, что ответ получен.
//
// Порядок проверок не случаен: сначала «роутер молчит» (нечего сравнивать),
// потом «источник выключен» (нечем сравнивать), и только потом поход в кэш.
// Иначе выключенный источник и молчащий роутер слились бы в одну причину.
func latestFor(ctx context.Context, cache *Cache, source, installed string) (string, string) {
	if installed == "" {
		return "", ReasonNoSnapshot
	}
	if cache == nil || !cache.Configured(source) {
		return "", ReasonNotConfigured
	}
	v, err := cache.Latest(ctx, source)
	if err != nil || v == "" {
		return "", ReasonUnavailable
	}
	return v, ""
}

// AwgManagerUpdateHint -- что обновление панели значит для владельца роутера.
//
// Текст по-русски и о последствии: подсказку читает человек, а не оператор
// бэкенда. Прежняя английская формулировка про NativeWG и 2.10.6 нарушала
// правило продукта и вдобавок опиралась на номер релиза -- связь релиза панели
// со сменой модуля ядра разведка 12.09.2026 подтвердить не смогла (шаг B9:
// сравнения по парку нет, поле выставлено наружу только этим циклом).
//
// Движок (kernel/native) в условии больше не участвует: живой роутер с
// activeBackend=kernel тоже несёт модуль ядра (замер 12.09.2026: kmod
// 3.1.20260906 на KN-1811), и молчать про него значило бы умолчать о настоящем
// последствии. Формулировка «может сменить» честна для обоих движков: здесь мы
// предупреждаем о риске, а о свершившейся смене говорит RebootHint.
func AwgManagerUpdateHint(installed, available string) string {
	if !SoftwareNewerThan(installed, available) {
		return ""
	}
	return "Обновление может сменить модуль ядра, и VPN-туннели поднимутся только после перезагрузки роутера."
}

// RebootHint отвечает по-русски и о последствии. Строится на наблюдаемом факте
// смены модуля ядра между снимками, а не на номере версии панели.
//
// Пустая строка -- это «повода предупреждать нет». Пустой prevKmod означает
// первое знакомство с роутером (сравнивать не с чем), пустой nowKmod -- что
// агент про модуль замолчал; ни то, ни другое сменой не является.
func RebootHint(prevKmod, nowKmod string) string {
	if prevKmod == "" || nowKmod == "" || prevKmod == nowKmod {
		return ""
	}
	return "После обновления панели сменился модуль ядра AmneziaWG. " +
		"VPN-туннели поднимутся только после перезагрузки роутера."
}

// SoftwareNewerThan returns true if `candidate` is strictly newer than
// `installed` in semver order. Returns false if either input is empty or
// not parseable as semver — false-positive update warnings are worse than
// missed ones.
//
// Leading "v" or "V" is stripped before comparison; the canonical "v" prefix
// required by golang.org/x/mod/semver is added back internally.
func SoftwareNewerThan(installed, candidate string) bool {
	if installed == "" || candidate == "" {
		return false
	}
	i := normalize(installed)
	c := normalize(candidate)
	if !semver.IsValid(i) || !semver.IsValid(c) {
		return false
	}
	return semver.Compare(c, i) > 0
}

func normalize(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	return "v" + s
}

// FirmwareNewerThan compares Keenetic firmware versions. KeeneticOS uses a
// dotted format that is not strict semver: it can include uppercase letter
// segments (e.g. "5.00.C.11.0-0") and arbitrary length. Strategy:
//
//  1. Split both on '.'.
//  2. Compare segments pairwise: numeric vs numeric → integer compare;
//     anything else → byte-wise compare.
//  3. If all shared segments equal, longer wins.
//
// Returns false on empty input.
func FirmwareNewerThan(installed, candidate string) bool {
	if installed == "" || candidate == "" {
		return false
	}
	a := strings.Split(installed, ".")
	b := strings.Split(candidate, ".")
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if cmp := compareSeg(a[i], b[i]); cmp != 0 {
			return cmp < 0 // a < b means installed < candidate → candidate newer
		}
	}
	return len(b) > len(a)
}

func compareSeg(a, b string) int {
	ai, aerr := parsePosInt(a)
	bi, berr := parsePosInt(b)
	if aerr == nil && berr == nil {
		switch {
		case ai < bi:
			return -1
		case ai > bi:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(a, b)
}

// parsePosInt is a stripped-down strconv.Atoi that returns an error for
// anything but a non-empty all-digit string. Avoids strconv's wider error
// space and lets compareSeg fall through to lex compare when either side
// has letters or punctuation.
func parsePosInt(s string) (int, error) {
	if s == "" {
		return 0, errNotPosInt
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errNotPosInt
		}
	}
	return strconv.Atoi(s)
}

var errNotPosInt = newErr("not a positive integer")

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }
func newErr(s string) error         { return sentinelErr(s) }
