package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
)

// Кабинет провайдера, которого нет: подписка, устройства и список стран.
// Без него экран кабинетов отвечает «не настроено» -- честно, но проверить
// на этом ответе можно ровно одну ветку из десяти.
type fakeCabinet struct {
	mu     sync.Mutex
	issued map[string]bool
}

func (c *fakeCabinet) Account(_ context.Context, _ int64, provider string) (backend.VPNAccount, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.issued == nil {
		c.issued = map[string]bool{}
	}
	switch provider {
	case "amnezia":
		return backend.VPNAccount{
			Provider: "amnezia", Label: "Amnezia Premium", Connected: true,
			Status: "active", EndsAt: time.Now().Add(200 * 24 * time.Hour).Format("2006-01-02"),
			DevicesUsed: 3, DevicesMax: 5,
			Options: []backend.VPNOption{
				{ID: "nl", Label: "Нидерланды", Issued: c.issued["nl"]},
				{ID: "de", Label: "Германия", Issued: c.issued["de"]},
				{ID: "fi", Label: "Финляндия"},
			},
		}, nil
	case "hidemyname":
		// Второй кабинет намеренно не подключён: у экрана есть отдельная
		// ветка «ключа нет», и её тоже надо чем-то показывать.
		return backend.VPNAccount{
			Provider: "hidemyname", Label: "HideMy.name", Connected: false,
			Note: "ключ кабинета для этого роутера не сохранён",
		}, nil
	}
	return backend.VPNAccount{}, fmt.Errorf("песочница: кабинет %q неизвестен", provider)
}

func (c *fakeCabinet) IssueConfig(_ context.Context, _ int64, provider, optionID string) (backend.VPNIssuedConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.issued == nil {
		c.issued = map[string]bool{}
	}
	c.issued[optionID] = true
	return backend.VPNIssuedConfig{
		TunnelName: provider + "_" + optionID,
		Conf:       []byte("[Interface]\nPrivateKey = sandbox\n"),
		Backend:    "nativewg",
	}, nil
}
