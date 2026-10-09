// Package unstick -- сторож зависших состояний awg-manager (v0.59): туннель,
// который дольше порога висит в broken / needs_start / needs_stop /
// starting / stopping, агент сам приводит к намерению владельца
// (spec 2026-10-09-v059-awgm-unstick-design.md).
package unstick

// Kind -- вид зависания; от него зависит порог.
type Kind int

const (
	KindNone       Kind = iota // не зависание -- не трогать
	KindBroken                 // broken: должен работать, но не поднялся
	KindNeeds                  // needs_start / needs_stop: намерение и факт разошлись
	KindTransition             // starting / stopping, которые не кончаются
)

// Remedy -- действие ступени 1.
type Remedy string

const (
	RemedyRestart Remedy = "restart"
	RemedyStart   Remedy = "start"
	RemedyStop    Remedy = "stop"
)

// Classify относит статус awg-manager к виду зависания и лекарству.
// Выключенный владельцем туннель (enabled=false): только остановка или ничего,
// никогда не запуск. Включённый в stopping: перезапуск, в needs_stop: запуск
// (цель включённого -- running).
func Classify(status string, enabled bool) (Kind, Remedy) {
	switch status {
	case "broken":
		if enabled {
			return KindBroken, RemedyRestart
		}
		return KindBroken, RemedyStop
	case "needs_start":
		if enabled {
			return KindNeeds, RemedyStart
		}
	case "needs_stop":
		if !enabled {
			return KindNeeds, RemedyStop
		}
		// Включён, но needs_stop («conf disabled, процесс жив»): тумблер в
		// KeenOS awg-manager сам за секунды доводит до enabled=false, так что
		// минутное сочетание -- не намерение владельца, а несогласованность,
		// из которой awg-manager не выходит (его #669). Живая приёмка
		// 09.10.2026: stop → enable на workrouter, start поднял за 10 с.
		return KindNeeds, RemedyStart
	case "starting":
		if enabled {
			return KindTransition, RemedyRestart
		}
		return KindTransition, RemedyStop
	case "stopping":
		if enabled {
			return KindTransition, RemedyRestart
		}
		return KindTransition, RemedyStop
	}
	return KindNone, ""
}

// Resolved -- туннель пришёл к намерению владельца: включённый работает,
// выключенный остановлен.
func Resolved(status string, enabled bool) bool {
	if enabled {
		return status == "running"
	}
	switch status {
	case "stopped", "disabled", "not_created":
		return true
	}
	return false
}

// KnownStatus -- словарь статусов awg-manager 2.19 (бандл UI, сверено 09.10).
// Незнакомый статус сторож не трогает и один раз пишет в журнал.
func KnownStatus(status string) bool {
	switch status {
	case "running", "starting", "stopping", "needs_start", "needs_stop",
		"broken", "stopped", "not_created", "disabled":
		return true
	}
	return false
}
