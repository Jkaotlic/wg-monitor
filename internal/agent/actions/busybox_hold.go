package actions

import (
	"context"
	"slices"
	"strings"
)

// HoldBusybox changes only the package hold flag; it never updates package feeds
// or installs packages. Use the same locks as upgrades, including cron.
func (o *OpkgRunner) HoldBusybox(ctx context.Context) (status, output string) {
	if _, _, ok := o.lockHeldFresh(); ok {
		return "locked", opkgLockHeldText
	}
	if err := o.releaseStaleLock(); err != nil {
		return "err", "clear stale lock: " + err.Error()
	}
	if err := o.takeLock(); err != nil {
		if text, busy := opkgLockBusyText(err); busy {
			return "locked", text
		}
		return "err", "acquire lock: " + err.Error()
	}
	defer o.releaseLock()
	out, err := o.Exec(ctx, "opkg", "flag", "hold", "busybox")
	if err != nil {
		return "err", "Не удалось установить hold BusyBox: " + err.Error() + "\n" + string(out)
	}
	out, err = o.Exec(ctx, "opkg", "status", "busybox")
	if err != nil {
		return "err", "Не удалось проверить hold BusyBox: " + err.Error() + "\n" + string(out)
	}
	for _, record := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n\n") {
		pkg, held, installed := "", false, false
		for _, line := range strings.Split(record, "\n") {
			if value, ok := strings.CutPrefix(line, "Package:"); ok {
				pkg = strings.TrimSpace(value)
			}
			if value, ok := strings.CutPrefix(line, "Status:"); ok {
				fields := strings.Fields(value)
				held = len(fields) == 3 && slices.Contains(strings.Split(fields[1], ","), "hold")
				installed = len(fields) == 3 && fields[0] == "install" && fields[2] == "installed"
			}
		}
		if pkg == "busybox" && held && installed {
			return "ok", "BusyBox: hold подтверждён. Обновление пакета заблокировано; остальные пакеты не изменены."
		}
	}
	return "err", "Hold BusyBox НЕ подтверждён: пакет отсутствует, не установлен или флаг не записан. Защита не подтверждена."
}
