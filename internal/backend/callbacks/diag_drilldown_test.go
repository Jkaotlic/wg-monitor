package callbacks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

func TestDiagTestExpand_CacheHit_RenderDetail(t *testing.T) {
	dc := newDiagCache()
	// Настоящая форма отчёта awg-manager 2.18.2: плоский tests[], у проверки
	// VPN-туннеля — tunnelId и tunnelName.
	body := `{"version":"1.0","tests":[` +
		`{"name":"mtu_check","description":"MTU интерфейса","status":"fail","detail":"MTU = 1500, путь пропускает 1280","tunnelId":"awg10","tunnelName":"Дача"},` +
		`{"name":"mtu_check","description":"MTU интерфейса","status":"pass","detail":"MTU = 1280","tunnelId":"awg11","tunnelName":"Работа"}]}`
	tok := dc.Put(body, 5*time.Minute)
	tgFake := &fakeDiagTG{}
	a := NewDiagTestExpandAction(dc, tgFake)
	q := &tg.CallbackQuery{ID: "qid", Message: tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "diag_test", UserID: 7, DiagRawToken: tok, DiagTestID: "mtu_check"}
	_, err := a.Apply(context.Background(), q, args)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// Страница проверки — шаг глубже сводки: здесь уже можно показать, что
	// увидел роутер. Но проверка названа по-человечески, а VPN-туннель —
	// именем владельца, не id.
	for _, want := range []string{"Размер пакета", "VPN-туннель «Дача»", "VPN-туннель «Работа»", "MTU = 1500, путь пропускает 1280", "К сводке"} {
		if !strings.Contains(tgFake.lastText, want) && !hasInKb(tgFake.lastKb, want) {
			t.Errorf("missing %q in render or kb. text=%q", want, tgFake.lastText)
		}
	}
	if strings.Contains(tgFake.lastText, "awg10") {
		t.Errorf("страница проверки показывает id VPN-туннеля: %q", tgFake.lastText)
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
	body := `{"version":"1.0","tests":[{"name":"mtu_check","status":"fail","tunnelId":"awg10","tunnelName":"Дача"}]}`
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
	body := `{"version":"1.0","generatedAt":"2026-05-14T12:00:00Z","durationMs":2559,"system":{"appVersion":"2.8.2","backend":"nativewg","totalMemoryMB":256},` +
		`"tests":[{"name":"wan_connectivity","description":"WAN up с gateway","status":"pass","detail":"default via 10.0.0.1"},` +
		`{"name":"awg_handshake","description":"Handshake свежий (<3 мин)","status":"pass","detail":"1 minute ago","tunnelId":"awg10","tunnelName":"Дача"}]}`
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
	// «К сводке» возвращает ту же сводку владельцу, что пришла первой: ответ,
	// всё ли в порядке, а не версию панели.
	if !strings.Contains(tgFake.lastText, "всё в порядке") {
		t.Errorf("сводка не отвечает, всё ли в порядке: %s", tgFake.lastText)
	}
	if strings.Contains(tgFake.lastText, "2.8.2") {
		t.Errorf("версия панели — инженерия, её место в полном отчёте: %s", tgFake.lastText)
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
