package actions

import (
	"os"
	"time"
)

// LockState -- что с mkdir-замком (каталог + файл pid владельца внутри).
type LockState int

const (
	// LockFree -- каталога замка нет.
	LockFree LockState = iota
	// LockHeld -- pid владельца записан, процесс жив.
	LockHeld
	// LockHeldUnknownOwner -- pid нет (старый cron-скрипт или владелец ещё
	// не успел записать), а замок моложе staleAfter.
	LockHeldUnknownOwner
	// LockAbandoned -- владелец мёртв (kill -9, OOM) или pid нет и замок
	// старше staleAfter: можно снимать.
	LockAbandoned
)

// Held -- замок занят живым или неизвестным владельцем.
func (s LockState) Held() bool { return s == LockHeld || s == LockHeldUnknownOwner }

// MkdirLockState -- жив ли mkdir-замок dir: pid из dir/pid жив -- занят;
// pid есть, процесса нет -- брошен сразу; pid нет -- по возрасту каталога
// (старше staleAfter -- брошен). Общий для upgradeInProgress сторожа и
// takeSharedLock (AG-06); cron-скрипт делает то же через kill -0.
func MkdirLockState(dir string, now time.Time, staleAfter time.Duration) LockState {
	st, err := os.Stat(dir)
	if err != nil {
		return LockFree
	}
	switch pid, alive := opkgLockOwner(dir); {
	case pid > 0 && alive:
		return LockHeld
	case pid > 0:
		return LockAbandoned
	}
	if now.Sub(st.ModTime()) > staleAfter {
		return LockAbandoned
	}
	return LockHeldUnknownOwner
}
