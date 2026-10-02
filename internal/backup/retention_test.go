package backup

import (
	"slices"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// dailyNames -- по одному архиву в 02:00 UTC за каждый из days дней до last включительно.
func dailyNames(kind Kind, last time.Time, days int) []string {
	var out []string
	for i := 0; i < days; i++ {
		d := last.AddDate(0, 0, -i)
		out = append(out, ArchiveName(kind, time.Date(d.Year(), d.Month(), d.Day(), 2, 0, 0, 0, time.UTC)))
	}
	return out
}

func kept(all, deleted []string) []string {
	var out []string
	for _, n := range all {
		if !slices.Contains(deleted, n) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

func names(kind Kind, stamps ...string) []string {
	var out []string
	for _, s := range stamps {
		out = append(out, "wg-monitor-"+string(kind)+"-backup-"+s+".tgz.enc")
	}
	slices.Sort(out)
	return out
}

func TestArchiveNameRoundTrip(t *testing.T) {
	at := mustTime(t, "2026-10-02T02:03:04Z")
	if got := ArchiveName(KindSmall, at.In(time.FixedZone("MSK", 3*3600))); got != "wg-monitor-small-backup-20261002T020304Z.tgz.enc" {
		t.Fatalf("имя малого: %s", got)
	}
	if got := ArchiveName(KindFull, at); got != "wg-monitor-full-backup-20261002T020304Z.tgz.enc" {
		t.Fatalf("имя полного: %s", got)
	}
	kind, ts, ok := ParseArchiveName("wg-monitor-small-backup-20261002T020304Z.tgz.enc")
	if !ok || kind != KindSmall || !ts.Equal(at) {
		t.Fatalf("разбор: %v %v %v", kind, ts, ok)
	}
	for _, bad := range []string{
		"wg-monitor-full-backup-20261002T020304Z.tgz",
		"wg-monitor-full-backup-20261002T020304Z.tgz.enc.partial",
		"wg-monitor-tiny-backup-20261002T020304Z.tgz.enc",
		"wg-monitor-full-backup-20261302T020304Z.tgz.enc", // 13-й месяц
		"x-wg-monitor-full-backup-20261002T020304Z.tgz.enc",
		"dir/wg-monitor-full-backup-20261002T020304Z.tgz.enc",
		"",
	} {
		if _, _, ok := ParseArchiveName(bad); ok {
			t.Errorf("имя %q принято за архив", bad)
		}
	}
}

func TestRetentionSevenDailyFourWeekly(t *testing.T) {
	now := mustTime(t, "2026-10-07T03:00:00Z") // среда
	all := dailyNames(KindSmall, now, 60)
	del := SelectForDeletion(KindSmall, all, now, Retention{KeepDaily: 7, KeepWeekly: 4})
	want := names(KindSmall,
		// семь дневных
		"20261007T020000Z", "20261006T020000Z", "20261005T020000Z", "20261004T020000Z",
		"20261003T020000Z", "20261002T020000Z", "20261001T020000Z",
		// предыдущие недели: воскресенье 4.10 уже среди дневных, дальше 27.09, 20.09, 13.09
		"20260927T020000Z", "20260920T020000Z", "20260913T020000Z",
	)
	if got := kept(all, del); !slices.Equal(got, want) {
		t.Fatalf("оставлено:\n%v\nждали:\n%v", got, want)
	}
	if len(del)+len(want) != len(all) {
		t.Fatalf("удалено %d + оставлено %d != всего %d", len(del), len(want), len(all))
	}
}

func TestRetentionFullDefaultKeepsThreeDaily(t *testing.T) {
	if got := DefaultRetention(KindSmall); got != (Retention{KeepDaily: 7, KeepWeekly: 4}) {
		t.Fatalf("умолчание малого: %+v", got)
	}
	if got := DefaultRetention(KindFull); got != (Retention{KeepDaily: 3, KeepWeekly: 0}) {
		t.Fatalf("умолчание полного: %+v", got)
	}
	now := mustTime(t, "2026-10-07T03:00:00Z")
	all := dailyNames(KindFull, now, 30)
	del := SelectForDeletion(KindFull, all, now, DefaultRetention(KindFull))
	want := names(KindFull, "20261007T020000Z", "20261006T020000Z", "20261005T020000Z")
	if got := kept(all, del); !slices.Equal(got, want) {
		t.Fatalf("оставлено %v, ждали %v", got, want)
	}
}

func TestRetentionWeekBoundaryIsMondayUTC(t *testing.T) {
	now := mustTime(t, "2026-10-07T03:00:00Z") // неделя с понедельника 5.10
	all := names(KindSmall,
		"20261005T000000Z", // понедельник 00:00:00 -- уже текущая неделя
		"20261004T235959Z", // воскресенье 23:59:59 -- предыдущая
		"20261004T020000Z",
		"20260928T000000Z", // понедельник предыдущей недели
		"20260927T235959Z", // позапрошлая неделя
	)
	// Дневных не оставляем вовсе, кроме обязательного самого свежего.
	del := SelectForDeletion(KindSmall, all, now, Retention{KeepDaily: 0, KeepWeekly: 1})
	want := names(KindSmall, "20261005T000000Z", "20261004T235959Z")
	if got := kept(all, del); !slices.Equal(got, want) {
		t.Fatalf("оставлено %v, ждали %v", got, want)
	}
	del = SelectForDeletion(KindSmall, all, now, Retention{KeepDaily: 0, KeepWeekly: 2})
	want = names(KindSmall, "20261005T000000Z", "20261004T235959Z", "20260927T235959Z")
	if got := kept(all, del); !slices.Equal(got, want) {
		t.Fatalf("оставлено %v, ждали %v", got, want)
	}
}

func TestRetentionISOWeekAcrossNewYear(t *testing.T) {
	// 1–3 января 2027 принадлежат 53-й неделе 2026 года.
	now := mustTime(t, "2027-01-06T03:00:00Z")
	all := dailyNames(KindSmall, now, 20)
	del := SelectForDeletion(KindSmall, all, now, Retention{KeepDaily: 1, KeepWeekly: 2})
	want := names(KindSmall, "20270106T020000Z", "20270103T020000Z", "20261227T020000Z")
	if got := kept(all, del); !slices.Equal(got, want) {
		t.Fatalf("оставлено %v, ждали %v", got, want)
	}
}

func TestRetentionSameDayKeepsNewest(t *testing.T) {
	now := mustTime(t, "2026-10-07T23:00:00Z")
	all := names(KindSmall, "20261007T020000Z", "20261007T120000Z", "20261007T120001Z", "20261006T020000Z")
	// Дубликаты в списке ничего не меняют.
	del := SelectForDeletion(KindSmall, append(append([]string{}, all...), all...), now, Retention{KeepDaily: 2})
	want := names(KindSmall, "20261007T120001Z", "20261006T020000Z")
	if got := kept(all, del); !slices.Equal(got, want) {
		t.Fatalf("оставлено %v, ждали %v", got, want)
	}
	if len(del) != 2 {
		t.Fatalf("удаляемые должны быть без повторов: %v", del)
	}
}

func TestRetentionKeepsFilesFromTheFuture(t *testing.T) {
	// Часы Pi без батарейки после перезагрузки показывают прошлое: все
	// архивы «из будущего». Ни один не удаляется и квоту они не занимают.
	now := mustTime(t, "2026-10-07T03:00:00Z")
	future := names(KindSmall, "20261101T020000Z", "20261102T020000Z", "20270101T020000Z")
	past := dailyNames(KindSmall, now, 5)
	all := append(append([]string{}, future...), past...)
	del := SelectForDeletion(KindSmall, all, now, Retention{KeepDaily: 2})
	want := append(append([]string{}, future...), names(KindSmall, "20261007T020000Z", "20261006T020000Z")...)
	slices.Sort(want)
	if got := kept(all, del); !slices.Equal(got, want) {
		t.Fatalf("оставлено %v, ждали %v", got, want)
	}
	// Часы в 1970-м: всё из будущего, удалять нечего.
	if del := SelectForDeletion(KindSmall, all, time.Unix(0, 0), Retention{KeepDaily: 1}); len(del) != 0 {
		t.Fatalf("при сбитых часах удалено %v", del)
	}
}

func TestRetentionNeverTouchesForeignFiles(t *testing.T) {
	now := mustTime(t, "2026-10-07T03:00:00Z")
	foreign := []string{
		"wg-monitor-full-backup-20260101T020000Z.tgz.enc", // другой вид
		"wg-monitor-small-backup-20260101T020000Z.tgz.enc.partial",
		"wg-monitor-small-backup-20260101T020000Z.tgz",
		"wg-monitor-small-backup-latest.tgz.enc",
		"wg-monitor-small-backup-20260101T020000Z.tgz.enc.bak",
		"backup-status.json",
		"state.db",
		"notes.txt",
		".tmp-wg-monitor-backup.123",
		"../wg-monitor-small-backup-20260101T020000Z.tgz.enc",
		"sub/wg-monitor-small-backup-20260101T020000Z.tgz.enc",
	}
	all := append(append([]string{}, foreign...), dailyNames(KindSmall, now, 10)...)
	del := SelectForDeletion(KindSmall, all, now, Retention{KeepDaily: 1})
	if len(del) != 9 {
		t.Fatalf("ждали 9 удаляемых, получили %v", del)
	}
	for _, d := range del {
		if slices.Contains(foreign, d) {
			t.Fatalf("под удаление попал чужой файл %q", d)
		}
		if kind, _, ok := ParseArchiveName(d); !ok || kind != KindSmall {
			t.Fatalf("под удаление попало не имя малого архива: %q", d)
		}
	}
}

func TestRetentionNeverDeletesNewestAndHandlesEmpty(t *testing.T) {
	now := mustTime(t, "2026-10-07T03:00:00Z")
	if del := SelectForDeletion(KindSmall, nil, now, Retention{}); len(del) != 0 {
		t.Fatalf("пустой список: %v", del)
	}
	all := dailyNames(KindSmall, now, 3)
	del := SelectForDeletion(KindSmall, all, now, Retention{KeepDaily: 0, KeepWeekly: 0})
	if got := kept(all, del); !slices.Equal(got, names(KindSmall, "20261007T020000Z")) {
		t.Fatalf("самый свежий архив обязан остаться: %v", got)
	}
	// Отрицательные значения -- как ноль, а не «удалить всё».
	del = SelectForDeletion(KindSmall, all, now, Retention{KeepDaily: -5, KeepWeekly: -1})
	if got := kept(all, del); !slices.Equal(got, names(KindSmall, "20261007T020000Z")) {
		t.Fatalf("отрицательные значения: %v", got)
	}
	// После долгого простоя дневные -- это дни, когда архивы были, а не календарь.
	old := dailyNames(KindSmall, mustTime(t, "2026-07-01T03:00:00Z"), 5)
	del = SelectForDeletion(KindSmall, old, now, Retention{KeepDaily: 3})
	if got := kept(old, del); len(got) != 3 {
		t.Fatalf("после простоя должно остаться 3 архива, осталось %v", got)
	}
}
