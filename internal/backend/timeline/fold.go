// Пакет timeline превращает поток проверок в происшествия.
//
// Каждый отчёт агента пишет строку на КАЖДУЮ проверку (handler.go, INSERT OR
// IGNORE в цикле по rep.Checks), а не только на изменение состояния. При
// интервале в 60 секунд это порядка 8 600 строк в сутки на роутер. Показывать
// их человеку нельзя, и обрезать до пятисот -- тоже: он получит последний час
// под заголовком «7 дней».
//
// Единица здесь -- происшествие: «обход блокировок не работал с 09:05 до
// 09:09». Оно отвечает на вопрос, с которым открывают экран: что я
// почувствовал и как долго это длилось.
package timeline

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// resolverGuardCheck mirrors internal/backend's own resolverGuardCheck
// constant (handler.go). Duplicated rather than imported: this package sits
// below backend (backend imports timeline, not the reverse), so it copies
// the one string it needs instead of creating a cycle.
const resolverGuardCheck = "resolver_guard"

// heartbeatCheck -- сердцебиение агента: каждый полный отчёт несёт его, как бы
// ни кончились остальные проверки.
const heartbeatCheck = "agent_heartbeat"

// FlapGap -- тишина, после которой новое падение считается новой новостью, а
// не продолжением прежней. Короче -- один вечер разваливается на десяток
// строк; длиннее -- два разных отвала слипаются, и человек не видит, что
// упало дважды.
const FlapGap = 30 * time.Minute

// MaxIncidents -- потолок выдачи. Он стоит на происшествиях, а не на строках:
// потолок в строках и есть та поломка, из-за которой экран за неделю
// показывал час.
const MaxIncidents = 100

// Incident -- одна поломка глазами человека. У идущей поломки To нулевое:
// конец, которого ещё не было, нельзя записать временем.
type Incident struct {
	CheckName string
	From      time.Time
	To        time.Time
	DownSec   int
	Flaps     int
	Ongoing   bool

	// v0.47: серии пингчека awg-manager (timeline.FoldWithAwgm). Нули --
	// серий нет или агент старый.
	AwgmFirstFail time.Time // первая неудача awg-manager внутри происшествия
	AwgmFails     int       // сколько проб awg-manager провалил за происшествие
	AwgmWentDown  bool      // awg-manager признал VPN-туннель упавшим
	AwgmOnly      bool      // видел только awg-manager: моргнул между отчётами
	AwgmClean     bool      // журнал читался, а awg-manager связь не терял
}

// Fold сворачивает строки событий в происшествия. Вход принимается в любом
// порядке и не мутируется; наружу идут свежие вперёд.
func Fold(rows []db.EventRow, now time.Time) []Incident {
	byCheck := make(map[string][]db.EventRow)
	for _, r := range rows {
		byCheck[r.CheckName] = append(byCheck[r.CheckName], r)
	}

	var heartbeats []time.Time
	for _, r := range byCheck[heartbeatCheck] {
		heartbeats = append(heartbeats, r.TS)
	}
	sort.Slice(heartbeats, func(i, j int) bool { return heartbeats[i].Before(heartbeats[j]) })

	var out []Incident
	for check, list := range byCheck {
		sorted := make([]db.EventRow, len(list))
		copy(sorted, list)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].TS.Before(sorted[j].TS) })
		var goneAt time.Time
		if check == resolverGuardCheck {
			goneAt = watchdogGoneAt(sorted, heartbeats)
		}
		out = append(out, foldCheck(check, sorted, now, goneAt)...)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].From.After(out[j].From) })
	if len(out) > MaxIncidents {
		out = out[:MaxIncidents]
	}
	return out
}

// foldCheck работает по одной проверке: пара ищется внутри одного имени, иначе
// dns и hydraroute слиплись бы в одну поломку.
func foldCheck(check string, sorted []db.EventRow, now, goneAt time.Time) []Incident {
	var raw []Incident
	var cur *Incident
	for _, e := range sorted {
		if e.Status == "ok" {
			// A resolver_guard "ok" whose details say the watchdog has not
			// read its settings yet is not an answer about DNS -- the report
			// handler (handler.go, resolverGuardNotReady) skips such rows
			// for the state-machine FSM too, so an open incident here must
			// not close on one either: it would split one outage into two
			// around a report that said nothing.
			if check == resolverGuardCheck && resolverGuardRowNotReady(e) {
				continue
			}
			if cur != nil {
				cur.To = e.TS
				cur.DownSec = int(e.TS.Sub(cur.From).Seconds())
				raw = append(raw, *cur)
				cur = nil
			}
			continue
		}
		// Любой не-ok открывает происшествие: fail, warn и unknown одинаково
		// означают «сейчас это не работает».
		if cur == nil {
			cur = &Incident{CheckName: check, From: e.TS, Flaps: 1}
		}
	}
	if cur != nil {
		if goneAt.IsZero() {
			cur.Ongoing = true
			cur.DownSec = int(now.Sub(cur.From).Seconds())
		} else {
			cur.To = goneAt
			cur.DownSec = int(goneAt.Sub(cur.From).Seconds())
		}
		raw = append(raw, *cur)
	}
	return mergeFlaps(raw)
}

// watchdogGoneAt: сторож выключили правкой файла (или его настройки сломались)
// посреди аварии -- проверка пропала из отчётов, а отчёты идут. Идущая авария,
// которую уже никто не обновит, висела бы в ленте вечно; бэкенд закрывает её
// тревогу по тому же признаку (clearMissingResolverGuardHard). Концом
// считается первый отчёт без проверки; нулевое время -- проверка на месте.
func watchdogGoneAt(guardRows []db.EventRow, heartbeats []time.Time) time.Time {
	if len(guardRows) == 0 {
		return time.Time{}
	}
	last := guardRows[len(guardRows)-1].TS
	for _, hb := range heartbeats {
		if hb.After(last) {
			return hb
		}
	}
	return time.Time{}
}

// resolverGuardRowNotReady parses details_json for "ready": false. Unparsable
// details_json (old agent, empty string) is treated as a normal row -- ready
// is reported false only, never assumed.
func resolverGuardRowNotReady(e db.EventRow) bool {
	if e.DetailsJSON == "" {
		return false
	}
	var d struct {
		Ready *bool `json:"ready"`
	}
	if err := json.Unmarshal([]byte(e.DetailsJSON), &d); err != nil {
		return false
	}
	return d.Ready != nil && !*d.Ready
}

// mergeFlaps склеивает соседние происшествия одной проверки, между которыми
// меньше FlapGap тишины. DownSec складывается по падениям, а не считается от
// начала до конца: между морганиями связь работала.
func mergeFlaps(raw []Incident) []Incident {
	if len(raw) == 0 {
		return nil
	}
	out := []Incident{raw[0]}
	for _, inc := range raw[1:] {
		prev := &out[len(out)-1]
		if !prev.Ongoing && inc.From.Sub(prev.To) < FlapGap {
			prev.To = inc.To
			prev.DownSec += inc.DownSec
			prev.Flaps += inc.Flaps
			prev.Ongoing = inc.Ongoing
			continue
		}
		out = append(out, inc)
	}
	return out
}
