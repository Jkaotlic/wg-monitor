package callbacks

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/tg"
)

// pendingAddOperator is one in-progress FSM: the admin tapped "Добавить
// оператора" and we're waiting for them to either forward a message from
// the new operator or type a numeric TG ID. Single FSM per admin at a
// time (keyed by admin user id); a fresh put replaces any prior entry.
type pendingAddOperator struct {
	AdminUserID int64
	RouterID    int64
	ExpiresAt   time.Time
}

// pendingAddOperatorStore is a goroutine-safe map keyed by admin's TG
// user id. Lifetime mirrors pendingMaintStore — short TTL (5 min), in-
// memory only, evicted on expired-get.
type pendingAddOperatorStore struct {
	mu sync.Mutex
	m  map[int64]*pendingAddOperator
}

func newPendingAddOperatorStore() *pendingAddOperatorStore {
	return &pendingAddOperatorStore{m: make(map[int64]*pendingAddOperator)}
}

// put stores an FSM for `adminID`, replacing any prior pending entry for
// the same admin. ttl is added to time.Now() as the expiry.
func (s *pendingAddOperatorStore) put(adminID, routerID int64, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[adminID] = &pendingAddOperator{
		AdminUserID: adminID,
		RouterID:    routerID,
		ExpiresAt:   time.Now().Add(ttl),
	}
}

// get returns the unexpired FSM for `adminID` or (nil, false). Expired
// entries are evicted as a side effect.
func (s *pendingAddOperatorStore) get(adminID int64) (*pendingAddOperator, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[adminID]
	if !ok {
		return nil, false
	}
	if time.Now().After(p.ExpiresAt) {
		delete(s.m, adminID)
		return nil, false
	}
	return p, true
}

// clear removes the FSM for `adminID` (no-op if absent).
func (s *pendingAddOperatorStore) clear(adminID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, adminID)
}

// accessHomeMessage renders the "👥 Доступ" screen: list of all routers with
// owner + operator-count summary. One button per router plus a "back" row.
func accessHomeMessage(d *db.DB) (string, tg.InlineKeyboardMarkup) {
	users, err := d.Users().GetAll()
	if err != nil {
		return "👥 Управление доступом\n\nНе удалось прочитать роутеров: " + err.Error(),
			tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
				{{Text: "« Назад", CallbackData: "panel:0:home"}},
			}}
	}
	var b strings.Builder
	b.WriteString("👥 Управление доступом\n\nВыбери роутер:")
	rows := make([][]tg.InlineKeyboardButton, 0, len(users)+1)
	if len(users) == 0 {
		b.WriteString("\n\nРоутеров нет.")
	}
	for _, u := range users {
		ops, _ := d.RouterOperators().List(u.ID)
		ownerLabel := "?"
		if u.TelegramUserID != nil {
			ownerLabel = fmt.Sprintf("%d", *u.TelegramUserID)
		}
		btnLabel := fmt.Sprintf("%s — owner: %s | %s",
			u.Nickname, ownerLabel, pluralOperators(len(ops)))
		rows = append(rows, []tg.InlineKeyboardButton{
			{Text: btnLabel, CallbackData: fmt.Sprintf("access:0:router:%d", u.ID)},
		})
		b.WriteString("\n  • ")
		b.WriteString(btnLabel)
	}
	rows = append(rows, []tg.InlineKeyboardButton{
		{Text: "« Назад", CallbackData: "panel:0:home"},
	})
	return b.String(), tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// accessRouterMessage renders the per-router access screen: owner + each
// operator with its own ✖ button, plus add / back rows.
func accessRouterMessage(d *db.DB, routerID int64) (string, tg.InlineKeyboardMarkup, error) {
	u, err := d.Users().GetByID(routerID)
	if err != nil {
		return "", tg.InlineKeyboardMarkup{}, err
	}
	ops, err := d.RouterOperators().List(routerID)
	if err != nil {
		return "", tg.InlineKeyboardMarkup{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "👥 %s\n\n", u.Nickname)
	rows := make([][]tg.InlineKeyboardButton, 0, len(ops)+3)
	if u.TelegramUserID == nil {
		b.WriteString("Owner: (не привязан, TOFU)\n")
	} else {
		fmt.Fprintf(&b, "Owner: %d\n", *u.TelegramUserID)
		rows = append(rows, []tg.InlineKeyboardButton{
			{Text: "✖ Отвязать owner'a", CallbackData: fmt.Sprintf("access:0:unbind_owner:%d", routerID)},
		})
	}
	b.WriteString("\nОператоры:")
	if len(ops) == 0 {
		b.WriteString(" (нет)")
	}
	for _, op := range ops {
		fmt.Fprintf(&b, "\n  • %d", op.TelegramUserID)
		rows = append(rows, []tg.InlineKeyboardButton{
			{Text: fmt.Sprintf("✖ %d", op.TelegramUserID),
				CallbackData: fmt.Sprintf("access:0:remove_op:%d:%d", routerID, op.TelegramUserID)},
		})
	}
	rows = append(rows, []tg.InlineKeyboardButton{
		{Text: "➕ Добавить оператора", CallbackData: fmt.Sprintf("access:0:add:%d", routerID)},
	})
	rows = append(rows, []tg.InlineKeyboardButton{
		{Text: "« К списку роутеров", CallbackData: "access:0:home"},
	})
	return b.String(), tg.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

// handleAccessCallback is the dispatcher for access:* callbacks. Admin gate
// is enforced in HandleCallback; this method assumes the caller is admin.
func (r *Router) handleAccessCallback(ctx context.Context, q *tg.CallbackQuery, args Args) {
	slog.Info("access callback", "screen", args.AccessScreen, "router_id", args.AccessRouterID, "op_tg_id", args.AccessOperatorTGID, "from", q.From.ID)
	switch args.AccessScreen {
	case "home":
		r.accessShowHome(ctx, q)
	case "router":
		r.accessShowRouter(ctx, q, args.AccessRouterID)
	case "add":
		r.accessStartAdd(ctx, q, args.AccessRouterID)
	case "remove_op":
		r.accessRemoveOp(ctx, q, args.AccessRouterID, args.AccessOperatorTGID)
	case "unbind_owner":
		r.accessUnbindOwner(ctx, q, args.AccessRouterID)
	case "back":
		r.accessBack(ctx, q)
	case "cancel_add":
		r.accessCancelAdd(ctx, q)
	default:
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "unknown screen")
	}
}

func (r *Router) accessShowHome(ctx context.Context, q *tg.CallbackQuery) {
	text, kb := accessHomeMessage(r.d)
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("access home edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) accessShowRouter(ctx context.Context, q *tg.CallbackQuery, routerID int64) {
	text, kb, err := accessRouterMessage(r.d, routerID)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("access router edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) accessStartAdd(ctx context.Context, q *tg.CallbackQuery, routerID int64) {
	u, err := r.d.Users().GetByID(routerID)
	if err != nil || u == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "роутер не найден")
		return
	}
	r.pendingAddOperator.put(q.From.ID, routerID, 5*time.Minute)
	hint := fmt.Sprintf("🆔 Добавление оператора для %s\n\nПерешли мне (в личку с ботом) любое сообщение от нужного человека ИЛИ напиши его числовой Telegram ID. Жду 5 минут.", u.Nickname)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		{{Text: "✖ Отмена", CallbackData: "access:0:cancel_add"}},
	}}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, hint, "", &kb); err != nil {
		slog.Warn("access add edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "жду forward или ID в личке")
}

func (r *Router) accessRemoveOp(ctx context.Context, q *tg.CallbackQuery, routerID, opTGID int64) {
	if err := r.d.RouterOperators().Remove(routerID, opTGID); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось удалить")
		slog.Warn("access remove op failed", "err", err, "router_id", routerID, "op_tg", opTGID)
		return
	}
	r.accessShowRouter(ctx, q, routerID)
}

func (r *Router) accessUnbindOwner(ctx context.Context, q *tg.CallbackQuery, routerID int64) {
	if err := r.d.Users().SetTelegramUserID(routerID, 0); err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "не удалось отвязать owner'a")
		slog.Warn("access unbind owner failed", "err", err, "router_id", routerID)
		return
	}
	r.accessShowRouter(ctx, q, routerID)
}

func (r *Router) accessBack(ctx context.Context, q *tg.CallbackQuery) {
	text, kb := panelHomeMessage()
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, "", &kb); err != nil {
		slog.Warn("access back edit failed", "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}

func (r *Router) accessCancelAdd(ctx context.Context, q *tg.CallbackQuery) {
	r.pendingAddOperator.clear(q.From.ID)
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "отменено")
	r.accessShowHome(ctx, q)
}

// pluralOperators returns a Russian-correct count label: "0 операторов",
// "1 оператор", "2 оператора", ..., "11 операторов", "21 оператор".
func pluralOperators(n int) string {
	mod10 := n % 10
	mod100 := n % 100
	switch {
	case mod100 >= 11 && mod100 <= 14:
		return fmt.Sprintf("%d операторов", n)
	case mod10 == 1:
		return fmt.Sprintf("%d оператор", n)
	case mod10 >= 2 && mod10 <= 4:
		return fmt.Sprintf("%d оператора", n)
	default:
		return fmt.Sprintf("%d операторов", n)
	}
}
