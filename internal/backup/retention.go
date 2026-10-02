package backup

import (
	"regexp"
	"slices"
	"time"
)

// Kind -- вид ночного архива.
type Kind string

const (
	// KindSmall -- всё для восстановления системы, кроме истории событий;
	// уходит админу в Telegram.
	KindSmall Kind = "small"
	// KindFull -- вся база с историей; остаётся на диске и на внешней цели.
	KindFull Kind = "full"
)

const archiveStampLayout = "20060102T150405Z"

var archiveNameRe = regexp.MustCompile(`^wg-monitor-(small|full)-backup-(\d{8}T\d{6}Z)\.tgz\.enc$`)

// ArchiveName -- имя файла архива: wg-monitor-<вид>-backup-<время UTC>.tgz.enc.
func ArchiveName(kind Kind, at time.Time) string {
	return "wg-monitor-" + string(kind) + "-backup-" + at.UTC().Format(archiveStampLayout) + ".tgz.enc"
}

// ParseArchiveName разбирает имя файла (без каталога). ok=false -- это не
// архив wg-monitor: такие файлы хранение не трогает никогда.
func ParseArchiveName(name string) (kind Kind, at time.Time, ok bool) {
	m := archiveNameRe.FindStringSubmatch(name)
	if m == nil {
		return "", time.Time{}, false
	}
	at, err := time.Parse(archiveStampLayout, m[2])
	if err != nil {
		return "", time.Time{}, false
	}
	return Kind(m[1]), at.UTC(), true
}

// Retention -- правило хранения архивов одного вида.
type Retention struct {
	// KeepDaily -- за сколько последних дней (UTC), в которые архивы есть,
	// оставлять по одному самому свежему архиву.
	KeepDaily int
	// KeepWeekly -- за сколько последних недель ISO (понедельник--воскресенье,
	// UTC) до текущей оставлять по одному самому свежему архиву.
	KeepWeekly int
}

// DefaultRetention: малый -- 7 дневных и 4 недельных; полный -- 3 дневных
// (он большой, а лежит на той же карте, что и база).
func DefaultRetention(kind Kind) Retention {
	if kind == KindFull {
		return Retention{KeepDaily: 3, KeepWeekly: 0}
	}
	return Retention{KeepDaily: 7, KeepWeekly: 4}
}

// SelectForDeletion решает, какие из файлов names (имена без каталога) пора
// удалить. Чистая функция: диск не трогает.
//
// Рассматриваются только имена архивов вида kind; всё прочее не возвращается
// никогда. Время архива берётся из имени, а не из mtime. Остаются:
//   - самый свежий архив -- всегда, даже при нулях в правиле;
//   - самый свежий архив за каждый из KeepDaily последних дней, в которые
//     архивы есть (после простоя «последние дни» -- те, что были, а не календарь);
//   - самый свежий архив за каждую из KeepWeekly последних недель ISO,
//     предшествующих неделе now, в которые архивы есть;
//   - архивы «из будущего» (время в имени позже now): часы могли сбиться,
//     такие файлы не удаляются и места в правиле не занимают.
func SelectForDeletion(kind Kind, names []string, now time.Time, r Retention) []string {
	type archive struct {
		name string
		at   time.Time
	}
	now = now.UTC()
	seen := map[string]bool{}
	var past []archive
	for _, name := range names {
		k, at, ok := ParseArchiveName(name)
		if !ok || k != kind || seen[name] {
			continue
		}
		seen[name] = true
		if at.After(now) {
			continue
		}
		past = append(past, archive{name: name, at: at})
	}
	if len(past) == 0 {
		return nil
	}
	// От свежих к старым: первый встреченный в дне или неделе -- самый свежий.
	slices.SortFunc(past, func(a, b archive) int { return b.at.Compare(a.at) })

	keep := map[string]bool{past[0].name: true}

	days := map[string]bool{}
	for _, a := range past {
		day := a.at.Format("20060102")
		if days[day] {
			continue
		}
		if len(days) >= r.KeepDaily {
			break
		}
		days[day] = true
		keep[a.name] = true
	}

	type week struct{ year, week int }
	var current week
	current.year, current.week = now.ISOWeek()
	weeks := map[week]bool{}
	for _, a := range past {
		var w week
		w.year, w.week = a.at.ISOWeek()
		if w == current || weeks[w] {
			continue
		}
		if len(weeks) >= r.KeepWeekly {
			break
		}
		weeks[w] = true
		keep[a.name] = true
	}

	var del []string
	for _, a := range past {
		if !keep[a.name] {
			del = append(del, a.name)
		}
	}
	slices.Sort(del)
	return del
}
