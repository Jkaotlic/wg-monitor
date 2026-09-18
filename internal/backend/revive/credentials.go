package revive

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Сохранённые учётные данные и авто-оживление (v0.45).
//
// Решение оператора 18.09: «пароль root хранится зашифрованным и
// используется для авто-оживления давно не обновлявшихся роутеров». Шифрует
// тот же Box (AES-256-GCM, AAD = user_id) тем же ключом revive.key_file, что
// и секреты намерений: без ключа -- без хранения, база без ключа не
// расшифровывается. Открытый текст живёт только в памяти этого пакета.

// RequestedBySystem -- requested_by намерения, поставленного авто-проходом, а
// не админом. Telegram-номера положительны, поэтому -1 ни с кем не совпадёт.
const RequestedBySystem int64 = -1

// AutoRetryAfter -- не чаще раза в сутки после того, как авто-оживление
// закончилось (не вышло, срок истёк, ожил). Иначе проход раз в полчаса
// стучался бы в панель снова и снова.
const AutoRetryAfter = 24 * time.Hour

// StoredCredentials -- то, что админ ввёл и что сохраняется для
// авто-оживления. Печать скрыта, как у запросов мини-аппа.
type StoredCredentials struct {
	RootPassword string
	AWGMLogin    string
	AWGMPassword string
	AWGMAPIKey   string
}

func (StoredCredentials) String() string       { return "revive.StoredCredentials{" + maskedValue + "}" }
func (c StoredCredentials) GoString() string   { return c.String() }
func (StoredCredentials) LogValue() slog.Value { return slog.StringValue(maskedValue) }

// SaveCredentials шифрует и сохраняет учётные данные роутера. Без пароля
// root не сохраняет ничего (saved=false): авто-оживление без него не
// запустится. Пароли хранятся как введены, логин и ключ -- без краёв, как в
// Schedule.
func (s *Service) SaveCredentials(routerID int64, c StoredCredentials) (bool, error) {
	if !s.Enabled() {
		return false, ErrDisabled
	}
	creds := NewSecrets(c.RootPassword, strings.TrimSpace(c.AWGMLogin), c.AWGMPassword, strings.TrimSpace(c.AWGMAPIKey))
	if !creds.Usable() {
		return false, nil
	}
	nonce, ct, err := s.box.Seal(routerID, creds)
	if err != nil {
		return false, err
	}
	if err := s.cfg.DB.RouterCredentials().Put(routerID, nonce, ct, s.now()); err != nil {
		return false, err
	}
	s.logger.Info("учётные данные роутера сохранены зашифрованными", "router_id", routerID)
	return true, nil
}

// AutoOutcome -- чем кончилась попытка авто-постановки. Коды -- для журнала
// и для строки парка, не для человека.
type AutoOutcome string

const (
	AutoScheduled        AutoOutcome = "scheduled"
	AutoNoPassword       AutoOutcome = "no_root_password"
	AutoNoPanelAddress   AutoOutcome = "no_panel_address"
	AutoActive           AutoOutcome = "active"
	AutoCooldown         AutoOutcome = "cooldown"
	AutoCancelledByAdmin AutoOutcome = "cancelled_by_admin"
	AutoAgentAlive       AutoOutcome = "agent_alive"
	AutoUnreadable       AutoOutcome = "unreadable"
	AutoRouterGone       AutoOutcome = "router_gone"
)

// AutoSchedule ставит оживление по сохранённым учётным данным: на текущую
// версию бэкенда (target пусто), на максимальный срок, requested_by =
// RequestedBySystem. Кого оживлять, решает вызывающий (признак «давно не
// обновлялся» живёт в пакете backend); здесь -- можно ли и не рано ли.
//
// Повтор безопасен: идущее или ждущее намерение (своё или админа) не
// трогается; закончившееся -- не раньше AutoRetryAfter; отменённое админом --
// только если пароль сохранён заново после отмены.
func (s *Service) AutoSchedule(ctx context.Context, routerID int64) (AutoOutcome, error) {
	if !s.Enabled() {
		return "", ErrDisabled
	}
	nonce, ct, savedAt, ok, err := s.cfg.DB.RouterCredentials().Get(routerID)
	if err != nil {
		return "", err
	}
	if !ok {
		return AutoNoPassword, nil
	}
	u, err := s.cfg.DB.Users().GetByID(routerID)
	if errors.Is(err, db.ErrUserNotFound) {
		return AutoRouterGone, nil
	}
	if err != nil {
		return "", err
	}
	if !s.panelURLSafe(strings.TrimSpace(derefString(u.AWGMURL))) {
		return AutoNoPanelAddress, nil
	}
	now := s.now()
	if s.agentFresh(u, now) && !s.needsReinstall(u) {
		return AutoAgentAlive, nil
	}

	s.work.Lock()
	cur, err := s.cfg.DB.Revive().Get(routerID)
	if err != nil {
		s.work.Unlock()
		return "", err
	}
	if blocked := autoBlockedBy(cur, savedAt, now); blocked != "" {
		s.work.Unlock()
		return blocked, nil
	}
	creds, err := s.box.Open(routerID, nonce, ct)
	if err != nil || !creds.Usable() {
		s.work.Unlock()
		s.logger.Warn("авто-оживление: сохранённый пароль не расшифровывается", "router_id", routerID)
		return AutoUnreadable, nil
	}
	_, err = s.putLocked(routerID, RequestedBySystem, creds, "", DefaultExpiryDays, now)
	s.work.Unlock()
	if errors.Is(err, ErrRunning) {
		return AutoActive, nil
	}
	if err != nil {
		return "", err
	}
	s.logger.Info("авто-оживление поставлено", "router_id", routerID, "nickname", u.Nickname)
	s.confirmSoon(routerID)
	return AutoScheduled, nil
}

// autoBlockedBy -- мешает ли уже записанное намерение авто-постановке.
func autoBlockedBy(cur *db.ReviveIntent, savedAt, now time.Time) AutoOutcome {
	if cur == nil {
		return ""
	}
	switch cur.Status {
	case StatusWaiting, StatusRunning:
		return AutoActive
	case StatusCancelled:
		// Админ отменил: не спорим, пока он не ввёл пароль заново.
		if !savedAt.After(cur.UpdatedAt) {
			return AutoCancelledByAdmin
		}
		return ""
	}
	if now.Sub(cur.UpdatedAt) < AutoRetryAfter {
		return AutoCooldown
	}
	return ""
}

func (s *Service) needsReinstall(u *db.User) bool {
	return s.cfg.NeedsReinstall != nil && s.cfg.NeedsReinstall(u)
}

// forgetRejectedCredentials -- панель отказала во входе: если сохранён ТОТ ЖЕ
// пароль root, что был у намерения, он заведомо неверен и стирается. Другой
// (админ ввёл новый в щель) не трогается. Зовётся до finish: finish стирает
// секрет намерения, сравнить потом было бы не с чем.
func (s *Service) forgetRejectedCredentials(routerID int64) {
	nonce, ct, ok, err := s.cfg.DB.Revive().Secret(routerID)
	if err != nil || !ok {
		return
	}
	used, err := s.box.Open(routerID, nonce, ct)
	if err != nil {
		return
	}
	sNonce, sCT, _, ok, err := s.cfg.DB.RouterCredentials().Get(routerID)
	if err != nil || !ok {
		return
	}
	stored, err := s.box.Open(routerID, sNonce, sCT)
	if err != nil {
		return
	}
	if subtle.ConstantTimeCompare([]byte(used.RootPassword()), []byte(stored.RootPassword())) != 1 {
		return
	}
	if gone, err := s.cfg.DB.RouterCredentials().DeleteIfMatches(routerID, sNonce); err != nil {
		s.logger.Warn("авто-оживление: неподошедший пароль не стёрт", "router_id", routerID, "err", err)
	} else if gone {
		s.logger.Info("сохранённый пароль root не подошёл и стёрт", "router_id", routerID)
	}
}
