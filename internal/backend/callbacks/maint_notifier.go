package callbacks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/internal/backend/upstream"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// MaintEditTG is the subset of tg.Client MaintPanelNotifier uses.
type MaintEditTG interface {
	EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error
}

// MaintPanelNotifier handles version_audit / firmware_status / service_restart /
// firmware_install CommandResults by editing the originating panel message
// in place. It also keeps the audit cache (firmware + version snapshots) fresh
// so the smart-reply Updates section and the FwOpen handler can read recent
// state without a fresh round-trip to the agent.
type MaintPanelNotifier struct {
	TG       MaintEditTG
	Up       *upstream.Cache   // optional — nil-safe; missing → no Updates section
	Cooldown *cooldownStore    // for cooldown-aware re-render after restart/install
	Audit    *simpleAuditCache // updated with each version_audit / firmware_status
	DB       *db.DB
	Sink     CommandEnqueuer
	// MiniAppBaseURL -- публичный адрес бэкенда для кнопки «Панель роутера».
	MiniAppBaseURL string
}

// NotifyCommandResult dispatches by ref.Action. Returns nil for unsupported
// actions (handler will fall through to TGNotifier in that case — the
// backend's dispatcher only calls us for known maint actions, so unknowns
// here would indicate a wiring bug).
func (n *MaintPanelNotifier) NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, userID int64) error {
	user, err := n.DB.Users().GetByID(userID)
	if err != nil || user == nil {
		return fmt.Errorf("user lookup: %w", err)
	}
	switch ref.Action {
	case "version_audit":
		return n.renderStatus(ctx, ref, res, user)
	case "firmware_status":
		return n.renderFirmware(ctx, ref, res, user)
	case "service_restart", "firmware_install":
		return n.renderActionBanner(ctx, ref, res, user)
	default:
		return fmt.Errorf("MaintPanelNotifier: unsupported action %q", ref.Action)
	}
}

// renderStatus updates the audit cache with the new VersionAudit and
// re-renders the Status screen. Updates section is computed from
// upstream.Cache (if wired) + the just-decoded VersionAudit.
func (n *MaintPanelNotifier) renderStatus(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	if res.Status != "ok" {
		text := alerts.Card{
			Badge:   "❌",
			Label:   "🛠 Обслуживание",
			Summary: "агент не ответил",
			Meta:    []string{alerts.KV("роутер", user.Nickname), alerts.KV("команда", "version_audit")},
			Details: res.Output,
			Hint:    "Нажми «Повторить» ниже. Если повторно падает — открой проверку роутера.",
		}.Render(alerts.CardOpts{MaxBytes: 3900})
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: "🔄 Повторить", CallbackData: fmt.Sprintf("maint_open:%d:_panel_", user.ID)}},
		}}
		return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
	}
	var va wire.VersionAudit
	if err := json.Unmarshal([]byte(res.Output), &va); err != nil {
		return fmt.Errorf("decode version_audit: %w", err)
	}
	n.Audit.PutVersionAudit(user.ID, va)
	// Второй источник снимка. Кэш строкой выше живёт до ближайшего рестарта,
	// а сравнивать «было/стало» надо и после него; вдобавок version_audit --
	// единственный, кто знает версию HydraRoute Neo и доступную прошивку.
	// Ошибку только логируем: человек ждёт экран, а не отказ из-за базы.
	if n.DB != nil {
		if err := n.DB.RouterVersions().Upsert(user.ID, backend.VersionSnapshotFromAudit(va)); err != nil {
			slog.Warn("router versions upsert from version_audit", "user_id", user.ID, "err", err)
		}
	}
	args := buildMaintPanelArgs(ctx, user, va, n.Up, n.Cooldown, n.MiniAppBaseURL, ref.ChatID)
	text := tg.MaintPanelText(args)
	kb := tg.MaintPanelKeyboard(user.ID, args)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

// renderFirmware updates the firmware-status cache and re-renders the
// firmware screen.
func (n *MaintPanelNotifier) renderFirmware(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	if res.Status != "ok" {
		text := alerts.Card{
			Badge:   "❌",
			Label:   "📦 Прошивка",
			Summary: "агент не ответил",
			Meta:    []string{alerts.KV("роутер", user.Nickname), alerts.KV("команда", "firmware_status")},
			Details: res.Output,
			Hint:    "Вернись назад или перепроверь статус прошивки.",
		}.Render(alerts.CardOpts{MaxBytes: 3900})
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: "↩ Назад", CallbackData: fmt.Sprintf("maint_open:%d:_panel_", user.ID)}},
		}}
		return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
	}
	var fs wire.FirmwareStatus
	if err := json.Unmarshal([]byte(res.Output), &fs); err != nil {
		return fmt.Errorf("decode firmware_status: %w", err)
	}
	n.Audit.PutFirmwareStatus(user.ID, fs)
	cdRem := n.Cooldown.remaining(user.ID, "firmware_install")
	text := tg.FirmwareScreenText(user.Nickname, fs)
	kb := tg.FirmwareScreenKeyboard(user.ID, fs, cdRem)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

// renderActionBanner re-renders the Status screen with a one-line banner
// at the top reflecting the action result. Falls back to a minimal text
// when there's no cached audit (e.g., the user tapped restart before the
// initial version_audit completed).
func (n *MaintPanelNotifier) renderActionBanner(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	banner := alerts.Card{
		Badge:   "✅",
		Label:   "🛠 Обслуживание",
		Summary: strings.TrimSpace(res.Output),
		Meta:    []string{alerts.KV("роутер", user.Nickname), alerts.KV("статус", res.Status)},
	}.Render(alerts.CardOpts{MaxBytes: 900})
	if res.Status != "ok" {
		banner = alerts.Card{
			Badge:   "❌",
			Label:   "🛠 Обслуживание",
			Summary: "команда не выполнена",
			Meta:    []string{alerts.KV("роутер", user.Nickname), alerts.KV("статус", res.Status)},
			Details: res.Output,
		}.Render(alerts.CardOpts{MaxBytes: 900})
	}
	va, ok := n.Audit.GetVersionAudit(user.ID)
	if !ok {
		text := banner
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: "🔄 Обновить", CallbackData: fmt.Sprintf("maint_open:%d:_panel_", user.ID)}},
		}}
		if err := n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb); err != nil {
			return err
		}
		n.enqueueFreshVersionAudit(user.ID, ref, res)
		return nil
	}
	args := buildMaintPanelArgs(ctx, user, va, n.Up, n.Cooldown, n.MiniAppBaseURL, ref.ChatID)
	text := banner + "\n\n" + tg.MaintPanelText(args)
	kb := tg.MaintPanelKeyboard(user.ID, args)
	if err := n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb); err != nil {
		return err
	}
	n.enqueueFreshVersionAudit(user.ID, ref, res)
	return nil
}

func (n *MaintPanelNotifier) enqueueFreshVersionAudit(userID int64, ref cmdpkg.MessageRef, res wire.CommandResult) {
	if n.Sink == nil || res.Status != "ok" {
		return
	}
	cmd := wire.Command{ID: defaultCmdID(), Action: "version_audit", IssuedAt: time.Now().UTC()}
	nextRef := cmdpkg.MessageRef{ChatID: ref.ChatID, MessageID: ref.MessageID, ThreadID: ref.ThreadID}
	if err := n.Sink.EnqueueWithRef(userID, cmd, nextRef); err != nil {
		slog.Warn("maint notifier refresh enqueue failed", "err", err, "user_id", userID)
	}
}

// hrneoKnownInstalled -- «опрос ответил, и ответил «стоит»». Неизвестность
// (nil) панель читает как «не стоит»: рисовать по молчанию «установлен» было
// бы выдумкой. В снимке базы неизвестность при этом остаётся неизвестностью --
// панель показывает сейчас, а база помнит.
func hrneoKnownInstalled(va wire.VersionAudit) bool {
	return va.HrneoInstalled != nil && *va.HrneoInstalled
}

// maintPanelAppURL -- адрес кнопки «Панель роутера» в панели обслуживания.
// Пусто, когда кнопку рисовать нельзя: в группе (у групп chat_id
// отрицательный) Telegram отвергает web_app-кнопку вместе со всем сообщением,
// а без годного адреса панели мини-апп её не откроет.
func maintPanelAppURL(base string, user *db.User, chatID int64) string {
	if chatID <= 0 || user == nil || !backend.PanelKnown(user.AWGMURL) {
		return ""
	}
	return tg.MiniAppRouterSettingsURL(base, user.ID)
}

// buildMaintPanelArgs assembles the renderer args from the cached
// VersionAudit + upstream cache (for the Updates section) + cooldown state.
// Pure function so both the notifier (refresh path) and the router (instant
// cached render in openMaintPanelMessage) can call it.
//
// appBase и chatID нужны кнопке «Панель роутера»: она зависит от чата, в
// котором рисуется панель, и живёт здесь, чтобы ни один путь отрисовки её не
// потерял.
func buildMaintPanelArgs(ctx context.Context, user *db.User, va wire.VersionAudit, up *upstream.Cache, cd *cooldownStore, appBase string, chatID int64) tg.MaintPanelArgs {
	infos, _ := upstream.ComputeUpdates(ctx, up, va)
	updates := make([]tg.UpdateLine, 0, len(infos))
	for _, u := range infos {
		updates = append(updates, tg.UpdateLine{Name: u.Name, Installed: u.Installed, Available: u.Available, Hint: u.Hint})
	}
	return tg.MaintPanelArgs{
		Nickname:                  user.Nickname,
		HrneoInstalled:            hrneoKnownInstalled(va) || va.HrneoVersion != "",
		HrneoVersion:              va.HrneoVersion,
		HrneoUptime:               va.HrneoUptime,
		HrneoRunning:              va.HrneoRunning || (!hrneoKnownInstalled(va) && va.HrneoVersion != ""),
		AwgmgrVersion:             va.AwgmgrVersion,
		AwgmgrUptime:              va.AwgmgrUptime,
		AwgmgrRunning:             va.AwgmgrRunning || va.AwgmgrVersion != "",
		KeeneticOS:                "", // model not in VersionAudit; left empty for now
		Firmware:                  wire.FirmwareStatus{Current: va.FirmwareCurrent, Available: va.FirmwareAvail},
		Updates:                   updates,
		RouterCooldownRemaining:   cd.remaining(user.ID, "router_reboot"),
		FirmwareCooldownRemaining: cd.remaining(user.ID, "firmware_install"),
		PanelAppURL:               maintPanelAppURL(appBase, user, chatID),
	}
}
