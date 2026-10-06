package backend

import (
	"context"
	"errors"

	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Контракт кабинетов VPN и своих серверов для мини-аппа (цикл 3 программы
// «бот без слеш-команд»). Реализации живут там, где лежат данные: ключи и
// клиенты кабинетов -- callbacks.Router, свои серверы --
// selfhostedamnezia.Service, документ в личку -- *tg.Client. Здесь только
// то, что нужно обработчикам.

var (
	// ErrCabinetSecretInvalid -- ключ или код не того вида (не vpn://, не цифры).
	ErrCabinetSecretInvalid = errors.New("ключ или код неверного вида")
	// ErrCabinetSecretNotFound -- у роутера нет ключа/кода с таким id, или нет
	// ни одного, когда нужен активный.
	ErrCabinetSecretNotFound = errors.New("ключ или код не найден")
	// ErrVPNSlotBusy -- выпуск новой страны Amnezia при занятых слотах
	// подписки. Текст читает человек в шаге мастера замены (replace.go:251),
	// поэтому он по-русски и говорит, что делать.
	ErrVPNSlotBusy = errors.New("все слоты подписки Amnezia Premium заняты — отзовите страну, которая больше не нужна")
)

// CabinetRejectedError -- кабинет не принял ключ или код при добавлении.
// Reason -- готовый русский текст без секрета и без ответа кабинета.
type CabinetRejectedError struct{ Reason string }

func (e *CabinetRejectedError) Error() string { return e.Reason }

// CabinetSecret -- то, что видно про сохранённый ключ или код. Самого
// секрета здесь нет и быть не может: только маска из четырёх последних знаков.
type CabinetSecret struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Mask   string `json:"mask"`
	Active bool   `json:"active"`
}

// VPNCabinetKeys -- ключи Amnezia Premium и коды HideMy.name роутера.
// provider -- "amnezia" или "hidemyname".
type VPNCabinetKeys interface {
	Secrets(routerID int64, provider string) ([]CabinetSecret, error)
	// AddSecret проверяет секрет в кабинете и только потом сохраняет.
	// Ошибки: ErrCabinetSecretInvalid, *CabinetRejectedError; прочее --
	// сбой хранилища.
	AddSecret(ctx context.Context, routerID int64, provider, secret, label string) (CabinetSecret, error)
	SetActiveSecret(routerID int64, provider, id string) error
	DeleteSecret(routerID int64, provider, id string) error
	// RevokeSlot отзывает выпущенную страну активным ключом Amnezia.
	// ErrCabinetSecretNotFound -- ключа нет.
	RevokeSlot(ctx context.Context, routerID int64, country string) error
}

// SelfHostedVPS -- свои VPN-серверы (реализует *selfhostedamnezia.Service).
type SelfHostedVPS interface {
	List() ([]selfhostedamnezia.Instance, error)
	// Defaults -- чем заполняются пустые поля формы (без адресов и пароля).
	Defaults() selfhostedamnezia.Config
	Create(inst selfhostedamnezia.Instance) error
	Update(id string, inst selfhostedamnezia.Instance) error
	SetEnabled(id string, enabled bool) error
	Delete(id string) error
	Check(ctx context.Context, id string) (selfhostedamnezia.CheckResult, error)
	Issue(ctx context.Context, id, clientName string) (selfhostedamnezia.IssuedConfig, selfhostedamnezia.Instance, error)
	// ConfirmHostKey -- «Подтвердить ключ сервера SHA256:…» (v0.56, C1):
	// ожидающий отпечаток, совпавший с присланным, становится доверенным.
	// Не совпал или ожидающего нет -- ErrHostKeyNotPending.
	ConfirmHostKey(id, fingerprint string) error
	// Clients -- выданные подключения сервера (v0.55, B3).
	Clients(ctx context.Context, id string) ([]selfhostedamnezia.Client, selfhostedamnezia.Instance, error)
	// Revoke -- отзыв подключения по ключу. ErrClientNotFound -- такого нет.
	Revoke(ctx context.Context, id, publicKey string) (selfhostedamnezia.Client, selfhostedamnezia.Instance, error)
}

// MiniappDocSender -- .conf документом и QR картинкой в личку нажавшему (*tg.Client).
type MiniappDocSender interface {
	SendDocument(ctx context.Context, chatID int64, threadID *int64, filename string, data []byte, caption string) (int64, error)
	SendPhoto(ctx context.Context, chatID int64, threadID *int64, filename string, data []byte, caption string) (int64, error)
}

var _ MiniappDocSender = (*tg.Client)(nil)
