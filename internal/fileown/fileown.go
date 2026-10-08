//go:build unix

// Package fileown -- новый файл получает владельца своего каталога, когда
// процесс работает от root.
//
// Зачем: контейнер бэкенда на Pi работает от root, а каталог данных и бэкап
// принадлежат пользователю хоста. Атомарная запись (временный файл →
// переименование) от root каждый раз подменяла файл хранилища файлом root
// 0600, и ночной бэкап падал «нет прав» (06.10.2026, awg3-panels.json).
// SQLite делает то же самое для своих -wal/-shm (robustFchown), поэтому
// state.db остался у пользователя.
package fileown

import (
	"os"
	"syscall"
)

// Швы для тестов: от root тесты не запускаются.
var (
	geteuid = os.Geteuid
	chown   = func(f *os.File, uid, gid int) error { return f.Chown(uid, gid) }
)

// MatchDir отдаёт f владельцу каталога dir, если процесс -- root, а каталог
// не принадлежит root. Не от root ничего не делает: сменить владельца
// обычный пользователь всё равно не может, а файл и так его.
func MatchDir(f *os.File, dir string) error {
	if geteuid() != 0 {
		return nil
	}
	st, err := os.Stat(dir)
	if err != nil {
		return err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || (sys.Uid == 0 && sys.Gid == 0) {
		return nil
	}
	return chown(f, int(sys.Uid), int(sys.Gid)) // #nosec G115 -- uid/gid из stat, 32 бита помещаются в int
}
