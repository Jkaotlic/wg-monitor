// Package routerscripts -- шелл-скрипты, которые агент ставит на роутер.
//
// Каждый скрипт -- обычный файл рядом с этим пакетом, а не строка в Go-коде:
// его ставит агент, и тот же файл можно поставить руками (как -- в README.md
// рядом). Источник один, копии не расходятся.
//
// Значения, которые агент подставляет (пути, пороги), стоят в скрипте
// строками `ИМЯ=значение` сверху; в файле -- рабочие значения по умолчанию,
// поэтому скрипт годен и без агента. Подстановка -- Render.
package routerscripts

import (
	_ "embed"
	"fmt"
	"strings"
)

// EntwareCleanup -- очистка временных файлов Entware (v0.57: вынесена из
// fmt.Sprintf агента). Подставляются LOG, MIN_FREE_KB, MIN_MEM_AVAILABLE_KB,
// MAX_LOG_KB.
//
//go:embed entware-cleanup.sh
var EntwareCleanup string

// Porthop -- смена исходящего порта VPN-туннеля, чей поток убила блокировка:
// ручной скрипт оператора (v0.57 -- в репозитории, ядро без изменений).
//
//go:embed awg-porthop.sh
var Porthop string

// PorthopInit -- init-скрипт Entware для Porthop (start/stop/restart/status
// по pid-файлу).
//
//go:embed S99wg-monitor-porthop
var PorthopInit string

// Render заменяет в script строки `key=...` (с начала строки) на `key=value`.
// Каждый ключ обязан встретиться ровно один раз: иначе подстановка молча
// ничего бы не сделала, и на роутер уехало бы значение по умолчанию.
// value подставляется как есть -- кавычки, если нужны, ставит вызывающий.
func Render(script string, values [][2]string) (string, error) {
	lines := strings.Split(script, "\n")
	for _, kv := range values {
		prefix := kv[0] + "="
		hits := 0
		for i, l := range lines {
			if strings.HasPrefix(l, prefix) {
				lines[i] = prefix + kv[1]
				hits++
			}
		}
		if hits != 1 {
			return "", fmt.Errorf("routerscripts: key %s found %d times, want exactly 1", kv[0], hits)
		}
	}
	return strings.Join(lines, "\n"), nil
}
