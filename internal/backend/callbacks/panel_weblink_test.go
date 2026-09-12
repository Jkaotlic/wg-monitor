package callbacks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Кнопка «Открыть в браузере» живёт внутри уже существующего хаба /panel:
// новой команды /admin не заводим, поэтому реестр команд и справка не
// меняются вовсе.
func TestPanelHubOffersWebLinkButton(t *testing.T) {
	_, kb := panelHomeMessage()
	if !containsStr(flattenKbCallbacks(&kb), "panel:0:weblink") {
		t.Fatalf("в хабе нет кнопки веб-ссылки: %v", flattenKbCallbacks(&kb))
	}
}

func panelWebLinkQuery(from int64) *tg.CallbackQuery {
	return &tg.CallbackQuery{
		ID:   "cb-weblink",
		From: tg.User{ID: from},
		Data: "panel:0:weblink",
		Message: tg.Message{
			Chat:      tg.Chat{ID: -100},
			MessageID: 80,
		},
	}
}

func TestPanelWebLinkGivesLinkAndSaysHowLongItLives(t *testing.T) {
	d := newTestDBEmpty(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, PublicBaseURL: "https://wg.example.com"})

	r.HandleCallback(context.Background(), panelWebLinkQuery(12345))

	if len(f.edits) != 1 {
		t.Fatalf("правок сообщения = %d, want 1 (ответы: %v)", len(f.edits), f.answers)
	}
	text := f.edits[0]
	if !strings.Contains(text, "https://wg.example.com/dashboard/login#token=") {
		t.Fatalf("в ответе нет ссылки на вход: %s", text)
	}
	// Срок обязан быть сказан человеку, а не спрятан в коде.
	if !strings.Contains(text, "12 часов") {
		t.Errorf("в ответе не сказан срок жизни ссылки: %s", text)
	}
	if !strings.Contains(text, "Не пересылайте") {
		t.Errorf("в ответе нет предупреждения о пересылке: %s", text)
	}
	if !strings.Contains(text, "три последние ссылки") {
		t.Errorf("в ответе не сказано про лимит живых ссылок: %s", text)
	}
	live, err := d.WebLinks().ActiveFor(12345, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Fatalf("живых грантов в базе = %d, want 1", len(live))
	}
}

// Админ не настроен -- общий гейт панели открыт настежь (router.go:356
// пропускает всех, когда AdminUserID == 0), и выдать ссылку на управление
// всем парком в этот момент нельзя.
func TestPanelWebLinkRefusesWhenAdminIsNotConfigured(t *testing.T) {
	d := newTestDBEmpty(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 0, PublicBaseURL: "https://wg.example.com"})

	r.HandleCallback(context.Background(), panelWebLinkQuery(777))

	if !containsStr(f.answers, backend.WebLinkCopyAdminOnly) {
		t.Fatalf("отказ не сказан словами: ответы %v", f.answers)
	}
	for _, text := range append(append([]string{}, f.edits...), f.sentMsgs...) {
		if strings.Contains(text, "#token=") {
			t.Fatalf("ссылка всё-таки уехала: %s", text)
		}
	}
}

// Публичного адреса по https нет -- объясняем словами, а не выдаём ссылку в
// никуда.
func TestPanelWebLinkSaysWhenPublicAddressIsMissing(t *testing.T) {
	for _, base := range []string{"", "http://wg.example.com"} {
		d := newTestDBEmpty(t)
		f := &fakeRouterTGFull{}
		r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, PublicBaseURL: base})

		r.HandleCallback(context.Background(), panelWebLinkQuery(12345))

		said := append(append([]string{}, f.edits...), f.answers...)
		if !containsStr(said, backend.WebLinkCopyNoPublicBase) {
			t.Fatalf("base=%q: отказ не сказан словами: %v", base, said)
		}
	}
}
