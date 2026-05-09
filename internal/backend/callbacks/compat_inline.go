package callbacks

import (
	"context"
	"log/slog"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// handleCompatBtn turns a compat-inline-keyboard tap into the same dispatch
// path as the equivalent reply-keyboard text message. Args.CheckName carries
// the short code (smart_reply, tunnels, …) which we expand back into the
// original button label, then we synthesise a tg.Message and run it through
// HandleMessage. This keeps every command's logic in one place.
//
// Side notes:
//   - We answer the callback query with an empty toast so the spinner on
//     the button stops (TG keeps the spinner spinning otherwise).
//   - The synthesised Message has MessageID=0 and From copied from the
//     callback's From — handleNonCommandMessage's "delete user command
//     message" branch must skip when MessageID==0 (no real message exists).
func (r *Router) handleCompatBtn(ctx context.Context, q *tg.CallbackQuery, args Args) {
	text := tg.CompatBtnTextByCode(args.CheckName)
	if text == "" {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "неизвестная кнопка")
		slog.Warn("compat_btn: unknown code", "code", args.CheckName)
		return
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
	synthetic := &tg.Message{
		MessageID:       0, // sentinel: skip user-message deletion
		Chat:            q.Message.Chat,
		MessageThreadID: q.Message.MessageThreadID,
		From:            q.From,
		Text:            text,
	}
	r.HandleMessage(ctx, synthetic)
}
