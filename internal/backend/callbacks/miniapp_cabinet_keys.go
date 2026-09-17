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
// бота: вход в кабинет Amnezia с чтением аккаунта, список серверов HideMy.

var _ backend.VPNCabinetKeys = (*Router)(nil)

const (
	amneziaRejectedText = "Кабинет Amnezia Premium не принял ключ или не ответил — проверьте ключ и повторите"
	hideMyRejectedText  = "Кабинет HideMy.name не принял код или не ответил — проверьте код и повторите"
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
		if !strings.HasPrefix(secret, "vpn://") {
			return backend.CabinetSecret{}, backend.ErrCabinetSecretInvalid
		}
		if _, err := r.fetchAmneziaAccount(ctx, secret); err != nil {
			slog.Warn("кабинет Amnezia не принял ключ", "router_id", routerID, "err", redactSecret(err.Error(), secret))
			return backend.CabinetSecret{}, &backend.CabinetRejectedError{Reason: amneziaRejectedText}
		}
		stored, err := r.addAmneziaKeyLabeled(routerID, secret, label)
		if err != nil {
			return backend.CabinetSecret{}, err
		}
		return backend.CabinetSecret{ID: stored.ID, Label: stored.Label, Mask: cabinetSecretMask(stored.VPNKey), Active: true}, nil
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

// RevokeSlot освобождает слот подписки: отзывает выпущенную страну активным
// ключом. Premium-ключ при этом не перевыпускается.
func (r *Router) RevokeSlot(ctx context.Context, routerID int64, country string) error {
	key, err := r.getAmneziaKeyByID(routerID, "")
	if err != nil {
		return err
	}
	if key == "" {
		return backend.ErrCabinetSecretNotFound
	}
	if err := r.revokeAmneziaCountryConfig(ctx, key, country); err != nil {
		return fmt.Errorf("amnezia revoke: %s", redactSecret(err.Error(), key))
	}
	return nil
}
