package main

import (
	"log/slog"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
)

var _ backend.SelfHostedVPS = (*selfhostedamnezia.Service)(nil)

// newSelfHostedService -- свои VPN-серверы для мини-аппа. Пароль SSH из
// backend.yaml (устаревший путь) одноразово переносится в файл своих серверов,
// а в журнал идёт просьба убрать его из YAML (цикл 3, решение 11). Сам пароль
// в журнал не пишется.
func newSelfHostedService(cfg selfhostedamnezia.Config, logger *slog.Logger) *selfhostedamnezia.Service {
	path := cfg.StorePathOrDefault()
	if strings.TrimSpace(cfg.SSHPassword) != "" {
		migrated, err := selfhostedamnezia.MigrateLegacyPassword(path, cfg)
		logger.Warn("amnezia_selfhosted.ssh_password задан в backend.yaml — удалите его оттуда: пароль хранится в файле своих серверов и меняется в приложении",
			"store", path, "migrated", migrated, "err", err)
	}
	return selfhostedamnezia.NewService(path, cfg)
}
