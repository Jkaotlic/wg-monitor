package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/hidemy"
)

// Кабинеты провайдеров, которых нет: подписка, страны, серверы, ключи и коды.
// Состояния подобраны под приёмку экрана кабинета: слоты Amnezia заняты
// (выпуск новой страны -- отказ «отзовите»), у занятых стран есть «Отозвать»,
// ключ с «bad» и код с «000» в начале кабинет не принимает.

const sandboxAmneziaSlots = 2

type fakeCabinet struct {
	mu      sync.Mutex
	issued  map[string]bool                    // страны Amnezia, занявшие слот
	secrets map[string][]backend.CabinetSecret // "<router>/<provider>" -> ключи
	seeded  map[int64]bool
	nextID  int
}

var _ backend.VPNCabinetKeys = (*fakeCabinet)(nil)

var sandboxCountries = []backend.VPNOption{
	{ID: "nl", Label: "Нидерланды"}, {ID: "de", Label: "Германия"}, {ID: "fi", Label: "Финляндия"},
	{ID: "se", Label: "Швеция"}, {ID: "us", Label: "США"},
}

var sandboxHideMyServers = []backend.VPNOption{
	{ID: "a1b2c3d4e5f6", Label: "Нидерланды, Амстердам"},
	{ID: "0f1e2d3c4b5a", Label: "Германия, Франкфурт"},
	{ID: "9a8b7c6d5e4f", Label: "Финляндия, Хельсинки"},
}

func secretsKey(routerID int64, provider string) string {
	return fmt.Sprintf("%d/%s", routerID, provider)
}

// lockedSeed -- у каждого роутера при первом обращении есть ключ и код: экран
// открывается «подключённым», а пустое состояние получается удалением.
func (c *fakeCabinet) lockedSeed(routerID int64) {
	if c.issued == nil {
		c.issued = map[string]bool{"nl": true, "de": true}
		c.secrets = map[string][]backend.CabinetSecret{}
		c.seeded = map[int64]bool{}
	}
	if c.seeded[routerID] {
		return
	}
	c.seeded[routerID] = true
	c.secrets[secretsKey(routerID, "amnezia")] = []backend.CabinetSecret{{ID: "sandbox-key-1", Label: "Основной", Mask: "••••Qx7A", Active: true}}
	c.secrets[secretsKey(routerID, "hidemyname")] = []backend.CabinetSecret{{ID: "sandbox-code-1", Label: "Код #1", Mask: "••••4821", Active: true}}
}

func (c *fakeCabinet) lockedActive(routerID int64, provider string) bool {
	for _, s := range c.secrets[secretsKey(routerID, provider)] {
		if s.Active {
			return true
		}
	}
	return false
}

func (c *fakeCabinet) Account(_ context.Context, routerID int64, provider string) (backend.VPNAccount, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lockedSeed(routerID)
	switch provider {
	case "amnezia":
		acc := backend.VPNAccount{Provider: "amnezia", Label: "Amnezia Premium"}
		if !c.lockedActive(routerID, provider) {
			acc.Note = "Ключ кабинета не сохранён — добавьте ключ vpn:// кнопкой «Добавить ключ»."
			return acc, nil
		}
		acc.Connected, acc.Status = true, "active"
		acc.EndsAt = time.Now().Add(200 * 24 * time.Hour).Format("2006-01-02")
		acc.DevicesUsed, acc.DevicesMax = len(c.issued), sandboxAmneziaSlots
		for _, o := range sandboxCountries {
			o.Issued = c.issued[o.ID]
			acc.Options = append(acc.Options, o)
		}
		return acc, nil
	case "hidemyname":
		acc := backend.VPNAccount{Provider: "hidemyname", Label: "HideMy.name"}
		if !c.lockedActive(routerID, provider) {
			acc.Note = "Код доступа не сохранён — добавьте его кнопкой «Добавить код»."
			return acc, nil
		}
		acc.Connected = true
		acc.Options = append(acc.Options, sandboxHideMyServers...)
		return acc, nil
	}
	return backend.VPNAccount{}, fmt.Errorf("песочница: кабинет %q неизвестен", provider)
}

func (c *fakeCabinet) IssueConfig(_ context.Context, routerID int64, provider, optionID string) (backend.VPNIssuedConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lockedSeed(routerID)
	if !c.lockedActive(routerID, provider) {
		return backend.VPNIssuedConfig{}, fmt.Errorf("песочница: ключ кабинета %s не сохранён", provider)
	}
	switch provider {
	case "amnezia":
		if !c.issued[optionID] && len(c.issued) >= sandboxAmneziaSlots {
			return backend.VPNIssuedConfig{}, backend.ErrVPNSlotBusy
		}
		c.issued[optionID] = true
	case "hidemyname":
	default:
		return backend.VPNIssuedConfig{}, fmt.Errorf("песочница: кабинет %q неизвестен", provider)
	}
	name := provider + "_" + optionID
	if provider == "hidemyname" {
		name = "hidemy_" + optionID
	}
	return backend.VPNIssuedConfig{
		TunnelName: name,
		Conf:       []byte("[Interface]\nPrivateKey = sandbox\n\n[Peer]\nPublicKey = sandbox\n"),
		Backend:    "nativewg",
	}, nil
}

func (c *fakeCabinet) Secrets(routerID int64, provider string) ([]backend.CabinetSecret, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lockedSeed(routerID)
	return append([]backend.CabinetSecret{}, c.secrets[secretsKey(routerID, provider)]...), nil
}

func (c *fakeCabinet) AddSecret(_ context.Context, routerID int64, provider, secret, label string) (backend.CabinetSecret, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lockedSeed(routerID)
	secret = strings.TrimSpace(secret)
	defaultLabel := "Ключ #%d"
	switch provider {
	case "amnezia":
		if !strings.HasPrefix(secret, "vpn://") {
			return backend.CabinetSecret{}, backend.ErrCabinetSecretInvalid
		}
		if strings.Contains(secret, "bad") {
			return backend.CabinetSecret{}, &backend.CabinetRejectedError{Reason: "Кабинет Amnezia Premium не принял ключ или не ответил — проверьте ключ и повторите"}
		}
	case "hidemyname":
		defaultLabel = "Код #%d"
		if !hidemy.ValidAccessCode(secret) {
			return backend.CabinetSecret{}, backend.ErrCabinetSecretInvalid
		}
		if strings.HasPrefix(secret, "000") {
			return backend.CabinetSecret{}, &backend.CabinetRejectedError{Reason: "Кабинет HideMy.name не принял код или не ответил — проверьте код и повторите"}
		}
	default:
		return backend.CabinetSecret{}, fmt.Errorf("песочница: кабинет %q неизвестен", provider)
	}
	time.Sleep(700 * time.Millisecond) // «проверка в кабинете» -- чтобы экран показал ожидание
	key := secretsKey(routerID, provider)
	list := c.secrets[key]
	if label == "" {
		label = fmt.Sprintf(defaultLabel, len(list)+1)
	}
	c.nextID++
	added := backend.CabinetSecret{ID: fmt.Sprintf("sandbox-%s-%d", provider, c.nextID), Label: label, Mask: "••••" + secret[len(secret)-4:], Active: true}
	for i := range list {
		list[i].Active = false
	}
	c.secrets[key] = append(list, added)
	return added, nil
}

func (c *fakeCabinet) SetActiveSecret(routerID int64, provider, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lockedSeed(routerID)
	list := c.secrets[secretsKey(routerID, provider)]
	found := false
	for _, s := range list {
		found = found || s.ID == id
	}
	if !found {
		return backend.ErrCabinetSecretNotFound
	}
	for i := range list {
		list[i].Active = list[i].ID == id
	}
	return nil
}

func (c *fakeCabinet) DeleteSecret(routerID int64, provider, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lockedSeed(routerID)
	key := secretsKey(routerID, provider)
	list := c.secrets[key]
	for i, s := range list {
		if s.ID != id {
			continue
		}
		next := append(list[:i:i], list[i+1:]...)
		if s.Active && len(next) > 0 {
			next[0].Active = true
		}
		c.secrets[key] = next
		return nil
	}
	return backend.ErrCabinetSecretNotFound
}

func (c *fakeCabinet) RevokeSlot(_ context.Context, routerID int64, country string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lockedSeed(routerID)
	if !c.lockedActive(routerID, "amnezia") {
		return backend.ErrCabinetSecretNotFound
	}
	if !c.issued[country] {
		return fmt.Errorf("песочница: страна %q не выпущена", country)
	}
	delete(c.issued, country)
	return nil
}
