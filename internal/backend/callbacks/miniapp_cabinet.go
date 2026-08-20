package callbacks

import (
	"context"
	"fmt"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/hidemy"
)

// Кабинеты провайдеров для мини-аппа. Реализация backend.VPNCabinet живёт
// здесь, а не в пакете backend, по той же причине, что и остальные
// notifier'ы: ключи кабинетов и клиенты к их API уже здесь, и вторая копия
// того и другого разошлась бы с первой.
//
// Содержимое конфига через клиента не ходит: наружу отдаётся только список
// того, что можно выпустить, а сам конфиг возвращается вызывающему
// хендлеру, который кладёт его прямо в команду агенту.

const (
	providerAmnezia = "amnezia"
	providerHideMy  = "hidemyname"
)

func (r *Router) Account(ctx context.Context, routerID int64, provider string) (backend.VPNAccount, error) {
	switch provider {
	case providerAmnezia:
		return r.amneziaAccountForMiniapp(ctx, routerID)
	case providerHideMy:
		return r.hideMyAccountForMiniapp(ctx, routerID)
	default:
		return backend.VPNAccount{}, fmt.Errorf("unknown provider %q", provider)
	}
}

func (r *Router) IssueConfig(ctx context.Context, routerID int64, provider, optionID string) (backend.VPNIssuedConfig, error) {
	switch provider {
	case providerAmnezia:
		key, err := r.getAmneziaKeyByID(routerID, "")
		if err != nil || key == "" {
			return backend.VPNIssuedConfig{}, fmt.Errorf("ключ Amnezia Premium не сохранён для этого роутера")
		}
		country := strings.ToLower(strings.TrimSpace(optionID))
		conf, err := r.downloadAmneziaConfig(ctx, key, country)
		if err != nil {
			return backend.VPNIssuedConfig{}, err
		}
		return backend.VPNIssuedConfig{TunnelName: "amnezia_" + country, Conf: conf, Backend: "nativewg"}, nil
	case providerHideMy:
		stored, ok := r.hideMyStoredCode(routerID, "")
		if !ok {
			return backend.VPNIssuedConfig{}, fmt.Errorf("код HideMy.name не сохранён для этого роутера")
		}
		server, err := r.hideMyServerByID(ctx, stored.AccessCode, optionID)
		if err != nil {
			return backend.VPNIssuedConfig{}, err
		}
		conf, err := r.downloadHideMyConfig(ctx, stored.AccessCode, server.IP)
		if err != nil {
			return backend.VPNIssuedConfig{}, err
		}
		return backend.VPNIssuedConfig{
			TunnelName: "hidemy_" + safeConfigSlug(server.ID),
			Conf:       conf,
			Backend:    "nativewg",
		}, nil
	default:
		return backend.VPNIssuedConfig{}, fmt.Errorf("unknown provider %q", provider)
	}
}

func (r *Router) amneziaAccountForMiniapp(ctx context.Context, routerID int64) (backend.VPNAccount, error) {
	acc := backend.VPNAccount{Provider: providerAmnezia, Label: "Amnezia Premium"}
	key, err := r.getAmneziaKeyByID(routerID, "")
	if err != nil || key == "" {
		acc.Note = "Ключ кабинета не сохранён. Отправьте его боту в теме роутера — приложение ключи не спрашивает."
		return acc, nil
	}
	info, err := r.fetchAmneziaAccount(ctx, key)
	if err != nil {
		acc.Note = "Кабинет не ответил: " + err.Error()
		return acc, nil
	}
	acc.Connected = true
	acc.Status = info.SubscriptionStatus
	acc.EndsAt = info.SubscriptionEndDate
	acc.DevicesUsed = info.ActiveDeviceCount
	acc.DevicesMax = info.MaxDeviceCount
	issued := make(map[string]bool, len(info.IssuedConfigs))
	for _, c := range info.IssuedConfigs {
		issued[strings.ToLower(c.CountryCode)] = true
	}
	for _, c := range info.AvailableCountries {
		acc.Options = append(acc.Options, backend.VPNOption{
			ID:     strings.ToLower(c.Code),
			Label:  c.Name,
			Issued: issued[strings.ToLower(c.Code)],
		})
	}
	if len(acc.Options) == 0 {
		acc.Note = "Кабинет не назвал ни одной доступной страны."
	}
	return acc, nil
}

func (r *Router) hideMyAccountForMiniapp(ctx context.Context, routerID int64) (backend.VPNAccount, error) {
	acc := backend.VPNAccount{Provider: providerHideMy, Label: "HideMy.name"}
	stored, ok := r.hideMyStoredCode(routerID, "")
	if !ok {
		acc.Note = "Код доступа не сохранён. Отправьте его боту в теме роутера — приложение коды не спрашивает."
		return acc, nil
	}
	servers, err := hidemy.New(r.cfg.HideMyBaseURL).ServerList(ctx, stored.AccessCode)
	if err != nil {
		acc.Note = "Кабинет не ответил: " + err.Error()
		return acc, nil
	}
	acc.Connected = true
	for _, s := range servers {
		acc.Options = append(acc.Options, backend.VPNOption{ID: s.ID, Label: s.Name})
	}
	if len(acc.Options) == 0 {
		acc.Note = "Кабинет не отдал ни одного сервера."
	}
	return acc, nil
}

// NotifyRouterTopic пишет текст в тему роутера. Нужен мастеру замены
// конфига: он сообщает исход при закрытом приложении -- это единственное,
// чего приложение не может, и ровно поэтому уведомления остаются у бота.
//
// Тема и чат берутся у самого роутера (EffectiveTelegramChatID): у каждого
// своя, и общий чат тут был бы рассылкой не по адресу.
func (r *Router) NotifyRouterTopic(ctx context.Context, routerID int64, text string) error {
	user, err := r.d.Users().GetByID(routerID)
	if err != nil || user == nil {
		return fmt.Errorf("notify router topic: роутер %d не найден: %w", routerID, err)
	}
	chatID := user.EffectiveTelegramChatID(r.cfg.ChatID)
	if chatID == 0 {
		return fmt.Errorf("notify router topic: у роутера %s нет чата", user.Nickname)
	}
	_, err = r.tg.SendMessage(ctx, chatID, user.TelegramThreadID, text, "", nil)
	return err
}
