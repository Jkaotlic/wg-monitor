package backend

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel"
	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
)

// LegacyStoreDir -- где JSON-хранилища лежали по умолчанию до v0.52.2. В
// докере это каталог ВНУТРИ контейнера, без тома: 02.10.2026 пересоздание
// контейнера стёрло панели, свои серверы и ключи кабинетов.
const LegacyStoreDir = "/var/lib/wg-monitor"

// Имена JSON-хранилищ: под ними файлы лежат по умолчанию и под ними же
// едут в архив бэкапа, каким бы ни был явно заданный путь.
const (
	amneziaStoreName = "amnezia-premium.json" // #nosec G101 -- имя файла хранилища, не секрет
	hideMyStoreName  = "hidemyname.json"
)

var selfHostedStoreName = filepath.Base(selfhostedamnezia.DefaultStorePath)

// StoreFile -- одно JSON-хранилище вне базы.
type StoreFile struct {
	// Name -- каноническое имя файла (и имя в архиве бэкапа).
	Name string
	// Path -- где файл лежит на самом деле.
	Path string
	// Defaulted -- путь не задан в конфиге и выведен из каталога базы.
	Defaulted bool
}

// storeDefaulted помнит, какие пути вывел ApplyStoreDefaults: только такие
// файлы переносятся со старого места.
type storeDefaulted struct {
	amnezia, selfHosted, hideMy bool
}

// ApplyStoreDefaults кладёт не заданные в конфиге хранилища в каталог базы:
// db_path всегда на томе, значит и они переживут пересоздание контейнера и
// попадут в бэкап. Явно заданные пути не трогает. Повторный вызов безопасен.
func ApplyStoreDefaults(cfg *Config) {
	dir := LegacyStoreDir
	if strings.TrimSpace(cfg.DBPath) != "" {
		dir = filepath.Dir(cfg.DBPath)
	}
	if strings.TrimSpace(cfg.Amnezia.SecretsPath) == "" {
		cfg.Amnezia.SecretsPath = filepath.Join(dir, amneziaStoreName)
		cfg.storeDefaulted.amnezia = true
	}
	if strings.TrimSpace(cfg.SelfHostedAmnezia.StorePath) == "" {
		cfg.SelfHostedAmnezia.StorePath = filepath.Join(dir, selfHostedStoreName)
		cfg.storeDefaulted.selfHosted = true
	}
	if strings.TrimSpace(cfg.HideMy.SecretsPath) == "" {
		cfg.HideMy.SecretsPath = filepath.Join(dir, hideMyStoreName)
		cfg.storeDefaulted.hideMy = true
	}
}

// Awg3StorePath -- файл awg3-панелей рядом с файлом своих серверов: тот же
// каталог состояния, тот же том докера, те же права.
func Awg3StorePath(selfHosted selfhostedamnezia.Config) string {
	return filepath.Join(filepath.Dir(selfHosted.StorePathOrDefault()), awg3panel.DefaultStoreName)
}

// StoreFiles -- все JSON-хранилища бэкенда: для переноса со старого места и
// для бэкапа. Звать после ApplyStoreDefaults (LoadConfig это делает сам).
func (c *Config) StoreFiles() []StoreFile {
	return []StoreFile{
		{Name: amneziaStoreName, Path: c.Amnezia.SecretsPath, Defaulted: c.storeDefaulted.amnezia},
		{Name: selfHostedStoreName, Path: c.SelfHostedAmnezia.StorePathOrDefault(), Defaulted: c.storeDefaulted.selfHosted},
		{Name: awg3panel.DefaultStoreName, Path: Awg3StorePath(c.SelfHostedAmnezia), Defaulted: c.storeDefaulted.selfHosted},
		{Name: hideMyStoreName, Path: c.HideMy.SecretsPath, Defaulted: c.storeDefaulted.hideMy},
	}
}

// MigrateLegacyStores одноразово переносит хранилища со старого места по
// умолчанию (legacyDir) на новое. Переносится только файл, чей путь выведен
// по умолчанию, чьего нового файла ещё нет и чей старый -- обычный файл.
// Существующий новый файл не перезаписывается никогда. Возвращает имена
// перенесённых файлов; в журнал идут только имена, не содержимое.
//
// Сбой переноса -- предупреждение, а не отказ старта: старый файл остаётся
// на месте нетронутым. Заодно убираются временные файлы прерванного переноса.
func MigrateLegacyStores(legacyDir string, stores []StoreFile, logger *slog.Logger) []string {
	var moved []string
	for _, st := range stores {
		if strings.TrimSpace(st.Path) == "" {
			continue
		}
		removeStaleMigrateTemps(st.Path)
		if !st.Defaulted {
			continue
		}
		old := filepath.Join(legacyDir, st.Name)
		if filepath.Clean(old) == filepath.Clean(st.Path) {
			continue
		}
		ok, err := moveStoreFile(old, st.Path)
		if ok {
			logger.Info("хранилище перенесено в каталог базы", "store", st.Name)
			moved = append(moved, st.Name)
			if err != nil {
				// Данные уже на новом месте; старая копия с секретами осталась.
				logger.Warn("хранилище перенесено, но старая копия не удалена — удалите её вручную",
					"store", st.Name, "err", err)
			}
			continue
		}
		if err != nil {
			logger.Warn("хранилище не перенесено в каталог базы — при пересоздании контейнера оно пропадёт",
				"store", st.Name, "err", err)
		}
	}
	return moved
}

// removeStaleMigrateTemps убирает <имя>.migrate-*, оставшиеся рядом с
// хранилищем после падения посреди переноса: в них секреты, а читать их
// некому. Только обычные файлы; перенос идёт в один поток на старте, так
// что живого временного файла здесь быть не может.
func removeStaleMigrateTemps(storePath string) {
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(storePath), globEscape(filepath.Base(storePath))+migrateTempSuffix+"*"))
	if err != nil {
		return
	}
	for _, m := range matches {
		if info, err := os.Lstat(m); err == nil && info.Mode().IsRegular() {
			_ = os.Remove(m)
		}
	}
}

const migrateTempSuffix = ".migrate-"

// globEscape экранирует знаки шаблона в имени файла из конфига.
func globEscape(name string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`).Replace(name)
}

// WarnStoresOutsideDBDir пишет по строке на каждое хранилище, чей явно
// заданный путь лежит вне каталога базы: db_path всегда на томе, а про
// чужой каталог этого никто не обещал. Пути по умолчанию сюда не попадают.
func WarnStoresOutsideDBDir(cfg *Config, logger *slog.Logger) {
	if strings.TrimSpace(cfg.DBPath) == "" {
		return
	}
	dbDir := filepath.Clean(filepath.Dir(cfg.DBPath))
	for _, st := range cfg.StoreFiles() {
		if st.Defaulted || strings.TrimSpace(st.Path) == "" {
			continue
		}
		if filepath.Clean(filepath.Dir(st.Path)) != dbDir {
			logger.Warn("хранилище вне каталога базы — при пересоздании контейнера оно пропадёт",
				"store", st.Name, "path", st.Path)
		}
	}
}

// moveStoreFile переносит src в dst: копия во временный файл рядом с dst
// (0600), fsync, затем жёсткая ссылка на имя dst -- она, в отличие от
// rename, не затирает существующий файл (rename -- лишь там, где ссылок нет). moved=false без ошибки -- переносить
// нечего или новый файл уже есть. moved=true с ошибкой -- файл на новом
// месте, но старую копию убрать не удалось.
func moveStoreFile(src, dst string) (moved bool, err error) {
	if _, err := os.Lstat(dst); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("stat new store: %w", err)
	}
	info, err := os.Lstat(src)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat old store: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return false, fmt.Errorf("create store dir: %w", err)
	}
	in, err := os.Open(src) // #nosec G304 -- путь собран из каталога по умолчанию и постоянного имени хранилища
	if err != nil {
		return false, fmt.Errorf("open old store: %w", err)
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+migrateTempSuffix+"*")
	if err != nil {
		return false, fmt.Errorf("create temp store: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // после успеха имя уже снято -- удаление ничего не делает
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("chmod temp store: %w", err)
	}
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("copy store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("sync store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close temp store: %w", err)
	}
	if err := os.Link(tmpPath, dst); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil // новый файл появился, пока копировали
		}
		// Том без жёстких ссылок: rename, перепроверив, что файла всё ещё нет
		// (перенос идёт на старте, до открытия хранилищ -- писать его некому).
		if _, serr := os.Lstat(dst); !errors.Is(serr, os.ErrNotExist) {
			return false, fmt.Errorf("publish store: %w", err)
		}
		if rerr := os.Rename(tmpPath, dst); rerr != nil {
			return false, fmt.Errorf("publish store: %w", rerr)
		}
	} else if err := os.Remove(tmpPath); err != nil {
		return false, fmt.Errorf("remove temp store: %w", err)
	}
	if d, err := os.Open(filepath.Dir(dst)); err == nil { // #nosec G304 -- каталог нового хранилища из конфига
		_ = d.Sync()
		_ = d.Close()
	}
	// Старый файл убираем последним: до этой строки он цел при любом сбое.
	if err := os.Remove(src); err != nil {
		return true, fmt.Errorf("remove old store: %w", err)
	}
	return true, nil
}
