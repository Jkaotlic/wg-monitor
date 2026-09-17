package callbacks

import (
	"context"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// TGClient -- то, чем бот пользуется у tg.Client.
type TGClient interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
	SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error)
	DeleteMessage(ctx context.Context, chatID, messageID int64) error
	AnswerCallbackQuery(ctx context.Context, callbackID, text string) error
	EditMessageText(ctx context.Context, chatID, messageID int64, text, parseMode string, markup *tg.InlineKeyboardMarkup) error
	GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]tg.Update, error)
}

// Config -- настройки бота и кабинетов. Группы больше нет (цикл 5): ни
// chat_id, ни списка разрешённых групп бот не знает.
type Config struct {
	AdminUserID        int64
	PublicBaseURL      string
	AmneziaBaseURL     string
	AmneziaSecretsPath string
	HideMyBaseURL      string
	HideMySecretsPath  string
}

// Router -- бот и кабинеты провайдеров. Бот (цикл 5) принимает только:
// /start в личке, кнопку «Тише на час» (silence), кнопку админа nmute,
// старые кнопки из уже отправленных сообщений (тост «Это теперь в
// приложении») и сторожей секретов и .conf в личке. Всё остальное -- молча.
type Router struct {
	d       *db.DB
	tg      TGClient
	cfg     Config
	silence *SilenceAction
}

func NewRouter(d *db.DB, tgClient TGClient, cfg Config) *Router {
	return &Router{d: d, tg: tgClient, cfg: cfg, silence: NewSilenceAction(d)}
}

// Run крутит getUpdates и хранит номер последнего обработанного обновления в
// KV. Ошибки -- с растущей паузой. Выход -- по отмене ctx.
func (r *Router) Run(ctx context.Context) error {
	var attempt int
	offset, _ := r.loadOffset()
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		updates, err := r.tg.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			attempt++
			wait := time.Duration(math.Min(math.Pow(2, float64(attempt)), 60)) * time.Second
			slog.Warn("getUpdates failed; backoff", "err", err, "wait", wait)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(wait):
			}
			continue
		}
		attempt = 0
		for _, u := range updates {
			r.handleUpdate(ctx, u)
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
		}
		if len(updates) > 0 {
			_ = r.saveOffset(offset)
		}
	}
}

func (r *Router) handleUpdate(ctx context.Context, u tg.Update) {
	switch {
	case u.CallbackQuery != nil:
		r.HandleCallback(ctx, u.CallbackQuery)
	case u.Message != nil:
		r.HandleMessage(ctx, u.Message)
	}
}

func (r *Router) loadOffset() (int64, error) {
	s, err := r.d.KV().Get("last_update_id")
	if err != nil || s == "" {
		return 0, err
	}
	return strconv.ParseInt(s, 10, 64)
}

func (r *Router) saveOffset(offset int64) error {
	return r.d.KV().Set("last_update_id", strconv.FormatInt(offset, 10))
}

// HandleCallback -- нажатие кнопки. Чат не проверяется: право решает человек
// (админ или роль на роутере), а не место, где висит сообщение.
func (r *Router) HandleCallback(ctx context.Context, q *tg.CallbackQuery) {
	// «🔕 Не писать мне про этот роутер» -- два поля, разбирается до Parse.
	if isAdminMuteCallback(q.Data) {
		r.handleAdminMuteCallback(ctx, q)
		return
	}
	// Кнопки, ушедшие в приложение, висят в старых сообщениях: ответ словами.
	if isMovedToAppCallback(q.Data) {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, movedToAppToast)
		return
	}
	args, err := Parse(q.Data)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "неизвестная кнопка")
		slog.Warn("malformed callback_data", "data", q.Data, "err", err)
		return
	}
	slog.Info("callback", "from", q.From.ID, "data", q.Data)
	if !r.silenceAllowed(ctx, q, args) {
		return
	}
	statusLine, err := r.silence.Apply(ctx, q, args)
	if err != nil {
		msg := "Ошибка: " + err.Error()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, msg)
		slog.Error("silence failed", "err", err)
		return
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
	newText := q.Message.Text + "\n\n" + statusLine
	empty := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, newText, "", &empty); err != nil {
		slog.Warn("editMessageText failed (state already updated)", "err", err)
	}
}

// silenceAllowed -- право заглушить тревогу роутера: админ, владелец или
// оператор (db.RouterAccessRole). Раньше роль смотрелась только при
// привязанном владельце, и оператор роутера без владельца не мог заглушить
// тревогу, пришедшую ему же в личку.
func (r *Router) silenceAllowed(ctx context.Context, q *tg.CallbackQuery, args Args) bool {
	u, err := r.d.Users().GetByID(args.UserID)
	if err != nil || u == nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "Ошибка: роутер не найден")
		slog.Warn("silence: роутер не найден", "router_user_id", args.UserID, "err", err)
		return false
	}
	if r.cfg.AdminUserID != 0 && q.From.ID == r.cfg.AdminUserID {
		return true
	}
	role, err := r.d.RouterAccessRole(u.ID, q.From.ID)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "Ошибка проверки доступа")
		slog.Warn("silence: роль не прочиталась", "router_user_id", u.ID, "from", q.From.ID, "err", err)
		return false
	}
	if role == "" {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "это не твой роутер")
		slog.Warn("silence: отказ без доступа", "router_user_id", u.ID, "from", q.From.ID)
		return false
	}
	return true
}

// HandleMessage -- сообщение боту. Отвечает бот только в личке: сторожа
// секретов и .conf, затем /start. В группах и на любой другой текст -- молча.
func (r *Router) HandleMessage(ctx context.Context, m *tg.Message) {
	if m == nil || !isPrivateMessage(m) {
		return
	}
	// Секрет кабинета удаляется до всего остального: «/start vpn://…» не
	// должен оставить ключ в переписке.
	if r.handleCabinetSecretMessage(ctx, m) {
		return
	}
	if r.handleConfDocument(ctx, m) {
		return
	}
	if cmd, _, ok := parseSlashCommand(m.Text); ok && cmd == "/start" {
		r.handleStart(ctx, m)
	}
}

// isPrivateMessage -- личка с человеком: чат и отправитель совпадают.
func isPrivateMessage(m *tg.Message) bool {
	return m.From.ID != 0 && m.Chat.ID == m.From.ID
}
