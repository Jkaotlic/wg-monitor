package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StatusFileName -- файл состояния бэкапа; лежит рядом с базой (на томе).
// Пишет CLI (`backup`, `backup verify`), читает бэкенд.
const StatusFileName = "backup-status.json"

// Куда ушёл архив: значения полей telegram и offsite.
const (
	DeliveryOK    = "ok"
	DeliveryError = "error"
	DeliveryOff   = "off" // не настроено или не положено этому виду
)

// MaxStatusErrorRunes -- предел длины текста ошибки в файле состояния.
const MaxStatusErrorRunes = 300

// Status -- содержимое backup-status.json. Форма стабильна: её читают
// бэкенд и мини-апп.
type Status struct {
	Version int          `json:"version"`
	Small   KindStatus   `json:"small"`
	Full    KindStatus   `json:"full"`
	Verify  VerifyStatus `json:"verify"`
}

// KindStatus -- итог последнего прогона одного вида архива. Пустой
// LastRunAt -- этот вид ещё ни разу не запускался.
type KindStatus struct {
	// LastOKAt -- время последнего полностью удачного прогона (RFC 3339, UTC)
	// или пустая строка. Неудачный прогон его не трогает.
	LastOKAt  string `json:"last_ok_at"`
	LastRunAt string `json:"last_run_at"`
	// OK -- последний прогон прошёл целиком: архив записан и ушёл всюду,
	// куда настроен.
	OK bool `json:"ok"`
	// SizeBytes и File -- архив последнего прогона; 0 и "" -- архив не записан.
	SizeBytes int64  `json:"size_bytes"`
	File      string `json:"file"`
	Telegram  string `json:"telegram"`
	// Offsite есть только у полного архива.
	Offsite string `json:"offsite,omitempty"`
	// Error -- короткий текст без секретов; пусто при OK.
	Error string `json:"error"`
}

// VerifyStatus -- итог последней проверки восстановления (`backup verify`).
type VerifyStatus struct {
	LastRunAt string `json:"last_run_at"`
	OK        bool   `json:"ok"`
	Error     string `json:"error"`
	// Routers -- сколько роутеров в проверенном архиве.
	Routers int `json:"routers"`
}

// StatusPath -- путь файла состояния для базы dbPath.
func StatusPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), StatusFileName)
}

// LoadStatus читает файл состояния. Файла нет -- ошибка os.ErrNotExist
// (бэкап ещё не запускался).
func LoadStatus(path string) (Status, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- путь выведен из db_path конфига
	if err != nil {
		return Status{}, err
	}
	var s Status
	if err := json.Unmarshal(body, &s); err != nil {
		return Status{}, fmt.Errorf("parse %s: %w", StatusFileName, err)
	}
	return s, nil
}

// UpdateStatus читает файл, даёт mutate поправить свою секцию и атомарно
// записывает обратно (временный файл рядом + rename): остальные секции
// сохраняются, читатель никогда не видит половину файла. Отсутствующий или
// битый файл -- не помеха: состояние начинается с чистого листа.
//
// Права 0644: файл читает бэкенд из контейнера под другим пользователем;
// секретов в нём нет по построению.
func UpdateStatus(path string, mutate func(*Status)) error {
	s, _ := LoadStatus(path)
	mutate(&s)
	s.Version = 1
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+StatusFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp status file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // после rename имени уже нет
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write status file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil { // #nosec G302 -- файл без секретов, читается из контейнера
		_ = tmp.Close()
		return fmt.Errorf("chmod status file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync status file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close status file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish status file: %w", err)
	}
	return nil
}

// StatusErrorText приводит текст ошибки к виду для файла состояния: одна
// строка, не длиннее MaxStatusErrorRunes. Секреты вычищает вызывающий --
// эта функция про форму, а не про содержание.
func StatusErrorText(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	if r := []rune(msg); len(r) > MaxStatusErrorRunes {
		msg = string(r[:MaxStatusErrorRunes-1]) + "…"
	}
	return msg
}
