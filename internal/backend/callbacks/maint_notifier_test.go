package callbacks

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

// fakeMaintTG implements MaintEditTG for tests, capturing every Edit call.
type fakeMaintTG struct {
	mu    sync.Mutex
	edits []editCall
}

type editCall struct {
	ChatID int64
	MsgID  int64
	Text   string
	KB     *tg.InlineKeyboardMarkup
}

func (f *fakeMaintTG) EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, editCall{chatID, msgID, text, kb})
	return nil
}

func (f *fakeMaintTG) lastText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.edits) == 0 {
		return ""
	}
	return f.edits[len(f.edits)-1].Text
}

// newMaintNotifierTestRig creates a MaintPanelNotifier with a fresh in-memory
// DB seeded with one user. It reuses the newTestDB helper from actions_test.go.
func newMaintNotifierTestRig(t *testing.T) (*fakeMaintTG, *MaintPanelNotifier, *db.User, *db.DB) {
	t.Helper()
	d, uid := newTestDB(t)
	user, err := d.Users().GetByID(uid)
	if err != nil || user == nil {
		t.Fatalf("GetByID(%d): %v", uid, err)
	}
	tgFake := &fakeMaintTG{}
	cd := newCooldownStore()
	audit := newSimpleAuditCache()
	n := &MaintPanelNotifier{TG: tgFake, Up: nil, Cooldown: cd, Audit: audit, DB: d}
	return tgFake, n, user, d
}

func TestMaintNotifier_VersionAudit_OK_RendersPanel(t *testing.T) {
	tgFake, n, u, _ := newMaintNotifierTestRig(t)
	va := wire.VersionAudit{AwgmgrVersion: "2.8.2", AwgmgrRunning: true, FirmwareCurrent: "5.0.0", HrneoInstalled: true, HrneoRunning: true, HrneoVersion: "2.4.0"}
	body, _ := json.Marshal(va)
	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "version_audit"}
	err := n.NotifyCommandResult(context.Background(), ref, wire.CommandResult{Status: "ok", Output: string(body)}, u.ID)
	if err != nil {
		t.Fatalf("NotifyCommandResult: %v", err)
	}
	got := tgFake.lastText()
	if !strings.Contains(got, "🛠 Обслуживание") {
		t.Errorf("expected panel text, got: %s", got)
	}
	if !strings.Contains(got, "v2.8.2") {
		t.Errorf("expected awgmgr version in render, got: %s", got)
	}
	// Audit cache populated
	if cached, ok := n.Audit.GetVersionAudit(u.ID); !ok || cached.AwgmgrVersion != "2.8.2" {
		t.Errorf("audit cache not populated: %+v", cached)
	}
}

func TestMaintNotifier_VersionAudit_HrneoStopped(t *testing.T) {
	tgFake, n, u, _ := newMaintNotifierTestRig(t)
	va := wire.VersionAudit{
		AwgmgrVersion:   "2.8.2",
		AwgmgrRunning:   true,
		FirmwareCurrent: "5.0.0",
		HrneoInstalled:  true,
		HrneoRunning:    false,
		HrneoVersion:    "2.4.0",
	}
	body, _ := json.Marshal(va)
	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "version_audit"}
	if err := n.NotifyCommandResult(context.Background(), ref, wire.CommandResult{Status: "ok", Output: string(body)}, u.ID); err != nil {
		t.Fatal(err)
	}
	got := tgFake.lastText()
	if !strings.Contains(got, "HydraRoute-Neo: ❌ остановлен") {
		t.Errorf("expected stopped hrneo state, got: %s", got)
	}
	if strings.Contains(got, "HydraRoute-Neo: ✅ running") {
		t.Errorf("must not render stopped hrneo as running, got: %s", got)
	}
}

func TestMaintNotifier_VersionAudit_Err_RendersErrorBanner(t *testing.T) {
	tgFake, n, u, _ := newMaintNotifierTestRig(t)
	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "version_audit"}
	err := n.NotifyCommandResult(context.Background(), ref, wire.CommandResult{Status: "err", Output: "connection refused"}, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := tgFake.lastText()
	if !strings.Contains(got, "агент не ответил") {
		t.Errorf("expected error banner, got: %s", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Errorf("expected error message in banner, got: %s", got)
	}
}

func TestMaintNotifier_FirmwareStatus_OK(t *testing.T) {
	tgFake, n, u, _ := newMaintNotifierTestRig(t)
	fs := wire.FirmwareStatus{Current: "5.0.1", Channel: "stable"}
	body, _ := json.Marshal(fs)
	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "firmware_status"}
	if err := n.NotifyCommandResult(context.Background(), ref, wire.CommandResult{Status: "ok", Output: string(body)}, u.ID); err != nil {
		t.Fatal(err)
	}
	got := tgFake.lastText()
	if !strings.Contains(got, "📦 Прошивка") {
		t.Errorf("expected firmware screen, got: %s", got)
	}
	if !strings.Contains(got, "актуальная") {
		t.Errorf("expected current firmware text, got: %s", got)
	}
	if cached, ok := n.Audit.GetFirmwareStatus(u.ID); !ok || cached.Current != "5.0.1" {
		t.Errorf("firmware cache not populated: %+v", cached)
	}
}

func TestMaintNotifier_FirmwareStatus_Err_RendersErrorBanner(t *testing.T) {
	tgFake, n, u, _ := newMaintNotifierTestRig(t)
	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "firmware_status"}
	if err := n.NotifyCommandResult(context.Background(), ref, wire.CommandResult{Status: "err", Output: "agent timeout"}, u.ID); err != nil {
		t.Fatal(err)
	}
	got := tgFake.lastText()
	if !strings.Contains(got, "агент не ответил") {
		t.Errorf("expected error banner, got: %s", got)
	}
	if !strings.Contains(got, "agent timeout") {
		t.Errorf("expected error details in banner, got: %s", got)
	}
}

func TestMaintNotifier_ServiceRestart_RendersBannerWithCachedAudit(t *testing.T) {
	tgFake, n, u, _ := newMaintNotifierTestRig(t)
	// Pre-seed audit cache so the banner can render full panel
	n.Audit.PutVersionAudit(u.ID, wire.VersionAudit{AwgmgrVersion: "2.8.2", FirmwareCurrent: "5.0.0"})
	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "service_restart"}
	if err := n.NotifyCommandResult(context.Background(), ref, wire.CommandResult{Status: "ok", Output: "hrneo restart sent"}, u.ID); err != nil {
		t.Fatal(err)
	}
	got := tgFake.lastText()
	if !strings.Contains(got, "✅") || !strings.Contains(got, "hrneo restart sent") {
		t.Errorf("expected success banner, got: %s", got)
	}
	if !strings.Contains(got, "🛠 Обслуживание") {
		t.Errorf("expected panel re-render below banner, got: %s", got)
	}
}

func TestMaintNotifier_ServiceRestart_EnqueuesFreshVersionAudit(t *testing.T) {
	_, n, u, _ := newMaintNotifierTestRig(t)
	n.Audit.PutVersionAudit(u.ID, wire.VersionAudit{AwgmgrVersion: "2.8.2", FirmwareCurrent: "5.0.0"})
	sink := &fakeEnqueuer{}
	n.Sink = sink

	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200, ThreadID: ptrInt64(300), Action: "service_restart"}
	if err := n.NotifyCommandResult(context.Background(), ref, wire.CommandResult{Status: "ok", Output: "hrneo restart sent"}, u.ID); err != nil {
		t.Fatal(err)
	}

	if len(sink.calls) != 1 || sink.calls[0].action != "version_audit" {
		t.Fatalf("successful maintenance action should enqueue fresh version_audit, got %+v", sink.calls)
	}
	if len(sink.refs) != 1 || sink.refs[0].chatID != 100 || sink.refs[0].messageID != 200 {
		t.Fatalf("fresh version_audit should target same panel message, refs=%+v", sink.refs)
	}
}

func TestMaintNotifier_ServiceRestart_NoCachedAudit_MinimalText(t *testing.T) {
	tgFake, n, u, _ := newMaintNotifierTestRig(t)
	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "service_restart"}
	if err := n.NotifyCommandResult(context.Background(), ref, wire.CommandResult{Status: "ok", Output: "ok"}, u.ID); err != nil {
		t.Fatal(err)
	}
	got := tgFake.lastText()
	if !strings.Contains(got, "✅") {
		t.Errorf("expected success banner, got: %s", got)
	}
	// Without cached audit, no full panel rendered — just banner + refresh button
	if !strings.Contains(got, "🛠 Обслуживание") && !strings.Contains(got, "Обновить") {
		t.Errorf("expected at least header or refresh hint, got: %s", got)
	}
}

func TestMaintNotifier_FirmwareInstall_ErrorBanner(t *testing.T) {
	tgFake, n, u, _ := newMaintNotifierTestRig(t)
	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "firmware_install"}
	if err := n.NotifyCommandResult(context.Background(), ref, wire.CommandResult{Status: "err", Output: "ndmc failed"}, u.ID); err != nil {
		t.Fatal(err)
	}
	got := tgFake.lastText()
	if !strings.Contains(got, "❌") || !strings.Contains(got, "ndmc failed") {
		t.Errorf("expected error banner, got: %s", got)
	}
}

func TestMaintNotifier_UnsupportedAction_ReturnsError(t *testing.T) {
	_, n, u, _ := newMaintNotifierTestRig(t)
	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "wat"}
	if err := n.NotifyCommandResult(context.Background(), ref, wire.CommandResult{Status: "ok"}, u.ID); err == nil {
		t.Error("expected error for unsupported action")
	}
}
