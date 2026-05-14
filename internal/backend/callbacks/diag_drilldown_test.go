package callbacks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/tg"
)

func TestDiagTestExpand_CacheHit_RenderDetail(t *testing.T) {
	dc := newDiagCache()
	body := `{"tunnels":{"awg10":{"mtu":{"status":"fail","current":1280,"expected":1380,"reason":"frag"}}}}`
	tok := dc.Put(body, 5*time.Minute)
	tgFake := &fakeDiagTG{}
	a := NewDiagTestExpandAction(dc, tgFake)
	q := &tg.CallbackQuery{ID: "qid", Message: tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "diag_test", UserID: 7, DiagRawToken: tok, DiagTestID: "mtu"}
	_, err := a.Apply(context.Background(), q, args)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	for _, want := range []string{"MTU интерфейса", "awg10", "1280", "1380", "frag", "К сводке"} {
		if !strings.Contains(tgFake.lastText, want) && !hasInKb(tgFake.lastKb, want) {
			t.Errorf("missing %q in render or kb. text=%q", want, tgFake.lastText)
		}
	}
}

func TestDiagTestExpand_CacheMiss(t *testing.T) {
	dc := newDiagCache()
	tgFake := &fakeDiagTG{}
	a := NewDiagTestExpandAction(dc, tgFake)
	q := &tg.CallbackQuery{ID: "qid", Message: tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "diag_test", UserID: 7, DiagRawToken: "deadbeef", DiagTestID: "mtu"}
	_, err := a.Apply(context.Background(), q, args)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(tgFake.lastText, "устарела") {
		t.Errorf("expected stale-cache message, got: %s", tgFake.lastText)
	}
}

func TestDiagTestExpand_TestNotFound(t *testing.T) {
	dc := newDiagCache()
	body := `{"tunnels":{"awg10":{"mtu":{"status":"fail"}}}}`
	tok := dc.Put(body, 5*time.Minute)
	tgFake := &fakeDiagTG{}
	a := NewDiagTestExpandAction(dc, tgFake)
	q := &tg.CallbackQuery{ID: "qid", Message: tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "diag_test", UserID: 7, DiagRawToken: tok, DiagTestID: "missing_test"}
	_, err := a.Apply(context.Background(), q, args)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(tgFake.lastText, "Не нашёл") {
		t.Errorf("expected not-found message, got: %s", tgFake.lastText)
	}
}

type fakeDiagTG struct {
	lastChatID int64
	lastMsgID  int64
	lastText   string
	lastKb     *tg.InlineKeyboardMarkup
}

func (f *fakeDiagTG) EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error {
	f.lastChatID = chatID
	f.lastMsgID = msgID
	f.lastText = text
	f.lastKb = kb
	return nil
}

func hasInKb(kb *tg.InlineKeyboardMarkup, want string) bool {
	if kb == nil {
		return false
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if strings.Contains(b.Text, want) || strings.Contains(b.CallbackData, want) {
				return true
			}
		}
	}
	return false
}

func TestDiagBack_CacheHit_RendersSummary(t *testing.T) {
	dc := newDiagCache()
	body := `{"version":"1.0","generatedAt":"2026-05-14T12:00:00Z","durationMs":2559,"system":{"appVersion":"2.8.2","backend":"nativewg","totalMemoryMB":256}}`
	tok := dc.Put(body, 5*time.Minute)
	tgFake := &fakeDiagTG{}
	a := NewDiagBackAction(dc, tgFake)
	q := &tg.CallbackQuery{ID: "qid", Message: tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "diag_back", UserID: 7, DiagRawToken: tok}
	if _, err := a.Apply(context.Background(), q, args); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(tgFake.lastText, "Диагностика") {
		t.Errorf("expected Диагностика in summary, got: %s", tgFake.lastText)
	}
	if !strings.Contains(tgFake.lastText, "2.8.2") {
		t.Errorf("expected appVersion in details, got: %s", tgFake.lastText)
	}
}

func TestDiagBack_CacheMiss(t *testing.T) {
	dc := newDiagCache()
	tgFake := &fakeDiagTG{}
	a := NewDiagBackAction(dc, tgFake)
	q := &tg.CallbackQuery{ID: "qid", Message: tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "diag_back", UserID: 7, DiagRawToken: "deadbeef"}
	if _, err := a.Apply(context.Background(), q, args); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(tgFake.lastText, "устарела") {
		t.Errorf("expected stale message, got: %s", tgFake.lastText)
	}
}
