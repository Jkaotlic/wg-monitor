package callbacks

import (
	"context"

	"github.com/anex/wg-monitor/internal/backend/alerts"
	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

// Notifier implements backend.TGNotifier by sending one or more chunks via
// the existing tg.Client. The first chunk replies to the original alert
// (ref.MessageID); subsequent chunks chain to the previous chunk so a paginated
// diag stays threaded together rather than scattered across the topic.
type Notifier struct {
	TG TGClient
}

func NewNotifier(c TGClient) *Notifier { return &Notifier{TG: c} }

func (n *Notifier) NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, action string, result wire.CommandResult, maxChars int) error {
	chunks := alerts.FormatCommandResult(action, result, maxChars)
	if len(chunks) == 0 {
		return nil
	}
	prev := ref.MessageID
	for _, c := range chunks {
		replyTo := prev
		mid, err := n.TG.SendMessageWithReplyKeyboard(ctx, ref.ChatID, ref.ThreadID, c, "", &replyTo, tg.ReplyKeyboardForTopic("per_router"))
		if err != nil {
			return err
		}
		prev = mid
	}
	return nil
}
