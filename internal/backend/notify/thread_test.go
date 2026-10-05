package notify

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// threadTG -- Telegram для нити починки: отправки с кнопками и без, правки.
type threadTG struct {
	mu      sync.Mutex
	sends   []threadSend
	edits   []threadEdit
	editErr map[int64]error // chatID -> ошибка правки
	nextID  int64
}

type threadSend struct {
	chatID  int64
	text    string
	replyTo *int64
	kb      *tg.InlineKeyboardMarkup
}

type threadEdit struct {
	chatID, messageID int64
	text              string
	kb                *tg.InlineKeyboardMarkup
}

func (f *threadTG) send(chatID int64, text string, replyTo *int64, kb *tg.InlineKeyboardMarkup) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.sends = append(f.sends, threadSend{chatID, text, replyTo, kb})
	return 900 + f.nextID, nil
}

func (f *threadTG) SendMessage(_ context.Context, chatID int64, _ *int64, text, _ string, replyTo *int64) (int64, error) {
	return f.send(chatID, text, replyTo, nil)
}

func (f *threadTG) SendMessageWithKeyboard(_ context.Context, chatID int64, _ *int64, text, _ string, replyTo *int64, kb *tg.InlineKeyboardMarkup) (int64, error) {
	return f.send(chatID, text, replyTo, kb)
}

func (f *threadTG) EditMessageText(_ context.Context, chatID, messageID int64, text, _ string, kb *tg.InlineKeyboardMarkup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.editErr[chatID]; ok {
		return err
	}
	f.edits = append(f.edits, threadEdit{chatID, messageID, text, kb})
	return nil
}

// fakeTexts -- память диспетчера о последней тревоге.
type fakeTexts struct {
	text string
	kb   *tg.InlineKeyboardMarkup
	ok   bool
}

func (f fakeTexts) Last(int64, string) (string, *tg.InlineKeyboardMarkup, bool) {
	return f.text, f.kb, f.ok
}

const (
	threadCheck = "tunnel_awg11"
	threadAlert = "🔴 [router-a]: VPN-туннель «Франкфурт» упал"
)

func alertKB() *tg.InlineKeyboardMarkup {
	kb := tg.AlertKeyboard(1, threadCheck, "https://example.com/miniapp/?router=1")
	return &kb
}

// threadSetup -- роутер с владельцем 1001 и оператором 1002, обоим ушла
// тревога (сообщения 11 и 12).
func threadSetup(t *testing.T) (*db.DB, int64) {
	t.Helper()
	d, router := newDB(t)
	if err := d.Users().SetTelegramUserID(router, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.RouterOperators().Add(router, 1002, 1001); err != nil {
		t.Fatal(err)
	}
	if err := d.AlertMessages().Put(router, threadCheck, 1001, 11); err != nil {
		t.Fatal(err)
	}
	if err := d.AlertMessages().Put(router, threadCheck, 1002, 12); err != nil {
		t.Fatal(err)
	}
	return d, router
}

func newTestRepairs(d *db.DB, tgc *threadTG, texts AlertTexts, now func() time.Time) *Repairs {
	f := NewFanout(d, tgc, quietLogger(), 0)
	if now == nil {
		now = time.Now
	}
	return NewRepairs(f, tgc, d, texts, now)
}

func TestThread_ProgressEditsEveryRecipient(t *testing.T) {
	d, router := threadSetup(t)
	tgc := &threadTG{}
	kb := alertKB()
	r := newTestRepairs(d, tgc, fakeTexts{threadAlert, kb, true}, nil)

	th := r.Begin(context.Background(), router, threadCheck)
	th.Progress(context.Background(), "Чиню: перезапускаю VPN-туннель «Франкфурт»…")

	if len(tgc.sends) != 0 {
		t.Fatalf("ход починки обязан править тревогу, а не слать новое: %+v", tgc.sends)
	}
	if len(tgc.edits) != 2 {
		t.Fatalf("правок=%d, ждали две -- каждому получателю", len(tgc.edits))
	}
	want := threadAlert + "\n\n" + "Чиню: перезапускаю VPN-туннель «Франкфурт»…"
	got := map[int64]int64{}
	for _, e := range tgc.edits {
		got[e.chatID] = e.messageID
		if e.text != want {
			t.Errorf("текст правки:\n%q\nждали:\n%q", e.text, want)
		}
		if e.kb == nil || len(e.kb.InlineKeyboard) != len(kb.InlineKeyboard) {
			t.Errorf("кнопки тревоги потерялись в правке: %+v", e.kb)
		}
	}
	if got[1001] != 11 || got[1002] != 12 {
		t.Fatalf("правки ушли не в те сообщения: %v", got)
	}

	// Следующий ход заменяет блок, а не копит его.
	tgc.edits = nil
	th.Progress(context.Background(), "Чиню: перезапуск не помог · сейчас: выпускаю конфиг заново…")
	for _, e := range tgc.edits {
		if strings.Count(e.text, "Чиню:") != 1 {
			t.Fatalf("блок хода накапливается:\n%s", e.text)
		}
	}
}

// Админу правка несёт тот же ряд «Не писать мне», что был под тревогой.
func TestThread_AdminEditKeepsMuteRow(t *testing.T) {
	d, router := newDB(t)
	if err := d.AlertMessages().Put(router, threadCheck, 7000, 70); err != nil {
		t.Fatal(err)
	}
	tgc := &threadTG{}
	f := NewFanout(d, tgc, quietLogger(), 7000)
	r := NewRepairs(f, tgc, d, fakeTexts{threadAlert, alertKB(), true}, time.Now)

	r.Begin(context.Background(), router, threadCheck).Progress(context.Background(), "Чиню: …")
	if len(tgc.edits) != 1 {
		t.Fatalf("правок=%d, ждали одну админу", len(tgc.edits))
	}
	rows := tgc.edits[0].kb.InlineKeyboard
	last := rows[len(rows)-1][0]
	if last.Text != AdminMuteButtonText {
		t.Fatalf("у админа пропал ряд выключения: %+v", rows)
	}
}

// Снимок номеров сообщений делается в Begin: «восстановилось» очищает
// таблицу, а итог починки всё равно обязан дописаться в тревогу.
func TestThread_EditsSnapshotAfterTableCleared(t *testing.T) {
	d, router := threadSetup(t)
	tgc := &threadTG{}
	r := newTestRepairs(d, tgc, fakeTexts{threadAlert, alertKB(), true}, nil)

	th := r.Begin(context.Background(), router, threadCheck)
	if err := d.AlertMessages().Clear(router, threadCheck); err != nil {
		t.Fatal(err)
	}
	th.Done(context.Background(), "Починил: перезапустил. VPN-туннель «Франкфурт» снова работает.")
	if len(tgc.edits) != 2 || len(tgc.sends) != 0 {
		t.Fatalf("правок=%d отправок=%d, ждали две правки по снимку", len(tgc.edits), len(tgc.sends))
	}
}

func TestThread_FallsBackToSendOnEditError(t *testing.T) {
	d, router := threadSetup(t)
	tgc := &threadTG{editErr: map[int64]error{1001: errors.New("Bad Request: message to edit not found")}}
	kb := alertKB()
	r := newTestRepairs(d, tgc, fakeTexts{threadAlert, kb, true}, nil)

	th := r.Begin(context.Background(), router, threadCheck)
	th.Progress(context.Background(), "Чиню: перезапускаю…")

	if len(tgc.edits) != 1 || tgc.edits[0].chatID != 1002 {
		t.Fatalf("правки=%+v, ждали одну -- оператору 1002", tgc.edits)
	}
	if len(tgc.sends) != 1 || tgc.sends[0].chatID != 1001 {
		t.Fatalf("отправки=%+v, ждали новое сообщение владельцу 1001", tgc.sends)
	}
	if !strings.Contains(tgc.sends[0].text, threadAlert) || !strings.Contains(tgc.sends[0].text, "Чиню: перезапускаю…") {
		t.Fatalf("новое сообщение без тревоги или хода:\n%s", tgc.sends[0].text)
	}
	if tgc.sends[0].kb == nil {
		t.Fatal("новое сообщение потеряло кнопки тревоги")
	}

	// Дальше правится уже новое сообщение: каждый ход новым сообщением --
	// это спам, а не тред.
	delete(tgc.editErr, 1001)
	tgc.edits, tgc.sends = nil, nil
	th.Progress(context.Background(), "Чиню: дальше…")
	if len(tgc.sends) != 0 {
		t.Fatalf("после замены сообщения снова шлём новое: %+v", tgc.sends)
	}
	for _, e := range tgc.edits {
		if e.chatID == 1001 && e.messageID != 901 {
			t.Fatalf("правится старое сообщение %d, ждали новое 901", e.messageID)
		}
	}
}

func TestThread_NeedHumanRepliesWithSound(t *testing.T) {
	d, router := threadSetup(t)
	tgc := &threadTG{}
	r := newTestRepairs(d, tgc, fakeTexts{threadAlert, alertKB(), true}, nil)
	r.SetMiniAppBaseURL("https://example.com")

	th := r.Begin(context.Background(), router, threadCheck)
	const action = "обновите ключ «Amnezia Premium» во вкладке «Управление»"
	th.NeedHuman(context.Background(), "Починить VPN-туннель «Франкфурт» сам не смог (перезапуск не помог). Заблокированное сейчас не открывается.", action)

	if len(tgc.edits) != 2 {
		t.Fatalf("правок=%d, итог обязан дописаться в тревогу обоим", len(tgc.edits))
	}
	if len(tgc.sends) != 2 {
		t.Fatalf("ответов=%d, ждали громкий ответ обоим", len(tgc.sends))
	}
	want := "Починить VPN-туннель «Франкфурт» сам не смог. Нужно ваше участие: " + action + "."
	for _, s := range tgc.sends {
		if s.replyTo == nil {
			t.Fatalf("ответ не привязан к тревоге: %+v", s)
		}
		wantReply := map[int64]int64{1001: 11, 1002: 12}[s.chatID]
		if *s.replyTo != wantReply {
			t.Errorf("ответ %d на сообщение %d, ждали %d", s.chatID, *s.replyTo, wantReply)
		}
		if s.text != want {
			t.Errorf("текст ответа:\n%q\nждали:\n%q", s.text, want)
		}
		if bad := latinOutsideQuotes(s.text); len(bad) > 0 {
			t.Errorf("латиница вне ёлочек %v:\n%s", bad, s.text)
		}
		if s.kb == nil || len(s.kb.InlineKeyboard) != 1 || s.kb.InlineKeyboard[0][0].WebApp == nil ||
			s.kb.InlineKeyboard[0][0].WebApp.URL != tg.MiniAppRouterURL("https://example.com", router) {
			t.Errorf("под ответом нет кнопки в приложение: %+v", s.kb)
		}
	}
}

// Без адреса мини-аппа ответ уходит без кнопки, а пустое действие -- только
// правка.
func TestThread_NeedHumanWithoutURLOrAction(t *testing.T) {
	d, router := threadSetup(t)
	tgc := &threadTG{}
	r := newTestRepairs(d, tgc, fakeTexts{threadAlert, alertKB(), true}, nil)

	th := r.Begin(context.Background(), router, threadCheck)
	th.NeedHuman(context.Background(), "Починить VPN-туннель «Франкфурт» сам не смог.", "обновите агента во вкладке «Управление»")
	if len(tgc.sends) != 2 {
		t.Fatalf("ответов=%d, ждали два", len(tgc.sends))
	}
	for _, s := range tgc.sends {
		if s.kb != nil {
			t.Fatalf("без адреса мини-аппа кнопки быть не может: %+v", s.kb)
		}
	}

	tgc.sends, tgc.edits = nil, nil
	th.NeedHuman(context.Background(), "VPN-туннель «Франкфурт» упал, но чинить нечего.", "")
	if len(tgc.sends) != 0 || len(tgc.edits) != 2 {
		t.Fatalf("пустое действие: отправок=%d правок=%d, ждали только правки", len(tgc.sends), len(tgc.edits))
	}
}

func TestThread_NotStartedEdit(t *testing.T) {
	d, router := threadSetup(t)
	tgc := &threadTG{}
	r := newTestRepairs(d, tgc, fakeTexts{threadAlert, alertKB(), true}, nil)

	r.Begin(context.Background(), router, threadCheck).NotStarted(context.Background(), "уже идёт починка или замена конфига")
	if len(tgc.edits) != 2 {
		t.Fatalf("правок=%d, ждали две", len(tgc.edits))
	}
	if !strings.HasSuffix(tgc.edits[0].text, "\n\nАвтопочинка не запускалась: уже идёт починка или замена конфига.") {
		t.Fatalf("текст правки:\n%s", tgc.edits[0].text)
	}
}

// Бэкенд перезапускался: текста тревоги в памяти нет, править нечем.
// Ход молчит (иначе каждая ступень -- новое сообщение), итоги уходят новым.
func TestThread_NoAlertTextDegradesToSend(t *testing.T) {
	d, router := threadSetup(t)
	tgc := &threadTG{}
	r := newTestRepairs(d, tgc, fakeTexts{}, nil)
	r.SetMiniAppBaseURL("https://example.com")

	th := r.Begin(context.Background(), router, threadCheck)
	th.Progress(context.Background(), "Чиню: …")
	if len(tgc.sends)+len(tgc.edits) != 0 {
		t.Fatalf("ход без тревоги обязан молчать: %+v %+v", tgc.sends, tgc.edits)
	}
	th.Done(context.Background(), "Починил. VPN-туннель «Франкфурт» снова работает.")
	if len(tgc.sends) != 2 || len(tgc.edits) != 0 {
		t.Fatalf("итог: отправок=%d правок=%d, ждали два новых сообщения", len(tgc.sends), len(tgc.edits))
	}
	tgc.sends = nil
	th.NeedHuman(context.Background(), "Починить VPN-туннель «Франкфурт» сам не смог.", "обновите агента во вкладке «Управление»")
	if len(tgc.sends) != 2 {
		t.Fatalf("«нужно участие» без тревоги: отправок=%d, ждали два", len(tgc.sends))
	}
	if !strings.Contains(tgc.sends[0].text, "Нужно ваше участие: обновите агента во вкладке «Управление».") {
		t.Fatalf("действие потерялось:\n%s", tgc.sends[0].text)
	}
	if tgc.sends[0].kb == nil {
		t.Fatal("кнопка в приложение потерялась")
	}
}

func TestRepairs_CoveredWindow(t *testing.T) {
	d, router := threadSetup(t)
	tgc := &threadTG{}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	r := newTestRepairs(d, tgc, fakeTexts{threadAlert, alertKB(), true}, clock)

	if r.TakeCovered(router, threadCheck) {
		t.Fatal("до починки проверка не закрыта")
	}
	r.Begin(context.Background(), router, threadCheck).Done(context.Background(), "Починил.")
	if r.TakeCovered(router, "tunnel_awg12") || r.TakeCovered(router+1, threadCheck) {
		t.Fatal("закрыта чужая проверка")
	}
	now = now.Add(31 * time.Minute)
	if r.TakeCovered(router, threadCheck) {
		t.Fatal("через 31 минуту закрытие обязано истечь")
	}
}

// Одна тревога -- одно «восстановилось»: после Done гасится первое, второе
// (новый случай) уходит как обычно.
func TestRepairs_CoveredConsumeOnce(t *testing.T) {
	d, router := threadSetup(t)
	r := newTestRepairs(d, &threadTG{}, fakeTexts{threadAlert, alertKB(), true}, nil)
	r.Begin(context.Background(), router, threadCheck).Done(context.Background(), "Починил.")
	if !r.TakeCovered(router, threadCheck) {
		t.Fatal("после «Починил» первое «восстановилось» обязано гаситься")
	}
	if r.TakeCovered(router, threadCheck) {
		t.Fatal("закрытие одноразовое: второе «восстановилось» уходит")
	}
}

// Частый случай в проде: «восстановилось» приходит, пока лесенка ещё
// доказывает результат. Ответ гасится, итог «Починил» дописывается правкой,
// а следующий случай не наследует закрытия.
func TestRepairs_RecoveryBeforeDone(t *testing.T) {
	d, router := threadSetup(t)
	tgc := &threadTG{}
	r := newTestRepairs(d, tgc, fakeTexts{threadAlert, alertKB(), true}, nil)
	th := r.Begin(context.Background(), router, threadCheck)
	if !r.TakeCovered(router, threadCheck) {
		t.Fatal("пока починка идёт, «восстановилось» обязано гаситься")
	}
	th.Done(context.Background(), "Починил.")
	if len(tgc.edits) != 2 || len(tgc.sends) != 0 {
		t.Fatalf("правок=%d отправок=%d, ждали только правку «Починил»", len(tgc.edits), len(tgc.sends))
	}
	if r.TakeCovered(router, threadCheck) {
		t.Fatal("погашенное «восстановилось» не продлевается Done на следующий случай")
	}
}

// «Сам не смог» и «не запускалась» закрытия не оставляют: поднимется
// VPN-туннель сам -- человек получит обычное «восстановилось».
func TestRepairs_NeedHumanAndNotStartedUncover(t *testing.T) {
	d, router := threadSetup(t)
	r := newTestRepairs(d, &threadTG{}, fakeTexts{threadAlert, alertKB(), true}, nil)
	r.Begin(context.Background(), router, threadCheck).NeedHuman(context.Background(), "Починить VPN-туннель «Франкфурт» сам не смог.", "")
	if r.TakeCovered(router, threadCheck) {
		t.Fatal("после «сам не смог» «восстановилось» обязано уйти")
	}
	r.Begin(context.Background(), router, threadCheck).NotStarted(context.Background(), "лимит попыток")
	if r.TakeCovered(router, threadCheck) {
		t.Fatal("после «не запускалась» «восстановилось» обязано уйти")
	}
}

// Вторая попытка запуска при идущей починке («уже идёт починка») своим
// NotStarted не снимает закрытие идущей.
func TestRepairs_SecondBeginDoesNotStealCover(t *testing.T) {
	d, router := threadSetup(t)
	r := newTestRepairs(d, &threadTG{}, fakeTexts{threadAlert, alertKB(), true}, nil)
	running := r.Begin(context.Background(), router, threadCheck)
	r.Begin(context.Background(), router, threadCheck).NotStarted(context.Background(), "уже идёт починка или замена конфига")
	running.Done(context.Background(), "Починил.")
	if !r.TakeCovered(router, threadCheck) {
		t.Fatal("чужое NotStarted сняло закрытие идущей починки")
	}
}

// Новая HARD-тревога снимает устаревшее закрытие.
func TestRepairs_UncoverOnNewHard(t *testing.T) {
	d, router := threadSetup(t)
	r := newTestRepairs(d, &threadTG{}, fakeTexts{threadAlert, alertKB(), true}, nil)
	r.Begin(context.Background(), router, threadCheck).Done(context.Background(), "Починил.")
	r.Uncover(router, threadCheck)
	if r.TakeCovered(router, threadCheck) {
		t.Fatal("новая тревога унаследовала закрытие прежней починки")
	}
}

// latinOutsideQuotes -- слова латиницей вне ёлочек, кроме «VPN».
func latinOutsideQuotes(text string) []string {
	var bad []string
	depth := 0
	var word strings.Builder
	flush := func() {
		if w := word.String(); w != "" && w != "VPN" {
			bad = append(bad, w)
		}
		word.Reset()
	}
	for _, r := range text {
		switch {
		case r == '«':
			flush()
			depth++
		case r == '»':
			if depth > 0 {
				depth--
			}
		case depth == 0 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			word.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return bad
}

// Заглушивший роутер (или снятый оператор) правок и ответов больше не
// получает: ни заглушивший до начала починки, ни посреди неё.
func TestThread_MutedRecipientsSkipped(t *testing.T) {
	d, router := threadSetup(t)
	tgc := &threadTG{}
	r := newTestRepairs(d, tgc, fakeTexts{threadAlert, alertKB(), true}, nil)
	r.SetMiniAppBaseURL("https://example.com")

	// Заглушил до начала: в снимок не попадает.
	if err := d.NotifyMutes().SetMuted(1002, router, true); err != nil {
		t.Fatal(err)
	}
	th := r.Begin(context.Background(), router, threadCheck)
	th.Progress(context.Background(), "Чиню: …")
	for _, e := range tgc.edits {
		if e.chatID == 1002 {
			t.Fatalf("заглушивший до починки получил правку: %+v", e)
		}
	}

	// Заглушил посреди починки: дальше ни правок, ни ответа.
	if err := d.NotifyMutes().SetMuted(1002, router, false); err != nil {
		t.Fatal(err)
	}
	th = r.Begin(context.Background(), router, threadCheck)
	if err := d.NotifyMutes().SetMuted(1002, router, true); err != nil {
		t.Fatal(err)
	}
	tgc.edits, tgc.sends = nil, nil
	th.NeedHuman(context.Background(), "Починить VPN-туннель «Франкфурт» сам не смог.", "обновите агента во вкладке «Управление»")
	for _, e := range tgc.edits {
		if e.chatID == 1002 {
			t.Fatalf("заглушивший посреди починки получил правку: %+v", e)
		}
	}
	for _, s := range tgc.sends {
		if s.chatID == 1002 {
			t.Fatalf("заглушивший посреди починки получил ответ: %+v", s)
		}
	}
	if len(tgc.sends) != 1 || tgc.sends[0].chatID != 1001 {
		t.Fatalf("ответы=%+v, ждали один -- владельцу", tgc.sends)
	}
}

// Правка не удалась, а получатель тем временем заглушил роутер: полную
// тревогу заново ему не шлём.
func TestThread_FallbackSkipsMuted(t *testing.T) {
	d, router := threadSetup(t)
	tgc := &threadTG{editErr: map[int64]error{1002: errors.New("Bad Request: message to edit not found")}}
	r := newTestRepairs(d, tgc, fakeTexts{threadAlert, alertKB(), true}, nil)
	th := r.Begin(context.Background(), router, threadCheck)
	if err := d.RouterOperators().Remove(router, 1002); err != nil {
		t.Fatal(err)
	}
	th.Done(context.Background(), "Починил.")
	for _, s := range tgc.sends {
		if s.chatID == 1002 {
			t.Fatalf("снятый оператор получил тревогу заново: %+v", s)
		}
	}
}

// Telegram не примет текст длиннее 4096 знаков: длинная тревога плюс блок
// починки ужимаются за счёт тревоги, блок остаётся целым -- он и есть
// новость.
func TestThread_EditFitsTelegramLimit(t *testing.T) {
	d, router := threadSetup(t)
	tgc := &threadTG{}
	long := threadAlert + "\n" + strings.Repeat("правило «Видео» ушло мимо VPN-туннеля\n", 200)
	r := newTestRepairs(d, tgc, fakeTexts{long, alertKB(), true}, nil)
	block := "Чиню: " + strings.Repeat("перезапуск не помог · ", 40) + "сейчас: выпускаю конфиг заново…"

	r.Begin(context.Background(), router, threadCheck).Progress(context.Background(), block)

	if len(tgc.edits) != 2 {
		t.Fatalf("правок=%d", len(tgc.edits))
	}
	for _, e := range tgc.edits {
		if n := len([]rune(e.text)); n > 4000 {
			t.Fatalf("правка длиной %d знаков", n)
		}
		if !strings.HasSuffix(e.text, "\n\n"+block) {
			t.Fatalf("блок починки обрезан:\n%s", e.text[len(e.text)-200:])
		}
		if !strings.HasPrefix(e.text, threadAlert) {
			t.Fatalf("голова тревоги потерялась")
		}
	}
}

// Quiet -- нить кончилась без слов: ни правки, ни сообщения, закрытие снято.
func TestThread_QuietSaysNothingAndUncovers(t *testing.T) {
	d, router := threadSetup(t)
	tgc := &threadTG{}
	r := newTestRepairs(d, tgc, fakeTexts{threadAlert, alertKB(), true}, nil)
	r.Begin(context.Background(), router, threadCheck).Quiet(context.Background())
	if len(tgc.edits)+len(tgc.sends) != 0 {
		t.Fatalf("Quiet заговорил: %+v %+v", tgc.edits, tgc.sends)
	}
	if r.TakeCovered(router, threadCheck) {
		t.Fatal("после Quiet «восстановилось» обязано уйти")
	}
}

// Блок починки длиной 3997-3998 знаков: места под тревогу нет, а резать блок
// по maxEditRunes-1 нельзя -- он короче: срез за длину (паника или мусор).
func TestWithRepairBlock_LongBlockNoPanic(t *testing.T) {
	for _, n := range []int{3996, 3997, 3998, 3999, 4000, 4100} {
		block := strings.Repeat("ж", n)
		got := withRepairBlock("тревога", block)
		if l := len([]rune(got)); l > maxEditRunes {
			t.Fatalf("блок %d: правка длиной %d знаков", n, l)
		}
		if !strings.Contains(got, "ж") {
			t.Fatalf("блок %d: блок починки потерялся", n)
		}
		// Срез за длину внутри ёмкости не паникует, а дописывает нулевые знаки.
		if strings.ContainsRune(got, 0) {
			t.Fatalf("блок %d: в правке нулевые знаки", n)
		}
	}
}
