package main

import (
	"context"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent"
	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
)

// Проверка dns_ru собрана целиком: русское имя -- из эталона, заграничное --
// то же, что у проверки dns, настройки читаются не чаще раза в интервал.
func TestBuildDNSRuCheck_Wired(t *testing.T) {
	cfg := &agent.Config{}
	cfg.Checks.DNS.TestDomain = "example.com"
	c := buildDNSRuCheck(cfg, awgmgr.New("http://127.0.0.1:1"))
	if c == nil {
		t.Fatal("проверка не собрана")
	}
	if c.Group() != "dns_ru" {
		t.Errorf("имя %q", c.Group())
	}
	if c.StatePath != cfg.State.DNSRuStatePath() || c.StatePath == "" {
		t.Errorf("StatePath %q: «ру-апстримы были» не переживёт перезапуск", c.StatePath)
	}
	if c.ForeignName != "example.com" {
		t.Errorf("ForeignName %q", c.ForeignName)
	}
	if c.ConfigInterval != dnsSplitInterval {
		t.Errorf("ConfigInterval = %v, хотим %v", c.ConfigInterval, dnsSplitInterval)
	}
	if c.Endpoints == nil || c.PrepareProbe == nil || c.LocalProbe == nil {
		t.Error("Endpoints, PrepareProbe или LocalProbe не проведены")
	}
}

// Сброс DNS отпускает прочитанные настройки и у dns_ru: иначе до десяти минут
// она пробовала бы апстримы, которых на роутере уже нет.
func TestDNSChangedHook_InvalidatesDNSRu(t *testing.T) {
	list := buildSingleChecks(&agent.Config{}, awgmgr.New("http://127.0.0.1:1"), nil)
	ru := buildDNSRuCheck(&agent.Config{}, awgmgr.New("http://127.0.0.1:1"))
	var reads int
	ru.Endpoints = func(context.Context) ([]keenetic.DNSEndpoint, error) { reads++; return nil, nil }
	ru.ConfigInterval = time.Hour
	ru.StatePath = ""
	ru.Run(context.Background(), checks.Deps{})
	hook := dnsChangedHook(list, []checks.MultiCheck{ru})
	if hook == nil {
		t.Fatal("хук не собран")
	}
	hook()
	ru.Run(context.Background(), checks.Deps{})
	if reads != 2 {
		t.Errorf("настройки прочитаны %d раз, хотим 2", reads)
	}
}
