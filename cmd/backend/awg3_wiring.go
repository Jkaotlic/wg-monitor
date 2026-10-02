package main

import (
	"log/slog"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel"
	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
)

var _ backend.Awg3Panels = (*awg3panel.Service)(nil)

// awg3StorePath -- файл awg3-панелей рядом с файлом своих серверов (правило
// живёт в backend.Awg3StorePath: по нему же файл попадает в бэкап).
func awg3StorePath(selfHosted selfhostedamnezia.Config) string {
	return backend.Awg3StorePath(selfHosted)
}

// newAwg3PanelService -- панели без фонового опроса; корни TLS -- системные
// (Caddy на VPS отдаёт публичный сертификат).
func newAwg3PanelService(selfHosted selfhostedamnezia.Config, logger *slog.Logger) *awg3panel.Service {
	return awg3panel.NewService(awg3StorePath(selfHosted), awg3panel.Options{Logger: logger})
}
