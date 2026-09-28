// Package wakehook -- мгновенная реакция на смену интерфейса через ndm-хук
// KeenOS (/opt/etc/ndm/ifstatechanged.d). Хук только трогает файл; агент
// замечает новое время изменения и шлёт внеочередной отчёт.
//
// Почему файл, а не сигнал: у Go-процесса без обработчика SIGUSR1 действие
// по умолчанию -- завершение. Хук переживёт откат агента на v0.46, и тот
// умирал бы на каждой смене интерфейса. touch безвреден любой версии.
//
// Соседи: awg-manager ставит свои ndm-хуки (POST /api/hook/ndms, сверка С1).
// Наш -- своё имя, поздний порядок (90-), без вывода, всегда exit 0.
package wakehook

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	DefaultDir      = "/opt/etc/ndm/ifstatechanged.d"
	DefaultWakeFile = "/opt/var/run/wg-monitor.wake"
	ScriptName      = "90-wg-monitor.sh"

	StateInstalled   = "installed"
	StateUnsupported = "unsupported"
	StateDisabled    = "disabled"
	StateError       = "error"
)

// Script -- содержимое хука. От переменных ndm (id, change, up...) не
// зависит намеренно: до сверки С2 их набор на прошивках не проверен, а
// лишнее пробуждение гасит антишторм агента.
func Script(wakeFile string) string {
	return "#!/bin/sh\n" +
		"# wg-monitor: будит агента при смене состояния интерфейса.\n" +
		"# Ставит и снимает агент сам (v0.47). Ничего не печатает и всегда выходит с 0,\n" +
		"# чтобы не мешать соседним хукам (awg-manager ставит свои).\n" +
		"touch " + wakeFile + " 2>/dev/null\n" +
		"exit 0\n"
}

// Ensure ставит или снимает свой хук. Каталог хуков не создаёт: его нет --
// прошивка хуки не поддерживает, и агент работает чистым опросом.
func Ensure(dir, wakeFile string, enabled bool) (string, string) {
	path := filepath.Join(dir, ScriptName)
	if !enabled {
		for _, p := range []string{path, tempPath(dir), legacyTempPath(dir)} {
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return StateError, err.Error()
			}
		}
		return StateDisabled, ""
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return StateUnsupported, ""
	}
	if err := os.MkdirAll(filepath.Dir(wakeFile), 0o755); err != nil {
		return StateError, err.Error()
	}
	want := Script(wakeFile)
	if cur, err := os.ReadFile(path); err == nil && string(cur) == want {
		return StateInstalled, ""
	}
	tmp := tempPath(dir)
	// Остаток прошлой попытки сохранил бы свои права при WriteFile.
	_ = os.Remove(tmp)
	_ = os.Remove(legacyTempPath(dir))
	if err := os.WriteFile(tmp, []byte(want), 0o600); err != nil {
		_ = os.Remove(tmp)
		return StateError, err.Error()
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return StateError, err.Error()
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return StateError, err.Error()
	}
	return StateInstalled, ""
}

// tempPath -- временный файл установки. Точка в начале и права 0600 до самого
// rename: ndm исполняет каталог хуков целиком, и недописанный скрипт не
// должен туда попасть исполняемым.
func tempPath(dir string) string { return filepath.Join(dir, "."+ScriptName+".tmp") }

// legacyTempPath -- имя временного файла в ранних сборках v0.47 (без точки,
// 0755). Снимается при выключении и деинсталляции.
func legacyTempPath(dir string) string { return filepath.Join(dir, ScriptName+".tmp") }
