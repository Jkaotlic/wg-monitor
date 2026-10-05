package main

import (
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
)

// Ревью v0.56, M4: флаг alerts.bypass_leak.enabled доходит до напоминаний.
func TestRealertConfig_CarriesBypassLeakFlag(t *testing.T) {
	for _, on := range []bool{false, true} {
		cfg := &backend.Config{}
		cfg.Alerts.BypassLeak.Enabled = on
		cfg.PublicBaseURL = "https://example.com"
		cfg.Telegram.AdminUserID = 42
		got := realertConfig(cfg)
		if got.BypassLeakEnabled != on {
			t.Fatalf("enabled=%v: в настройки напоминаний дошло %v", on, got.BypassLeakEnabled)
		}
		if got.MiniAppBaseURL != "https://example.com" || got.AdminUserID != 42 {
			t.Fatalf("прочие поля потерялись: %+v", got)
		}
	}
}
