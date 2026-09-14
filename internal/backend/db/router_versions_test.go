package db

import (
	"path/filepath"
	"testing"
)

func newTestDBForVersions(t *testing.T) (*DB, int64) {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	id, err := d.Users().Insert("router-a", "tok-a", "198.51.100.10", "awg11")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	return d, id
}

func TestRouterVersionsUpsertMovesPrevOnlyOnChange(t *testing.T) {
	d, uid := newTestDBForVersions(t)
	r := d.RouterVersions()
	if err := r.Upsert(uid, RouterVersionSnapshot{AwgmgrVersion: "2.17.2", KmodVersion: "1.0.0", Source: "report"}); err != nil {
		t.Fatal(err)
	}
	// Тот же отчёт ещё раз: «было» обязано остаться пустым, иначе первое же
	// повторение затрёт историю собой.
	if err := r.Upsert(uid, RouterVersionSnapshot{AwgmgrVersion: "2.17.2", KmodVersion: "1.0.0", Source: "report"}); err != nil {
		t.Fatal(err)
	}
	row, err := r.Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.PrevAwgmgrVersion != "" || row.ChangedAt != nil {
		t.Errorf("повтор той же версии сдвинул историю: prev=%q changed_at=%v", row.PrevAwgmgrVersion, row.ChangedAt)
	}

	if err := r.Upsert(uid, RouterVersionSnapshot{AwgmgrVersion: "2.18.0", KmodVersion: "1.1.0", Source: "report"}); err != nil {
		t.Fatal(err)
	}
	row, err = r.Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.PrevAwgmgrVersion != "2.17.2" || row.PrevKmodVersion != "1.0.0" {
		t.Errorf("«было» = %q/%q, хотим 2.17.2/1.0.0", row.PrevAwgmgrVersion, row.PrevKmodVersion)
	}
	if row.AwgmgrVersion != "2.18.0" || row.KmodVersion != "1.1.0" {
		t.Errorf("«стало» = %q/%q, хотим 2.18.0/1.1.0", row.AwgmgrVersion, row.KmodVersion)
	}
	if row.ChangedAt == nil {
		t.Error("changed_at не проставлен при смене версии")
	}
}

// version_audit знает hrneo, обычный отчёт -- нет. Смешивать их надо
// дополнением: иначе отчёт стирал бы версию HydraRoute Neo каждые полторы минуты.
func TestRouterVersionsUpsertDoesNotWipeFieldsAbsentInSource(t *testing.T) {
	d, uid := newTestDBForVersions(t)
	r := d.RouterVersions()
	yes := true
	if err := r.Upsert(uid, RouterVersionSnapshot{
		AwgmgrVersion: "2.17.2", HrneoVersion: "2.4.0", HrneoInstalled: &yes,
		FirmwareAvail: "4.3.8", FirmwareChannel: "release", Source: "version_audit",
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Upsert(uid, RouterVersionSnapshot{
		AwgmgrVersion: "2.17.2", KeeneticOS: "4.3.7", KmodVersion: "1.0.0", Source: "report",
	}); err != nil {
		t.Fatal(err)
	}
	row, err := r.Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.HrneoVersion != "2.4.0" {
		t.Errorf("hrneo_version = %q: отчёт затёр то, чего не знает", row.HrneoVersion)
	}
	if row.HrneoInstalled == nil || !*row.HrneoInstalled {
		t.Errorf("hrneo_installed = %v: отчёт затёр то, чего не знает", row.HrneoInstalled)
	}
	if row.FirmwareAvail != "4.3.8" || row.FirmwareChannel != "release" {
		t.Errorf("доступная прошивка потеряна: avail=%q channel=%q", row.FirmwareAvail, row.FirmwareChannel)
	}
	// А то, что отчёт знает, обязано доехать.
	if row.KeeneticOS != "4.3.7" || row.KmodVersion != "1.0.0" {
		t.Errorf("отчёт не дополнил снимок: os=%q kmod=%q", row.KeeneticOS, row.KmodVersion)
	}
	if row.Source != "report" {
		t.Errorf("source = %q, хотим report -- последний писавший", row.Source)
	}
}

// Старый агент не сообщает про модуль ядра. Это «неизвестно», а не «не загружен».
func TestRouterVersionsKeepsKmodLoadedNullForOldAgent(t *testing.T) {
	d, uid := newTestDBForVersions(t)
	if err := d.RouterVersions().Upsert(uid, RouterVersionSnapshot{AwgmgrVersion: "2.17.2", Source: "report"}); err != nil {
		t.Fatal(err)
	}
	row, err := d.RouterVersions().Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.KmodLoaded != nil {
		t.Errorf("kmod_loaded = %v, хотим nil (неизвестно)", *row.KmodLoaded)
	}
}

// «Не загружен» -- это ответ, и он обязан пережить базу, не превратившись в
// «неизвестно». Иначе выключенный модуль ядра выглядел бы как старый агент.
func TestRouterVersionsKmodLoadedFalseSurvivesAsFalse(t *testing.T) {
	d, uid := newTestDBForVersions(t)
	no := false
	if err := d.RouterVersions().Upsert(uid, RouterVersionSnapshot{
		AwgmgrVersion: "2.17.2", KmodLoaded: &no, Source: "report",
	}); err != nil {
		t.Fatal(err)
	}
	row, err := d.RouterVersions().Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.KmodLoaded == nil {
		t.Fatal("kmod_loaded = nil: «не загружен» превратился в «неизвестно»")
	}
	if *row.KmodLoaded {
		t.Error("kmod_loaded = true, хотим false")
	}
	// Следующий отчёт без поля не имеет права стереть известное в «неизвестно».
	if err := d.RouterVersions().Upsert(uid, RouterVersionSnapshot{AwgmgrVersion: "2.17.2", Source: "report"}); err != nil {
		t.Fatal(err)
	}
	row, _ = d.RouterVersions().Get(uid)
	if row.KmodLoaded == nil {
		t.Error("отчёт без поля стёр известное состояние модуля ядра")
	}
}

// Снимок живёт в базе, а не в памяти: кэш на пользователя умирал с
// рестартом (maint_audit_cache.go:22-35), и блок обновлений пустел.
// Проверяем через повторное открытие той же базы, затем удаление роутера.
func TestRouterVersionsSurvivesRestartAndCascades(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := d.Users().Insert("router-a", "tok-a", "198.51.100.10", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	if err := d.RouterVersions().Upsert(uid, RouterVersionSnapshot{
		AwgmgrVersion: "2.17.2", KmodVersion: "1.0.0", KmodLoaded: &yes, Source: "report",
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	// Рестарт бэкенда: та же база, миграция накатывается повторно.
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("повторное открытие базы: %v", err)
	}
	defer d2.Close()
	row, err := d2.RouterVersions().Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.AwgmgrVersion != "2.17.2" || row.KmodVersion != "1.0.0" {
		t.Errorf("снимок не пережил рестарт: %+v", row)
	}
	if row.KmodLoaded == nil || !*row.KmodLoaded {
		t.Errorf("kmod_loaded не пережил рестарт: %v", row.KmodLoaded)
	}
	if row.UpdatedAt.IsZero() {
		t.Error("updated_at пустой: непонятно, когда смотрели")
	}

	all, err := d2.RouterVersions().All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[uid].AwgmgrVersion != "2.17.2" {
		t.Errorf("All() = %+v, хотим один роутер с 2.17.2", all)
	}

	// Удалили роутер -- снимок уходит каскадом, осиротевших строк не остаётся.
	if _, err := d2.SQL().Exec(`DELETE FROM users WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	all, err = d2.RouterVersions().All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("после удаления роутера остался снимок: %+v", all)
	}
}

// Снимка нет -- это ответ «не знаем», а не ошибка: экран обязан отрисовать
// «неизвестно», а не пятисотку.
func TestRouterVersionsGetMissingIsEmptyNotError(t *testing.T) {
	d, uid := newTestDBForVersions(t)
	row, err := d.RouterVersions().Get(uid)
	if err != nil {
		t.Fatalf("Get без строки вернул ошибку: %v", err)
	}
	if !row.UpdatedAt.IsZero() || row.AwgmgrVersion != "" {
		t.Errorf("хотим пустой снимок, получили %+v", row)
	}
}
