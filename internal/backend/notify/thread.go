// internal/backend/notify/thread.go
package notify

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

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
// параллельно, диспетчер спрашивает Covered из своего потока.
type Repairs struct {
	fanout *Fanout
	editor Editor
	d      *db.DB
	texts  AlertTexts
	now    func() time.Time

	mu         sync.Mutex
	miniAppURL string
	// covered -- до какого времени «восстановилось» по проверке не шлётся.
	covered map[repairKey]time.Time
}

type repairKey struct {
	routerID  int64
	checkName string
}

func NewRepairs(f *Fanout, e Editor, d *db.DB, texts AlertTexts, now func() time.Time) *Repairs {
	if now == nil {
		now = time.Now
	}
	return &Repairs{fanout: f, editor: e, d: d, texts: texts, now: now, covered: map[repairKey]time.Time{}}
}

// SetMiniAppBaseURL -- адрес мини-аппа для кнопки под «нужно ваше участие».
// Пустой (или не https) -- ответ уходит без кнопки.
func (r *Repairs) SetMiniAppBaseURL(base string) {
	r.mu.Lock()
	r.miniAppURL = base
	r.mu.Unlock()
}

// Covered -- починка закрыла эту проверку в последние 30 минут: итог уже
// дописан в тревогу, и второй ответ «восстановилось» был бы шумом.
func (r *Repairs) Covered(routerID int64, checkName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.covered[repairKey{routerID, checkName}]
	if !ok {
		return false
	}
	if !r.now().Before(until) {
		delete(r.covered, repairKey{routerID, checkName})
		return false
	}
	return true
}

// Begin вызывается движком синхронно, под замком роутера, поэтому здесь
// только чтение базы и памяти -- ни одного похода в Telegram. Номера
// сообщений тревоги снимаются один раз: «восстановилось» может очистить
// таблицу посреди починки, а итог всё равно обязан дописаться в тревогу.
//
// Новая починка проверки -- новый случай: прежнее «починил» его
// «восстановилось» не гасит.
func (r *Repairs) Begin(_ context.Context, routerID int64, checkName string) linkrepair.Thread {
	key := repairKey{routerID, checkName}
	r.mu.Lock()
	delete(r.covered, key)
	appURL := tg.MiniAppRouterURL(r.miniAppURL, routerID)
	r.mu.Unlock()

	th := &repairThread{r: r, key: key, appURL: appURL}
	if r.texts != nil {
		th.base, th.kb, th.hasBase = r.texts.Last(routerID, checkName)
	}
	msgs, err := r.d.AlertMessages().List(routerID, checkName)
	if err != nil {
		r.warn("починка: номера сообщений тревоги не прочитались", "router_user_id", routerID, "check", checkName, "err", err)
	}
	for chatID, mid := range msgs {
		th.targets = append(th.targets, threadTarget{chatID: chatID, messageID: mid})
	}
	// Порядок получателей -- по номеру чата: правки идут предсказуемо.
	sort.Slice(th.targets, func(i, j int) bool { return th.targets[i].chatID < th.targets[j].chatID })
	return th
}

func (r *Repairs) markCovered(key repairKey) {
	r.mu.Lock()
	r.covered[key] = r.now().Add(coveredFor)
	r.mu.Unlock()
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
	t.edit(ctx, text)
}

func (t *repairThread) Done(ctx context.Context, text string) {
	t.r.markCovered(t.key)
	t.say(ctx, text)
}

func (t *repairThread) NotStarted(ctx context.Context, why string) {
	t.say(ctx, "Автопочинка не запускалась: "+strings.TrimRight(strings.TrimSpace(why), ".")+".")
}

// NeedHuman -- итог правкой и, если человеку есть что сделать, ответ со
// звуком на тревогу: правка беззвучна, и то, ради чего человек нужен, не
// может жить только в ней.
func (t *repairThread) NeedHuman(ctx context.Context, text, action string) {
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
	t.edit(ctx, text)
	if action == "" {
		return
	}
	reply := needHumanHead(text) + " Нужно ваше участие: " + action + "."
	kb := t.appKeyboard()
	for i := range t.targets {
		mid := t.targets[i].messageID
		chatID := t.targets[i].chatID
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
		t.edit(ctx, text)
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
// прежний: текст хода от движка уже несёт всё сделанное. Правка не удалась
// (сообщение удалено, старше лимита Telegram) -- этому получателю уходит
// новое сообщение, и дальше правится уже оно.
func (t *repairThread) edit(ctx context.Context, block string) {
	full := t.base + "\n\n" + block
	f := t.r.fanout
	for i := range t.targets {
		tgt := &t.targets[i]
		kb := t.kb
		if f.isAdmin(tgt.chatID) {
			kb = withAdminMuteRow(kb, t.key.routerID)
		}
		err := t.r.editor.EditMessageText(ctx, tgt.chatID, tgt.messageID, full, "", kb)
		if err == nil {
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
