package callbacks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
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

// panelWebLinkQuery собирает нажатие кнопки. Чат задаётся отдельно от
// нажавшего: ровно в этой разнице и живёт вопрос «кому достанется ссылка».
func panelWebLinkQuery(from, chatID int64) *tg.CallbackQuery {
	return &tg.CallbackQuery{
		ID:   "cb-weblink",
		From: tg.User{ID: from},
		Data: "panel:0:weblink",
		Message: tg.Message{
			Chat:      tg.Chat{ID: chatID},
			MessageID: 80,
		},
	}
}

// everythingSaid -- все тексты, которые бот куда-либо отправил: правки
// сообщения, новые сообщения, всплывающие ответы и посылки с клавиатурой.
func everythingSaid(f *fakeRouterTGFull) []string {
	said := append([]string{}, f.edits...)
	said = append(said, f.sentMsgs...)
	said = append(said, f.answers...)
	for _, s := range f.rkSends {
		said = append(said, s.text)
	}
	return said
}

func assertNoGrantAnywhere(t *testing.T, f *fakeRouterTGFull, d *db.DB, tgIDs ...int64) {
	t.Helper()
	for _, text := range everythingSaid(f) {
		if strings.Contains(text, "#token=") || strings.Contains(text, "/dashboard/login") {
			t.Fatalf("ссылка уехала в сообщение: %s", text)
		}
	}
	for _, tgID := range tgIDs {
		live, err := d.WebLinks().ActiveFor(tgID, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if len(live) != 0 {
			t.Fatalf("грант выдан (%d живых у %d): ссылки, которую нельзя показать, быть не должно", len(live), tgID)
		}
	}
}

// ГЛАВНОЕ свойство канала доставки: в общем чате ссылка не появляется.
//
// Хаб /panel открывается в том чате, откуда пришла команда, а разрешённые
// чаты (chatAllowed) -- это общая группа с темами роутеров, где по построению
// сидят владельцы и операторы, а не только админ. Грант живёт 12 часов,
// многоразовый и сверяется только с админом из конфига: любой, кто скопировал
// его из группы, получил бы полное управление всем парком, и в журнале это
// выглядело бы входом админа.
//
// Поэтому в группе грант не просто не показывается -- он не выдаётся вовсе.
func TestPanelWebLinkInGroupChatNeverShowsTheLink(t *testing.T) {
	d := newTestDBEmpty(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, PublicBaseURL: "https://wg.example.com"})

	r.HandleCallback(context.Background(), panelWebLinkQuery(12345, -100))

	assertNoGrantAnywhere(t, f, d, 12345)
	// И человеку сказано, где ссылку взять, а не «экран ещё не готов».
	if !containsStr(everythingSaid(f), backend.WebLinkCopyOnlyInDM) {
		t.Fatalf("в группе не сказано, что ссылка приходит в личку: %v", everythingSaid(f))
	}
}

// В личке админа ссылка выдаётся, и срок сказан словами.
func TestPanelWebLinkInDirectMessageGivesLinkAndSaysHowLongItLives(t *testing.T) {
	d := newTestDBEmpty(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, PublicBaseURL: "https://wg.example.com"})

	// Личка: чат совпадает с нажавшим -- та же примета, по которой
	// HandleCallback пускает админские панели в личку (adminPrivatePanel,
	// router.go:329), и она же запинена TestPanelHome_AdminDMAllowed.
	r.HandleCallback(context.Background(), panelWebLinkQuery(12345, 12345))

	said := everythingSaid(f)
	if !anyContains(said, "https://wg.example.com/dashboard/login#token=") {
		t.Fatalf("в личке ссылки нет: %v", said)
	}
	if !anyContains(said, "12 часов") {
		t.Errorf("не сказан срок жизни ссылки: %v", said)
	}
	if !anyContains(said, "Не пересылайте") {
		t.Errorf("нет предупреждения о пересылке: %v", said)
	}
	if !anyContains(said, "три последние ссылки") {
		t.Errorf("не сказано про лимит живых ссылок: %v", said)
	}
	live, err := d.WebLinks().ActiveFor(12345, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Fatalf("живых грантов в базе = %d, want 1", len(live))
	}
}

// Админ не настроен -- и хаб /panel в разрешённом чате открыт настежь:
// router.go:356 пропускает всех, когда AdminUserID == 0. Это ровно тот
// случай, который в бою выглядит как «доступ у всех»: пустой admin_user_id в
// backend.yaml -- и админский хаб публичен для всей группы.
//
// Чат здесь групповой намеренно: в личке такой вызов до кнопки вообще не
// доходит (adminPrivatePanel требует настроенного админа, а chatAllowed
// личку не знает), и тест проверял бы чужой гейт. В разрешённом чате нажатие
// доходит до кнопки -- и обязано получить отказ на ней самой.
func TestPanelWebLinkRefusesWhenAdminIsNotConfigured(t *testing.T) {
	d := newTestDBEmpty(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 0, PublicBaseURL: "https://wg.example.com"})

	r.HandleCallback(context.Background(), panelWebLinkQuery(777, -100))

	if !containsStr(f.answers, backend.WebLinkCopyAdminOnly) {
		t.Fatalf("отказ не сказан словами: ответы %v", f.answers)
	}
	assertNoGrantAnywhere(t, f, d, 777)
}

// Админ ЗАДАН, а жмёт не он. Вызов идёт прямо в panelWebLink, минуя общий
// гейт роутера: проверяется именно свой гейт кнопки, иначе тест доказывал бы
// работу чужой проверки, а не этой.
func TestPanelWebLinkRefusesSomeoneElseWhenAdminIsSet(t *testing.T) {
	d := newTestDBEmpty(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, PublicBaseURL: "https://wg.example.com"})

	r.panelWebLink(context.Background(), panelWebLinkQuery(777, 777))

	if !containsStr(f.answers, backend.WebLinkCopyAdminOnly) {
		t.Fatalf("свой гейт кнопки не отказал постороннему: ответы %v", f.answers)
	}
	assertNoGrantAnywhere(t, f, d, 777, 12345)
}

// Публичного адреса по https нет -- объясняем словами, а не выдаём ссылку в
// никуда.
func TestPanelWebLinkSaysWhenPublicAddressIsMissing(t *testing.T) {
	for _, base := range []string{"", "http://wg.example.com"} {
		d := newTestDBEmpty(t)
		f := &fakeRouterTGFull{}
		r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, PublicBaseURL: base})

		r.HandleCallback(context.Background(), panelWebLinkQuery(12345, 12345))

		if !containsStr(everythingSaid(f), backend.WebLinkCopyNoPublicBase) {
			t.Fatalf("base=%q: отказ не сказан словами: %v", base, everythingSaid(f))
		}
	}
}

func anyContains(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
