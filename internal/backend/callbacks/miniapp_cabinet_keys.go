package callbacks

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/hidemy"
)

// Ключи и коды кабинетов для мини-аппа (backend.VPNCabinetKeys). Раньше их
// принимал бот текстом в теме роутера; теперь -- только полем приложения по
// HTTPS (цикл 3, решение 2). Проверка перед сохранением -- та же, что была у
// бота: список серверов HideMy. Ключ Amnezia не принимается вовсе: кабинет
// Amnezia отключён (v0.61.3).

var _ backend.VPNCabinetKeys = (*Router)(nil)

const (
	hideMyRejectedText = "Кабинет HideMy.name не принял код или не ответил — проверьте код и повторите"
)

func (r *Router) Secrets(routerID int64, provider string) ([]backend.CabinetSecret, error) {
	switch provider {
	case providerAmnezia:
		keys, err := r.listAmneziaKeys(routerID)
		if err != nil {
			return nil, err
		}
		out := make([]backend.CabinetSecret, 0, len(keys.Keys))
		for _, k := range keys.Keys {
			out = append(out, backend.CabinetSecret{ID: k.ID, Label: k.Label, Mask: cabinetSecretMask(k.VPNKey), Active: k.ID == keys.ActiveID})
		}
		return out, nil
	case providerHideMy:
		codes, err := r.listHideMyCodes(routerID)
		if err != nil {
			return nil, err
		}
		out := make([]backend.CabinetSecret, 0, len(codes.Codes))
		for _, c := range codes.Codes {
			out = append(out, backend.CabinetSecret{ID: c.ID, Label: c.Label, Mask: cabinetSecretMask(c.AccessCode), Active: c.ID == codes.ActiveID})
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown provider %q", provider)
}

func (r *Router) AddSecret(ctx context.Context, routerID int64, provider, secret, label string) (backend.CabinetSecret, error) {
	secret = strings.TrimSpace(secret)
	switch provider {
	case providerAmnezia:
		// Кабинет отключён (v0.61.3): ключ не проверяется в кабинете и не
		// сохраняется -- выпускать им всё равно нечего.
		return backend.CabinetSecret{}, backend.ErrAmneziaDisabled
	case providerHideMy:
		if !hidemy.ValidAccessCode(secret) {
			return backend.CabinetSecret{}, backend.ErrCabinetSecretInvalid
		}
		if _, err := hidemy.New(r.cfg.HideMyBaseURL).ServerList(ctx, secret); err != nil {
			slog.Warn("кабинет HideMy.name не принял код", "router_id", routerID, "err", redactSecret(err.Error(), secret))
			return backend.CabinetSecret{}, &backend.CabinetRejectedError{Reason: hideMyRejectedText}
		}
		stored, err := r.addHideMyCodeLabeled(routerID, secret, label)
		if err != nil {
			return backend.CabinetSecret{}, err
		}
		return backend.CabinetSecret{ID: stored.ID, Label: stored.Label, Mask: cabinetSecretMask(stored.AccessCode), Active: true}, nil
	}
	return backend.CabinetSecret{}, fmt.Errorf("unknown provider %q", provider)
}

func (r *Router) SetActiveSecret(routerID int64, provider, id string) error {
	switch provider {
	case providerAmnezia:
		return r.setActiveAmneziaKey(routerID, id)
	case providerHideMy:
		return r.setActiveHideMyCode(routerID, id)
	}
	return fmt.Errorf("unknown provider %q", provider)
}

func (r *Router) DeleteSecret(routerID int64, provider, id string) error {
	switch provider {
	case providerAmnezia:
		return r.deleteAmneziaKey(routerID, id)
	case providerHideMy:
		return r.deleteHideMyCode(routerID, id)
	}
	return fmt.Errorf("unknown provider %q", provider)
}

// RevokeSlot -- отзыв страны в кабинете Amnezia Premium. Кабинет отключён
// (v0.61.3): отказ до чтения ключа и без обращения к кабинету.
func (r *Router) RevokeSlot(_ context.Context, _ int64, _ string) error {
	return backend.ErrAmneziaDisabled
}
