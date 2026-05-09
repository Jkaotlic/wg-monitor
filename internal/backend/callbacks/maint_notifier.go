package callbacks

import (
	"context"
	"encoding/json"
	"fmt"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/internal/backend/upstream"
	"github.com/anex/wg-monitor/pkg/wire"
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
		text := fmt.Sprintf("🛠 Обслуживание — %s\n⚠ агент не ответил\n%s", user.Nickname, res.Output)
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
	args := buildMaintPanelArgs(ctx, user, va, n.Up, n.Cooldown)
	text := tg.MaintPanelText(args)
	kb := tg.MaintPanelKeyboard(user.ID, args)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

// renderFirmware updates the firmware-status cache and re-renders the
// firmware screen.
func (n *MaintPanelNotifier) renderFirmware(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, user *db.User) error {
	if res.Status != "ok" {
		text := fmt.Sprintf("📦 Прошивка — %s\n⚠ агент не ответил\n%s", user.Nickname, res.Output)
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
	banner := "✅ " + res.Output
	if res.Status == "err" {
		banner = "❌ " + res.Output
	}
	va, ok := n.Audit.GetVersionAudit(user.ID)
	if !ok {
		text := fmt.Sprintf("🛠 Обслуживание — %s\n\n%s", user.Nickname, banner)
		kb := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{
			{{Text: "🔄 Обновить", CallbackData: fmt.Sprintf("maint_open:%d:_panel_", user.ID)}},
		}}
		return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
	}
	args := buildMaintPanelArgs(ctx, user, va, n.Up, n.Cooldown)
	text := banner + "\n\n" + tg.MaintPanelText(args)
	kb := tg.MaintPanelKeyboard(user.ID, args)
	return n.TG.EditMessageText(ctx, ref.ChatID, ref.MessageID, text, "", &kb)
}

// buildMaintPanelArgs assembles the renderer args from the cached
// VersionAudit + upstream cache (for the Updates section) + cooldown state.
// Pure function so both the notifier (refresh path) and the router (instant
// cached render in openMaintPanelMessage) can call it.
func buildMaintPanelArgs(ctx context.Context, user *db.User, va wire.VersionAudit, up *upstream.Cache, cd *cooldownStore) tg.MaintPanelArgs {
	var updates []tg.UpdateLine
	if va.FirmwareAvail != "" && upstream.FirmwareNewerThan(va.FirmwareCurrent, va.FirmwareAvail) {
		updates = append(updates, tg.UpdateLine{Name: "KeeneticOS", Installed: va.FirmwareCurrent, Available: va.FirmwareAvail})
	}
	if up != nil {
		if v, _ := up.Latest(ctx, "awgmgr"); v != "" && upstream.SoftwareNewerThan(va.AwgmgrVersion, v) {
			updates = append(updates, tg.UpdateLine{Name: "awg-manager", Installed: va.AwgmgrVersion, Available: v})
		}
		if v, _ := up.Latest(ctx, "hrneo"); v != "" && va.HrneoVersion != "" && upstream.SoftwareNewerThan(va.HrneoVersion, v) {
			updates = append(updates, tg.UpdateLine{Name: "HydraRoute-Neo", Installed: va.HrneoVersion, Available: v})
		}
	}
	return tg.MaintPanelArgs{
		Nickname:                  user.Nickname,
		HrneoVersion:              va.HrneoVersion,
		HrneoUptime:               va.HrneoUptime,
		HrneoRunning:              va.HrneoVersion != "",
		AwgmgrVersion:             va.AwgmgrVersion,
		AwgmgrUptime:              va.AwgmgrUptime,
		KeeneticOS:                "", // model not in VersionAudit; left empty for now
		Firmware:                  wire.FirmwareStatus{Current: va.FirmwareCurrent, Available: va.FirmwareAvail},
		Updates:                   updates,
		RouterCooldownRemaining:   cd.remaining(user.ID, "router_reboot"),
		FirmwareCooldownRemaining: cd.remaining(user.ID, "firmware_install"),
	}
}
