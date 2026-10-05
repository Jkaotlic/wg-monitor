// internal/backend/notify/thread.go
package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/linkrepair"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Editor -- правка уже отправленного сообщения. «message is not modified»
// tg.Client сам считает успехом.
type Editor interface {
	EditMessageText(ctx context.Context, chatID, messageID int64, text, parseMode string, markup *tg.InlineKeyboardMarkup) error
}

// AlertTexts -- последний текст HARD-тревоги по проверке и её кнопки. Хранит
// диспетчер в памяти: правка Telegram заменяет текст целиком, и дописать
// блок починки можно, только зная, что было в тревоге. После перезапуска
// бэкенда памяти нет -- ok=false.
type AlertTexts interface {
	Last(routerID int64, checkName string) (text string, kb *tg.InlineKeyboardMarkup, ok bool)
}

// coveredFor -- сколько после «Починил» гасится ответ «восстановилось»:
// проверка роутера приходит раз в минуту-две, порог восстановления -- пара
// отчётов, и тридцати минут хватает с запасом.
const coveredFor = 30 * time.Minute

// Repairs -- linkrepair.Reporter поверх Fanout: ход починки дописывается в
// саму тревогу (правка беззвучна), «нужно ваше участие» -- ещё и ответом со
// звуком. Безопасен для одновременных вызовов: починки разных роутеров идут
// параллельно, диспетчер спрашивает TakeCovered из своего потока.
type Repairs struct {
	fanout *Fanout
	editor Editor
	d      *db.DB
	texts  AlertTexts
	now    func() time.Time
	// sleep -- пауза перед повтором правки; прерывается ctx. Тесты
	// подменяют её, чтобы не ждать по-настоящему.
	sleep func(ctx context.Context, d time.Duration) error

	mu         sync.Mutex
	miniAppURL string
	// covered -- по каким проверкам «восстановилось» не шлётся: починка
	// идёт или кончилась «Починил» меньше 30 минут назад.
	covered map[repairKey]*coverMark
	gen     uint64
}

// coverMark -- закрытие проверки починкой.
//
// Пока починка идёт (running), проверка закрыта: лесенка после замены
// конфига ещё 30-60 с доказывает результат (свежий обмен ключами, выход,
// возврат трафика), а агент отчитывается раз в минуту -- «восстановилось»
// обычно доходит до диспетчера РАНЬШЕ «Починил». После Done закрытие живёт
// 30 минут; NeedHuman и NotStarted снимают его сразу: если VPN-туннель
// потом поднимется сам, человек обязан получить обычное «восстановилось».
//
// Закрытие одноразовое: одна тревога -- одно «восстановилось». Если
// «восстановилось» погашено, пока починка шла (taken), её Done метку уже не
// продлевает. Если же она кончится NeedHuman, хотя VPN-туннель уже
// поднялся, -- итог «сам не смог» останется единственным словом; это
// принято: ответ «восстановилось» был погашен в расчёте на починку.
type coverMark struct {
	gen     uint64 // чья метка: снимает и продлевает только её нить
	running bool
	until   time.Time
	taken   bool
}

type repairKey struct {
	routerID  int64
	checkName string
}

func NewRepairs(f *Fanout, e Editor, d *db.DB, texts AlertTexts, now func() time.Time) *Repairs {
	if now == nil {
		now = time.Now
	}
	return &Repairs{fanout: f, editor: e, d: d, texts: texts, now: now, sleep: sleepCtx, covered: map[repairKey]*coverMark{}}
}

// sleepCtx -- пауза, которую обрывает отмена ctx.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// SetMiniAppBaseURL -- адрес мини-аппа для кнопки под «нужно ваше участие».
// Пустой (или не https) -- ответ уходит без кнопки.
func (r *Repairs) SetMiniAppBaseURL(base string) {
	r.mu.Lock()
	r.miniAppURL = base
	r.mu.Unlock()
}

// TakeCovered -- одноразовый вопрос диспетчера при «восстановилось»: true --
// итог уже скажет (или сказала) починка, ответ не шлётся, и метка
// погашена: следующее «восстановилось» той же проверки уйдёт как обычно.
func (r *Repairs) TakeCovered(routerID int64, checkName string) bool {
	key := repairKey{routerID, checkName}
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.covered[key]
	if !ok {
		return false
	}
	switch {
	case m.running && !m.taken:
		// Починка ещё идёт: метка остаётся, чтобы её Done знал, что
		// «восстановилось» уже погашено, и не продлевал закрытие.
		m.taken = true
		return true
	case !m.running && r.now().Before(m.until):
		delete(r.covered, key)
		return true
	}
	if !m.running {
		delete(r.covered, key)
	}
	return false
}

// Uncover -- новая HARD-тревога той же проверки: прежнее закрытие к ней
// не относится. Вызывает диспетчер.
func (r *Repairs) Uncover(routerID int64, checkName string) {
	r.mu.Lock()
	delete(r.covered, repairKey{routerID, checkName})
	r.mu.Unlock()
}

// Begin вызывается движком синхронно, под замком роутера, поэтому здесь
// только чтение базы и памяти -- ни одного похода в Telegram. Номера
// сообщений тревоги снимаются один раз: «восстановилось» может очистить
// таблицу посреди починки, а итог всё равно обязан дописаться в тревогу.
//
// С Begin проверка закрыта (см. coverMark). Если по ней уже идёт починка
// (вторая попытка запуска, которая кончится NotStarted «уже идёт починка»),
// новая нить меткой не владеет и чужое закрытие не снимает.
func (r *Repairs) Begin(_ context.Context, routerID int64, checkName string) linkrepair.Thread {
	key := repairKey{routerID, checkName}
	r.mu.Lock()
	var gen uint64
	if m, ok := r.covered[key]; !ok || !m.running {
		r.gen++
		gen = r.gen
		r.covered[key] = &coverMark{gen: gen, running: true}
	}
	appURL := tg.MiniAppRouterURL(r.miniAppURL, routerID)
	r.mu.Unlock()

	th := &repairThread{r: r, key: key, gen: gen, appURL: appURL}
	if r.texts != nil {
		th.base, th.kb, th.hasBase = r.texts.Last(routerID, checkName)
	}
	msgs, err := r.d.AlertMessages().List(routerID, checkName)
	if err != nil {
		r.warn("починка: номера сообщений тревоги не прочитались", "router_user_id", routerID, "check", checkName, "err", err)
	}
	// Снимок -- только тем, кто и сейчас получатель: заглушивший роутер
	// (админ кнопкой «Не писать мне», оператор) и снятый оператор правок
	// больше не получают. Таблица alert_messages от выключения не чистится.
	cur := th.recipients()
	for chatID, mid := range msgs {
		if cur != nil && !cur[chatID] {
			continue
		}
		th.targets = append(th.targets, threadTarget{chatID: chatID, messageID: mid})
	}
	// Порядок получателей -- по номеру чата: правки идут предсказуемо.
	sort.Slice(th.targets, func(i, j int) bool { return th.targets[i].chatID < th.targets[j].chatID })
	return th
}

// finish -- нить кончилась. done=true -- «Починил»: закрытие на 30 минут,
// если «восстановилось» ещё не погашено; иначе метка снимается. gen=0 --
// нить меткой не владеет.
func (r *Repairs) finish(key repairKey, gen uint64, done bool) {
	if gen == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.covered[key]
	if !ok || m.gen != gen {
		return
	}
	if done && !m.taken {
		m.running = false
		m.until = r.now().Add(coveredFor)
		return
	}
	delete(r.covered, key)
}

func (r *Repairs) warn(msg string, args ...any) {
	if r.fanout != nil && r.fanout.logger != nil {
		r.fanout.logger.Warn(msg, args...)
	}
}

// repairThread -- одна починка глазами людей. Вызовы идут из одной горутины
// движка по очереди, поэтому своего замка у нити нет.
type repairThread struct {
	r       *Repairs
	key     repairKey
	gen     uint64
	appURL  string
	base    string
	kb      *tg.InlineKeyboardMarkup
	hasBase bool
	targets []threadTarget
}

// threadTarget -- кому и какое сообщение править. messageID меняется, когда
// правка не удалась и получателю ушло новое сообщение: дальше правится оно.
type threadTarget struct {
	chatID    int64
	messageID int64
}

// recipients -- кто получатель роутера сейчас (RecipientsFor: минус
// заглушившие и снятые). Перечитывается на каждый вызов нити: выключить
// уведомления можно и посреди починки. Не прочиталось -- nil, и шлём всем из
// снимка: итог починки потерять хуже, чем один раз написать лишнему.
func (t *repairThread) recipients() map[int64]bool {
	ids, err := RecipientsFor(t.r.d, t.key.routerID, t.r.fanout.adminID)
	if err != nil {
		t.r.warn("починка: получатели не прочитались", "router_user_id", t.key.routerID, "check", t.key.checkName, "err", err)
		return nil
	}
	set := make(map[int64]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// editable -- есть что править: текст тревоги в памяти и номера сообщений.
// Иначе нить работает новыми сообщениями (см. say).
func (t *repairThread) editable() bool {
	return t.hasBase && strings.TrimSpace(t.base) != "" && len(t.targets) > 0
}

// Progress -- ход починки. Без тревоги, которую можно править, молчит:
// каждая ступень новым сообщением -- это спам, а итог (Done/NeedHuman/
// NotStarted) всё равно придёт.
func (t *repairThread) Progress(ctx context.Context, text string) {
	if !t.editable() {
		return
	}
	t.edit(ctx, text, t.recipients())
}

func (t *repairThread) Done(ctx context.Context, text string) {
	t.r.finish(t.key, t.gen, true)
	t.say(ctx, text)
}

func (t *repairThread) NotStarted(ctx context.Context, why string) {
	t.r.finish(t.key, t.gen, false)
	t.say(ctx, "Автопочинка не запускалась: "+strings.TrimRight(strings.TrimSpace(why), ".")+".")
}

// NeedHuman -- итог правкой и, если человеку есть что сделать, ответ со
// звуком на тревогу: правка беззвучна, и то, ради чего человек нужен, не
// может жить только в ней.
func (t *repairThread) NeedHuman(ctx context.Context, text, action string) {
	t.r.finish(t.key, t.gen, false)
	action = strings.TrimRight(strings.TrimSpace(action), ".")
	if !t.editable() {
		if action == "" {
			t.send(ctx, text)
			return
		}
		// Править нечего: итог и действие -- одним новым сообщением, оно и
		// так со звуком.
		msg := text + "\n\nНужно ваше участие: " + action + "."
		if _, err := t.r.fanout.SendKeyboard(ctx, t.key.routerID, msg, "", t.appKeyboard()); err != nil {
			t.r.warn("починка: «нужно участие» не доставлено", "router_user_id", t.key.routerID, "check", t.key.checkName, "err", err)
		}
		return
	}
	cur := t.recipients()
	t.edit(ctx, text, cur)
	if action == "" {
		return
	}
	reply := needHumanHead(text) + " Нужно ваше участие: " + action + "."
	kb := t.appKeyboard()
	for i := range t.targets {
		mid := t.targets[i].messageID
		chatID := t.targets[i].chatID
		if cur != nil && !cur[chatID] {
			continue
		}
		if _, err := t.r.fanout.sendOne(ctx, chatID, t.key.routerID, reply, "", &mid, kb); err != nil {
			t.r.fanout.noteFailure(chatID, t.key.routerID, err)
			continue
		}
		t.r.fanout.noteSuccess(chatID)
	}
}

// say -- итог: правкой, если есть что править, иначе новым сообщением всем
// получателям роутера.
func (t *repairThread) say(ctx context.Context, text string) {
	if t.editable() {
		t.edit(ctx, text, t.recipients())
		return
	}
	t.send(ctx, text)
}

func (t *repairThread) send(ctx context.Context, text string) {
	if _, err := t.r.fanout.Send(ctx, t.key.routerID, text, ""); err != nil {
		t.r.warn("починка: сообщение не доставлено", "router_user_id", t.key.routerID, "check", t.key.checkName, "err", err)
	}
}

// edit дописывает блок под тревогу каждому получателю с теми же кнопками,
// что были под ней (админу -- с его рядом «Не писать мне»). Блок заменяет
// прежний: текст хода от движка уже несёт всё сделанное. cur -- получатели
// сейчас (nil -- неизвестно): кто выпал из них, тому ни правки, ни нового
// сообщения.
//
// Неудачная правка разбирается по причине (см. tryEdit): временная (429,
// 5xx, обрыв сети) повторяется с паузой; сообщения больше нет или чат
// закрыт -- получателю уходит новое сообщение, дальше правится оно; прочее
// (наша ошибка в тексте) -- только в журнал. Громкий дубль всей тревоги из-за
// флуд-контроля Telegram хуже пропущенной правки: следующий ход починки
// поправит сообщение снова.
func (t *repairThread) edit(ctx context.Context, block string, cur map[int64]bool) {
	full := withRepairBlock(t.base, block)
	f := t.r.fanout
	budget := editRetryBudget
	for i := range t.targets {
		tgt := &t.targets[i]
		if cur != nil && !cur[tgt.chatID] {
			continue
		}
		kb := t.kb
		if f.isAdmin(tgt.chatID) {
			kb = withAdminMuteRow(kb, t.key.routerID)
		}
		err := t.tryEdit(ctx, tgt, full, kb, &budget)
		if err == nil {
			continue
		}
		if editErrorKind(err) != editGone {
			t.r.warn("починка: тревога не поправлена, повтор на следующем ходе",
				"telegram_user_id", tgt.chatID, "message_id", tgt.messageID, "check", t.key.checkName, "err", err)
			continue
		}
		t.r.warn("починка: тревога не правится, шлём новым сообщением",
			"telegram_user_id", tgt.chatID, "message_id", tgt.messageID, "check", t.key.checkName, "err", err)
		// sendOne сам добавит админу ряд выключения -- передаём исходные кнопки.
		mid, sendErr := f.sendOne(ctx, tgt.chatID, t.key.routerID, full, "", nil, t.kb)
		if sendErr != nil {
			f.noteFailure(tgt.chatID, t.key.routerID, sendErr)
			continue
		}
		f.noteSuccess(tgt.chatID)
		tgt.messageID = mid
	}
}

// editRetryBudget -- сколько всего одна правка (все получатели вместе)
// может ждать повторов. Движок починки ждёт нить: дольше -- и ход починки
// встаёт из-за флуд-контроля Telegram.
const editRetryBudget = 30 * time.Second

// editBackoffStart -- первая пауза, когда Telegram не сказал, сколько ждать.
const editBackoffStart = time.Second

// tryEdit правит одно сообщение, повторяя временные ошибки, пока хватает
// общего запаса ожидания budget. Возвращает последнюю ошибку.
func (t *repairThread) tryEdit(ctx context.Context, tgt *threadTarget, full string, kb *tg.InlineKeyboardMarkup, budget *time.Duration) error {
	backoff := editBackoffStart
	for {
		err := t.r.editor.EditMessageText(ctx, tgt.chatID, tgt.messageID, full, "", kb)
		if err == nil || editErrorKind(err) != editTransient {
			return err
		}
		wait := backoff
		if d, ok := tg.RateLimitDelay(err); ok && d > 0 {
			wait = d
		} else {
			backoff *= 2
		}
		if wait > *budget {
			return err
		}
		*budget -= wait
		if sErr := t.r.sleep(ctx, wait); sErr != nil {
			return err
		}
	}
}

type editKind int

const (
	editOther     editKind = iota // наша ошибка в тексте/кнопках -- повтор не поможет
	editTransient                 // 429, 5xx, сеть -- повторить с паузой
	editGone                      // сообщения нет или чат закрыт -- новое сообщение
)

// editErrorKind -- что делать с ошибкой правки.
func editErrorKind(err error) editKind {
	if err == nil {
		return editOther
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return editOther
	}
	var ae *tg.APIError
	if !errors.As(err, &ae) {
		if messageGone(err.Error()) {
			return editGone
		}
		// Не ответ Telegram, а обрыв по дороге -- временно.
		return editTransient
	}
	switch {
	case ae.Code == http.StatusTooManyRequests || ae.Code >= 500:
		return editTransient
	case tg.IsUnreachableChat(err) || messageGone(ae.Description):
		return editGone
	}
	return editOther
}

// messageGone -- Telegram говорит, что править нечего: сообщение удалено,
// не найдено или старше срока правки.
func messageGone(desc string) bool {
	d := strings.ToLower(desc)
	return strings.Contains(d, "message to edit not found") ||
		strings.Contains(d, "message can't be edited") ||
		strings.Contains(d, "message_id_invalid")
}

// maxEditRunes -- потолок правки с запасом до лимита Telegram в 4096 знаков.
const maxEditRunes = 4000

// withRepairBlock -- тревога и блок починки одним текстом. Не влезает в
// лимит -- ужимается тревога (хвост, голова остаётся), блок целым: он и
// есть новость. Блок сам длиннее лимита (не бывает: лесенка короткая) --
// режется и он.
func withRepairBlock(base, block string) string {
	const sep, cut = "\n\n", "…"
	full := base + sep + block
	if utf8.RuneCountInString(full) <= maxEditRunes {
		return full
	}
	room := maxEditRunes - utf8.RuneCountInString(sep+block) - utf8.RuneCountInString(cut)
	if room <= 0 {
		// Блок в 3997-3998 знаков короче среза: резать по своей длине.
		b := []rune(block)
		return string(b[:min(len(b), maxEditRunes-1)]) + cut
	}
	head := []rune(base)
	if len(head) > room {
		head = head[:room]
	}
	return strings.TrimRight(string(head), " \n") + cut + sep + block
}

// appKeyboard -- одна кнопка «открыть роутер в приложении»; адреса нет --
// без кнопки.
func (t *repairThread) appKeyboard() *tg.InlineKeyboardMarkup {
	if t.appURL == "" {
		return nil
	}
	return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		{Text: "📱 Открыть в приложении", WebApp: &tg.WebAppInfo{URL: t.appURL}},
	}}}
}

// needHumanHead -- первая фраза громкого ответа. Имя VPN-туннеля движок
// уже вписал в итог («Починить VPN-туннель «X» сам не смог…»): берём его
// оттуда, а не из идентификатора проверки, которого владелец нигде не видел.
func needHumanHead(text string) string {
	const prefix = "Починить VPN-туннель «"
	if rest, ok := strings.CutPrefix(text, prefix); ok {
		if i := strings.Index(rest, "» сам не смог"); i > 0 {
			return fmt.Sprintf("%s%s» сам не смог.", prefix, rest[:i])
		}
	}
	return "Починить VPN-туннель сам не смог."
}

var _ linkrepair.Reporter = (*Repairs)(nil)
