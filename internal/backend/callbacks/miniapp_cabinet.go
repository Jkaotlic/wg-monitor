package callbacks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/hidemy"
	"github.com/Jkaotlic/wg-monitor/internal/backend/notify"
	"github.com/Jkaotlic/wg-monitor/internal/backend/sealedfile"
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
		// Кабинет отключён (v0.61.3): ни ключа, ни кабинета не спрашиваем.
		return backend.VPNIssuedConfig{}, backend.ErrAmneziaDisabled
	case providerHideMy:
		if _, err := r.listHideMyCodes(routerID); isSealedStoreError(err) {
			return backend.VPNIssuedConfig{}, err
		}
		stored, ok := r.hideMyStoredCode(routerID, "")
		if !ok {
			return backend.VPNIssuedConfig{}, fmt.Errorf("код HideMy.name не сохранён для этого роутера")
		}
		server, err := r.hideMyServerByID(ctx, stored.AccessCode, optionID)
		if err != nil {
			return backend.VPNIssuedConfig{}, redactCabinetError(err, stored.AccessCode)
		}
		conf, err := r.downloadHideMyConfig(ctx, stored.AccessCode, server.IP)
		if err != nil {
			return backend.VPNIssuedConfig{}, redactCabinetError(err, stored.AccessCode)
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

// amneziaAccountForMiniapp -- экран подписки Amnezia Premium. Кабинет
// отключён (v0.61.3): подписка не читается, стран нет, на экране -- почему и
// где брать конфиги. Сохранённые ключи по-прежнему видны и удаляются (это
// файл на сервере, не кабинет).
func (r *Router) amneziaAccountForMiniapp(_ context.Context, _ int64) (backend.VPNAccount, error) {
	return backend.VPNAccount{Provider: providerAmnezia, Label: "Amnezia Premium", Note: backend.AmneziaDisabledText}, nil
}

func (r *Router) hideMyAccountForMiniapp(ctx context.Context, routerID int64) (backend.VPNAccount, error) {
	acc := backend.VPNAccount{Provider: providerHideMy, Label: "HideMy.name"}
	if _, err := r.listHideMyCodes(routerID); sealedStoreNote(err) != "" {
		acc.Note, acc.KeyProblem = sealedStoreNote(err), sealedKeyProblem(err)
		return acc, nil
	}
	stored, ok := r.hideMyStoredCode(routerID, "")
	if !ok {
		acc.Note = "Код доступа не сохранён — добавьте его кнопкой «Добавить код»."
		return acc, nil
	}
	servers, err := hidemy.New(r.cfg.HideMyBaseURL).ServerList(ctx, stored.AccessCode)
	if err != nil {
		acc.Note = "Кабинет не ответил: " + redactSecret(err.Error(), stored.AccessCode)
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

// NotifyRouterTopic пишет текст всем, кому адресованы уведомления о роутере.
// Нужен мастеру замены конфига: он сообщает исход при закрытом приложении --
// это единственное, чего приложение не может, и ровно поэтому уведомления
// остаются у бота.
//
// Имя осталось прежним ради вызывающих; адресат сменился с темы группы на
// личку каждого получателя.
func (r *Router) NotifyRouterTopic(ctx context.Context, routerID int64, text string) error {
	if _, err := notify.NewFanout(r.d, r.tg, slog.Default(), r.cfg.AdminUserID).Send(ctx, routerID, text, ""); err != nil {
		return fmt.Errorf("notify router: %w", err)
	}
	return nil
}

// isSealedStoreError -- хранилище кабинета зашифровано, а ключа нет или он
// не тот (v0.55, B1). Такую ошибку нельзя выдавать за «ключ не сохранён»:
// человек завёл бы ключ заново, а прежние лежат целыми в файле.
func isSealedStoreError(err error) bool {
	return errors.Is(err, sealedfile.ErrKeyMissing) || errors.Is(err, sealedfile.ErrUnreadable)
}

// sealedKeyProblem -- код сбоя ключа для API (те же, что у экрана кабинетов).
func sealedKeyProblem(err error) string {
	switch {
	case errors.Is(err, sealedfile.ErrKeyMissing):
		return "cabinet_key_missing"
	case errors.Is(err, sealedfile.ErrUnreadable):
		return "cabinet_key_wrong"
	}
	return ""
}

// sealedStoreNote -- строка экрана кабинета для такой ошибки; "" -- причина другая.
func sealedStoreNote(err error) string {
	switch {
	case errors.Is(err, sealedfile.ErrKeyMissing):
		return "Ключи кабинетов на сервере не прочитать: ключ шифрования не найден — напишите администратору."
	case errors.Is(err, sealedfile.ErrUnreadable):
		return "Ключи кабинетов на сервере не расшифровываются: ключ шифрования не тот — напишите администратору."
	}
	return ""
}
