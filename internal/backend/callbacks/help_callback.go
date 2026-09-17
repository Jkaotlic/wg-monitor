package callbacks

import (
	"context"
	"log/slog"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Справка «ℹ Помощь» под панелями роутера: panel:0:help:<screen>.
//
// Жила внутри хаба /panel, а хаб уехал в приложение (цикл 2). Сама справка
// осталась: её кнопки стоят на панелях PingCheck и диагностики
// (tg/pingcheck_panel.go:144, tg/diag_keyboard.go:26); кнопка premium висит
// только в старых сообщениях -- её экран отсылает в приложение. Справка
// туннелей и маршрутов ушла с панелями (цикл 4): её старые кнопки отвечают
// тостом (moved_to_app.go).
//
// Открыть может любой: текст статический, данных роутера в нём нет. Поэтому
// гейта нет -- ни админского, ни по роутеру.
const helpCallbackPrefix = "panel:0:help:"

func isHelpCallback(data string) bool {
	return strings.HasPrefix(data, helpCallbackPrefix)
}

func (r *Router) handleHelpCallback(ctx context.Context, q *tg.CallbackQuery, screen string) {
	body := tg.HelpForScreen(screen)
	kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
		{{Text: "✖ Закрыть", CallbackData: "close_panel:0:_panel_"}},
	}}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, body, "", &kb); err != nil {
		slog.Warn("справка: правка сообщения не удалась", "screen", screen, "err", err)
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
}
