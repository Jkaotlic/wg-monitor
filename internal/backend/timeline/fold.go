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
	"sort"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

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
}

// Fold сворачивает строки событий в происшествия. Вход принимается в любом
// порядке и не мутируется; наружу идут свежие вперёд.
func Fold(rows []db.EventRow, now time.Time) []Incident {
	byCheck := make(map[string][]db.EventRow)
	for _, r := range rows {
		byCheck[r.CheckName] = append(byCheck[r.CheckName], r)
	}

	var out []Incident
	for check, list := range byCheck {
		sorted := make([]db.EventRow, len(list))
		copy(sorted, list)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].TS.Before(sorted[j].TS) })
		out = append(out, foldCheck(check, sorted, now)...)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].From.After(out[j].From) })
	if len(out) > MaxIncidents {
		out = out[:MaxIncidents]
	}
	return out
}

// foldCheck работает по одной проверке: пара ищется внутри одного имени, иначе
// dns и hydraroute слиплись бы в одну поломку.
func foldCheck(check string, sorted []db.EventRow, now time.Time) []Incident {
	var raw []Incident
	var cur *Incident
	for _, e := range sorted {
		if e.Status == "ok" {
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
		cur.Ongoing = true
		cur.DownSec = int(now.Sub(cur.From).Seconds())
		raw = append(raw, *cur)
	}
	return mergeFlaps(raw)
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
