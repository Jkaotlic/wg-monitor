package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// hrneoInitScript -- тот же скрипт, что у service_restart hrneo.
const hrneoInitScript = "/opt/etc/init.d/S99hrneo"

// hrneoSettle -- сколько дать HydraRoute подняться перед pidof.
const hrneoSettle = 3 * time.Second

// HrneoUpdate обновляет пакет hrneo под общим lock-файлом opkg.
//
// Своего эндпоинта обновления у HydraRoute Neo нет, поэтому путь -- opkg:
// update, проверка «hrneo в list-upgradable», место по формуле SmartUpgrade,
// upgrade hrneo, перезапуск, постусловие «версия сменилась и HydraRoute
// работает».
func (o *OpkgRunner) HrneoUpdate(ctx context.Context) (status, output string) {
	if _, _, ok := o.lockHeldFresh(); ok {
		return "locked", "на роутере уже идёт другая операция с пакетами — повторите через пару минут"
	}
	if err := o.releaseStaleLock(); err != nil {
		return "err", "clear stale lock: " + err.Error()
	}
	if err := o.takeLock(); err != nil {
		return "err", "acquire lock: " + err.Error()
	}
	defer o.releaseLock()

	from, installed := o.hrneoInstalledVersion(ctx)
	if !installed {
		return "err", "HydraRoute Neo не установлен"
	}
	updateOut, updateErr := o.Exec(ctx, "opkg", "update")
	if upd := parseOpkgUpdate(string(updateOut)); updateErr != nil && upd.feedsUpdated == 0 {
		return "err", "не удалось обновить списки пакетов: " + updateErr.Error()
	}
	listing, err := o.Exec(ctx, "opkg", "list-upgradable")
	if err != nil {
		return "err", "не удалось получить список обновлений: " + err.Error()
	}
	if !slices.Contains(parseUpgradablePkgs(string(listing)), "hrneo") {
		return encodeHrneoUpdate(wire.HrneoUpdateResult{From: from, To: from, Running: o.hrneoRunning(ctx)})
	}
	freeKB, totalKB, err := o.dfOpt(ctx)
	if err != nil {
		return "err", "не удалось узнать свободное место: " + err.Error()
	}
	neededKB := o.estimateInstallSizeKB(ctx, []string{"hrneo"})
	if ok, headroomKB := spaceVerdict(freeKB, totalKB, neededKB); !ok {
		return "err", fmt.Sprintf("Не хватит места на /opt: обновлению нужно %s, свободно %s из %s, а после установки должно остаться не меньше %s.",
			humanKB(neededKB), humanKB(freeKB), humanKB(totalKB), humanKB(headroomKB))
	}
	if out, err := o.Exec(ctx, "opkg", "upgrade", "hrneo"); err != nil {
		return "err", fmt.Sprintf("opkg upgrade hrneo: %v\n%s", err, out)
	}
	if out, err := o.Exec(ctx, hrneoInitScript, "restart"); err != nil {
		return "err", fmt.Sprintf("HydraRoute Neo обновлён, но перезапуск не удался: %v\n%s", err, out)
	}
	_ = o.sleep(ctx, hrneoSettle)
	to, _ := o.hrneoInstalledVersion(ctx)
	running := o.hrneoRunning(ctx)
	if to == "" || to == from {
		return "err", fmt.Sprintf("HydraRoute Neo не обновился: версия осталась %s", from)
	}
	if !running {
		return "err", fmt.Sprintf("HydraRoute Neo обновлён %s → %s, но не запустился после перезапуска", from, to)
	}
	return encodeHrneoUpdate(wire.HrneoUpdateResult{Updated: true, From: from, To: to, Running: true})
}

func (o *OpkgRunner) sleep(ctx context.Context, d time.Duration) error {
	if o.Sleep != nil {
		return o.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (o *OpkgRunner) hrneoInstalledVersion(ctx context.Context) (string, bool) {
	out, err := o.Exec(ctx, "opkg", "info", "hrneo")
	if err != nil {
		return "", false
	}
	return installedVersionFromOpkgInfo(string(out))
}

func (o *OpkgRunner) hrneoRunning(ctx context.Context) bool {
	_, err := o.Exec(ctx, "pidof", "hrneo")
	return err == nil
}

// installedVersionFromOpkgInfo -- версия УСТАНОВЛЕННОГО пакета из `opkg info`.
// После `opkg update` вывод несёт и кандидатов из фидов, блоками через пустую
// строку; установленный -- тот, у кого Status кончается на " installed".
// Сборки opkg без строки Status отдают один блок -- берём его версию.
// Версия сравнивается целиком, с ревизией: смена 3.18.3-1 → 3.18.3-2 -- тоже
// обновление.
func installedVersionFromOpkgInfo(s string) (string, bool) {
	firstVersion := ""
	for _, block := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n\n") {
		version, status := "", ""
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "Version:"); ok {
				version = strings.TrimSpace(v)
			}
			if v, ok := strings.CutPrefix(line, "Status:"); ok {
				status = strings.TrimSpace(v)
			}
		}
		if version == "" {
			continue
		}
		if status != "" {
			if strings.HasSuffix(status, " installed") && !strings.Contains(status, "not-installed") {
				return version, true
			}
			continue
		}
		if firstVersion == "" {
			firstVersion = version
		}
	}
	if firstVersion != "" {
		return firstVersion, true
	}
	return "", false
}

// spaceVerdict -- формула места SmartUpgrade: после установки свободного
// должно остаться не меньше 10% раздела.
func spaceVerdict(freeKB, totalKB, neededKB int64) (bool, int64) {
	headroomKB := totalKB / 10
	return freeKB-neededKB >= headroomKB, headroomKB
}

func encodeHrneoUpdate(res wire.HrneoUpdateResult) (string, string) {
	b, err := json.Marshal(res)
	if err != nil {
		return "err", "encode hrneo_update: " + err.Error()
	}
	return "ok", string(b)
}
