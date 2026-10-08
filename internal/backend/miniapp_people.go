package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// Справочник людей (v0.58): админ выдаёт доступ выбором человека из списка,
// а не вводом Telegram ID, который человек нигде не видит.

// MiniappPeopleTG -- getChat Bot API для дотягивания имён (*tg.Client).
// Интерфейсом -- чтобы тесты ходили в фейковый Telegram.
type MiniappPeopleTG interface {
	GetChat(ctx context.Context, chatID int64) (tg.ChatInfo, error)
}

var _ MiniappPeopleTG = (*tg.Client)(nil)

const (
	// miniappPeopleBackfillLimit -- не больше стольких getChat за один запрос
	// списка: список отдаётся сразу, остальные дотянутся следующими запросами.
	miniappPeopleBackfillLimit = 10
	// miniappPeopleRecheckAfter -- повтор getChat для того же номера, если
	// прошлая попытка имени не дала (бот не знает чат).
	miniappPeopleRecheckAfter = 24 * time.Hour
	// miniappPeopleBackfillBudget -- на все getChat одного фонового прохода.
	miniappPeopleBackfillBudget = 5 * time.Second
	// miniappPeopleWaitingWindow -- «ждёт доступа»: без доступа нигде и писал
	// за это время. Такие идут первыми.
	miniappPeopleWaitingWindow = 7 * 24 * time.Hour
)

type miniappPersonRouter struct {
	ID       int64  `json:"id"`
	Nickname string `json:"nickname"`
	Role     string `json:"role"` // owner | operator
}

type miniappPerson struct {
	TelegramUserID int64   `json:"telegram_user_id"`
	Name           string  `json:"name"`         // «имя фамилия» или ""
	Username       string  `json:"username"`     // без @ или ""
	LastSeenAt     *string `json:"last_seen_at"` // RFC3339 UTC; null -- не видели
	IsAdmin        bool    `json:"is_admin"`
	// Routers -- роли по всему парку; пустой массив -- доступа нет нигде.
	Routers []miniappPersonRouter `json:"routers"`
}

type miniappPeopleResp struct {
	People []miniappPerson `json:"people"`
}

// miniappRecordPerson -- человек вошёл в мини-апп. Ошибка базы вход не
// ломает: справочник -- подсказка админу, не путь доступа.
func miniappRecordPerson(d Deps, u miniappInitDataUser) {
	if d.DB == nil {
		return
	}
	if err := d.DB.People().Seen(u.ID, u.FirstName, u.LastName, u.Username, db.PersonSourceMiniapp, miniappNow()); err != nil && d.Logger != nil {
		d.Logger.Warn("miniapp session: справочник людей не записан", "telegram_user_id", u.ID, "err", err)
	}
}

func personHasName(p db.Person) bool {
	return p.FirstName != "" || p.LastName != "" || p.Username != ""
}

// miniappPeopleHandler -- GET /v1/miniapp/people: все, кого бэкенд знает по
// номеру (справочник ∪ владельцы ∪ операторы ∪ админ), с ролями по парку.
// Только админ, отказ как у соседних маршрутов доступа. Список отдаётся
// сразу; имена без подписи дотягиваются в фоне и видны со следующего открытия.
func miniappPeopleHandler(d Deps, bf *miniappPeopleBackfill) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := miniappRequireAdmin(d, w, r); !ok {
			return
		}
		now := miniappNow()
		resp, due, err := buildMiniappPeople(d, now)
		if err != nil {
			if d.Logger != nil {
				d.Logger.Warn("miniapp people: список не собран", "err", err)
			}
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "people lookup failed")
			return
		}
		bf.start(d, due)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// buildMiniappPeople собирает список и заодно -- номера, которым пора
// дотянуть имя (due).
func buildMiniappPeople(d Deps, now time.Time) (miniappPeopleResp, []int64, error) {
	routers, err := d.DB.Users().GetAll()
	if err != nil {
		return miniappPeopleResp{}, nil, err
	}
	ops, err := d.DB.RouterOperators().ListAll()
	if err != nil {
		return miniappPeopleResp{}, nil, err
	}
	stored, err := d.DB.People().List()
	if err != nil {
		return miniappPeopleResp{}, nil, err
	}

	// Роли по парку. Владелец и оператор одного роутера разом -- «владелец».
	nick := make(map[int64]string, len(routers))
	roles := map[int64][]miniappPersonRouter{}
	ownerOf := map[[2]int64]bool{}
	for _, u := range routers {
		nick[u.ID] = u.Nickname
		if u.TelegramUserID != nil && *u.TelegramUserID > 0 {
			tgid := *u.TelegramUserID
			roles[tgid] = append(roles[tgid], miniappPersonRouter{ID: u.ID, Nickname: u.Nickname, Role: "owner"})
			ownerOf[[2]int64{u.ID, tgid}] = true
		}
	}
	for _, op := range ops {
		if op.TelegramUserID <= 0 || ownerOf[[2]int64{op.UserID, op.TelegramUserID}] {
			continue
		}
		roles[op.TelegramUserID] = append(roles[op.TelegramUserID], miniappPersonRouter{ID: op.UserID, Nickname: nick[op.UserID], Role: "operator"})
	}
	for _, rs := range roles {
		sort.Slice(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
	}

	// Известные номера: те, кому доступ уже выдан, и админ.
	known := map[int64]bool{}
	for tgid := range roles {
		known[tgid] = true
	}
	if d.TelegramAdminUserID > 0 {
		known[d.TelegramAdminUserID] = true
	}

	people := make(map[int64]db.Person, len(stored)+len(known))
	for _, p := range stored {
		people[p.TelegramUserID] = p
	}
	for tgid := range known {
		if _, ok := people[tgid]; !ok {
			people[tgid] = db.Person{TelegramUserID: tgid}
		}
	}

	due := miniappPeopleDue(known, people, now)

	out := make([]miniappPerson, 0, len(people))
	for tgid, p := range people {
		mp := miniappPerson{
			TelegramUserID: tgid,
			Name:           strings.TrimSpace(p.FirstName + " " + p.LastName),
			Username:       strings.TrimPrefix(p.Username, "@"),
			IsAdmin:        miniappIsAdmin(tgid, d.TelegramAdminUserID),
			Routers:        roles[tgid],
		}
		if mp.Routers == nil {
			mp.Routers = []miniappPersonRouter{}
		}
		if p.LastSeenAt != nil {
			s := p.LastSeenAt.UTC().Format(time.RFC3339)
			mp.LastSeenAt = &s
		}
		out = append(out, mp)
	}
	sortMiniappPeople(out, people, now)
	return miniappPeopleResp{People: out}, due, nil
}

// miniappPeopleDue -- известные номера без имени, которым пора getChat: не
// больше miniappPeopleBackfillLimit, для номера -- не чаще раза в сутки.
func miniappPeopleDue(known map[int64]bool, people map[int64]db.Person, now time.Time) []int64 {
	var due []int64
	for tgid := range known {
		p := people[tgid]
		if personHasName(p) {
			continue
		}
		if p.NameCheckedAt != nil && now.Sub(*p.NameCheckedAt) < miniappPeopleRecheckAfter {
			continue
		}
		due = append(due, tgid)
	}
	sort.Slice(due, func(i, j int) bool { return due[i] < due[j] })
	if len(due) > miniappPeopleBackfillLimit {
		due = due[:miniappPeopleBackfillLimit]
	}
	return due
}

// miniappPeopleBackfill -- фоновое дотягивание имён getChat. Один на мукс,
// как вопросы агенту у /tunnels: проход идёт не дольше budget, и пока он
// идёт, новые открытия списка второй не запускают.
type miniappPeopleBackfill struct {
	running sync.Mutex // TryLock: занято -- проход уже идёт
	wg      sync.WaitGroup
	budget  time.Duration
	now     func() time.Time
}

func newMiniappPeopleBackfill() *miniappPeopleBackfill {
	return &miniappPeopleBackfill{budget: miniappPeopleBackfillBudget, now: miniappNow}
}

// start запускает проход в фоне и сразу возвращается. Без Telegram-клиента,
// без номеров или при идущем проходе -- ничего.
func (b *miniappPeopleBackfill) start(d Deps, due []int64) {
	if b == nil || d.PeopleTG == nil || len(due) == 0 {
		return
	}
	if !b.running.TryLock() {
		return
	}
	parent := d.ShutdownCtx
	if parent == nil {
		parent = context.Background()
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer b.running.Unlock()
		ctx, cancel := context.WithTimeout(parent, b.budget)
		defer cancel()
		b.run(ctx, d, due)
	}()
}

// wait -- дождаться идущего прохода (тесты).
func (b *miniappPeopleBackfill) wait() { b.wg.Wait() }

// run -- getChat по номерам. Ошибка Telegram -- не беда: номер остаётся без
// имени, попытка отмечается. Отмена контекста попыткой не считается.
func (b *miniappPeopleBackfill) run(ctx context.Context, d Deps, due []int64) {
	for _, tgid := range due {
		info, err := d.PeopleTG.GetChat(ctx, tgid)
		if ctx.Err() != nil {
			return
		}
		now := b.now()
		if err != nil || (info.FirstName == "" && info.LastName == "" && info.Username == "") {
			if derr := d.DB.People().MarkNameChecked(tgid, now); derr != nil && d.Logger != nil {
				d.Logger.Warn("miniapp people: попытка getChat не отмечена", "telegram_user_id", tgid, "err", derr)
			}
			continue
		}
		if derr := d.DB.People().SetNameFromTelegram(tgid, info.FirstName, info.LastName, info.Username, now); derr != nil && d.Logger != nil {
			d.Logger.Warn("miniapp people: имя из getChat не записано", "telegram_user_id", tgid, "err", derr)
		}
	}
}

// sortMiniappPeople -- сначала ждущие доступа (нет доступа нигде, писали за
// 7 дней), потом по last_seen_at убыванию, без даты -- в конце по номеру.
func sortMiniappPeople(out []miniappPerson, people map[int64]db.Person, now time.Time) {
	rank := func(mp miniappPerson) int {
		seen := people[mp.TelegramUserID].LastSeenAt
		switch {
		case seen == nil:
			return 2
		case len(mp.Routers) == 0 && !mp.IsAdmin && now.Sub(*seen) <= miniappPeopleWaitingWindow:
			return 0
		default:
			return 1
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank(out[i]), rank(out[j])
		if ri != rj {
			return ri < rj
		}
		if ri < 2 {
			si := people[out[i].TelegramUserID].LastSeenAt
			sj := people[out[j].TelegramUserID].LastSeenAt
			if !si.Equal(*sj) {
				return si.After(*sj)
			}
		}
		return out[i].TelegramUserID < out[j].TelegramUserID
	})
}
