package callbacks

import (
	"context"
	"encoding/json"
	"fmt"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// RoutesEditTG is the subset of tg.Client RoutesPanelNotifier uses.
type RoutesEditTG interface {
	EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error
}

// RoutesPanelNotifier handles route_status / route_rebind CommandResults
// by editing the originating panel message in place. It also keeps the
// in-memory snapshot cache fresh.
type RoutesPanelNotifier struct {
	TG    RoutesEditTG
	Cache *RoutesCache
	DB    *db.DB
}

func (n *RoutesPanelNotifier) NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, userID int64) error {
	user, err := n.DB.Users().GetByID(userID)
	if err != nil || user == nil {
		return fmt.Errorf("user lookup: %w", err)
	}
	switch ref.Action {
	case "route_status":
		return n.renderStatus(ctx, ref, res, user)
	case "route_rebind":
		return n.renderRebind(ctx, ref, res, user)
	default:
		return fmt.Errorf("RoutesPanelNotifier: unsupported action %q", ref.Action)
	}
}

func (n *RoutesPanelNotifier) renderStatus(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	if res.Status != "ok" {
		text := fmt.Sprintf("🛣 Маршруты — %s\n⚠ awg-manager не отвечает\n%s", user.Nickname, res.Output)
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: "🔁 Обновить", CallbackData: fmt.Sprintf("routes_refresh:%d:_panel_", user.ID)}},
		}}
		return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
		return fmt.Errorf("decode snapshot: %w", err)
	}
	n.Cache.Put(user.ID, snap)
	text := tg.RoutesPanelText(user.Nickname, snap)
	kb := tg.RoutesPanelKeyboard(user.ID, snap)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

func (n *RoutesPanelNotifier) renderRebind(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	var rb wire.RouteRebindResult
	if err := json.Unmarshal([]byte(res.Output), &rb); err != nil {
		return fmt.Errorf("decode rebind result: %w", err)
	}
	// Resolve human names BEFORE invalidation (cache holds the pre-rebind snapshot).
	srcName, dstName := rb.SrcTunnelID, rb.DstTunnelID
	if snap, ok := n.Cache.Get(user.ID); ok {
		for _, t := range snap.Tunnels {
			if t.ID == rb.SrcTunnelID {
				srcName = t.Name
			}
			if t.ID == rb.DstTunnelID {
				dstName = t.Name
			}
		}
	}
	n.Cache.Invalidate(user.ID)
	totalFailed := rb.DNS.Failed + rb.Static.Failed
	text := tg.RebindResultText(srcName, dstName, rb)
	kb := tg.RebindResultKeyboard(user.ID, rb.SrcTunnelID, rb.DstTunnelID, totalFailed)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}
