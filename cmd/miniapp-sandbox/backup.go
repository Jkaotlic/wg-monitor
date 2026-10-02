package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backup"
)

// Состояния бэкапа, которые песочница умеет показать в Парке (флаг -backup).
const (
	sandboxBackupGood    = "good"    // всё удалось
	sandboxBackupFailed  = "failed"  // малый не ушёл, полный не скопирован, проверка не прошла
	sandboxBackupUnknown = "unknown" // файла состояния нет
	sandboxBackupOff     = "off"     // источник не подключён вовсе (старый бэкенд)
)

// seedBackupStatus кладёт backup-status.json рядом с базой песочницы -- туда,
// где его ищет настоящий бэкенд (backup.StatusPath). Времена -- от now.
func seedBackupStatus(dbPath, mode string, now time.Time) error {
	ts := func(ago time.Duration) string { return now.Add(-ago).UTC().Format(time.RFC3339) }
	path := backup.StatusPath(dbPath)
	switch mode {
	case sandboxBackupGood:
		return backup.UpdateStatus(path, func(s *backup.Status) {
			s.Small = backup.KindStatus{LastOKAt: ts(3 * time.Hour), LastRunAt: ts(3 * time.Hour), OK: true,
				SizeBytes: 4 << 20, File: "wg-monitor-small-backup-sandbox.tgz.enc", Telegram: backup.DeliveryOK}
			s.Full = backup.KindStatus{LastOKAt: ts(3 * time.Hour), LastRunAt: ts(3 * time.Hour), OK: true,
				SizeBytes: 190 << 20, File: "wg-monitor-full-backup-sandbox.tgz.enc", Telegram: backup.DeliveryOff, Offsite: backup.DeliveryOK}
			s.Verify = backup.VerifyStatus{LastRunAt: ts(60 * time.Hour), OK: true, Routers: 3}
		})
	case sandboxBackupFailed:
		return backup.UpdateStatus(path, func(s *backup.Status) {
			s.Small = backup.KindStatus{LastOKAt: ts(100 * time.Hour), LastRunAt: ts(3 * time.Hour), OK: false,
				SizeBytes: 60 << 20, File: "wg-monitor-small-backup-sandbox.tgz.enc", Telegram: backup.DeliveryError,
				Error: "архив не ушёл в Telegram: он 60,0 МБ, а Telegram принимает до 45,0 МБ"}
			s.Full = backup.KindStatus{LastOKAt: ts(100 * time.Hour), LastRunAt: ts(3 * time.Hour), OK: false,
				SizeBytes: 190 << 20, File: "wg-monitor-full-backup-sandbox.tgz.enc", Telegram: backup.DeliveryOff, Offsite: backup.DeliveryError,
				Error: "архив не скопирован на внешний сервер: сервер недоступен"}
			s.Verify = backup.VerifyStatus{LastRunAt: ts(60 * time.Hour), OK: false,
				Error: "в архиве операторов 1, в манифесте архива 2"}
		})
	case sandboxBackupUnknown, sandboxBackupOff:
		return nil
	}
	return fmt.Errorf("неизвестное состояние бэкапа %q (good|failed|unknown|off)", mode)
}

// sandboxBackupPath -- где лежит файл состояния для базы песочницы.
func sandboxBackupPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), backup.StatusFileName)
}
