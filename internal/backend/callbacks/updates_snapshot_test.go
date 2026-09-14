package callbacks

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Блок «Доступны обновления» в умном ответе пустел после каждого рестарта
// бэкенда.
//
// Версии жили только в кэше на пользователя в памяти бота (удалён в цикле 1),
// кэш умирал с процессом, и до следующего нажатия «Сверить версии» бот об
// обновлениях молчал. Молчание при этом выглядело как «всё актуально».
// Снимок в базе рестарт переживает -- значит и блок обязан.
func TestUpdatesFallBackToSnapshotWhenCacheIsCold(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	uid, err := d.Users().Insert("router-a", "tok-snap-000000000000000000000000000000000", "198.51.100.10", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	// Прошивку приносит сам роутер, поэтому новость о ней собирается и с
	// выключенным источником апстрима -- как на парке сегодня.
	if err := d.RouterVersions().Upsert(uid, db.RouterVersionSnapshot{
		AwgmgrVersion:   "2.18.2+r2",
		FirmwareCurrent: "5.02.A.8.0-3",
		FirmwareAvail:   "5.02.A.9.0-0",
		Source:          "report",
	}); err != nil {
		t.Fatal(err)
	}

	// Кэш пуст -- ровно как сразу после рестарта.
	updates := updatesFromCacheOrSnapshot(context.Background(), d, nil, wire.VersionAudit{}, false, uid)
	var found bool
	for _, u := range updates {
		if u.Name == "KeeneticOS" && u.Available == "5.02.A.9.0-0" {
			found = true
		}
	}
	if !found {
		t.Errorf("после рестарта блок обновлений пуст, хотя снимок в базе есть: %+v", updates)
	}
}

// Свежий ответ роутера главнее снимка: он рассказывает про «сейчас», а снимок
// про «когда рассказывали в прошлый раз».
func TestUpdatesPreferFreshAuditOverSnapshot(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	uid, err := d.Users().Insert("router-a", "tok-snap-000000000000000000000000000000000", "198.51.100.10", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RouterVersions().Upsert(uid, db.RouterVersionSnapshot{
		FirmwareCurrent: "5.02.A.8.0-3",
		FirmwareAvail:   "5.02.A.9.0-0",
		Source:          "report",
	}); err != nil {
		t.Fatal(err)
	}

	// Роутер только что сказал, что прошивка уже поставлена: новости нет.
	fresh := wire.VersionAudit{FirmwareCurrent: "5.02.A.9.0-0"}
	updates := updatesFromCacheOrSnapshot(context.Background(), d, nil, fresh, true, uid)
	for _, u := range updates {
		if u.Name == "KeeneticOS" {
			t.Errorf("свежий ответ роутера проигнорирован в пользу снимка: %+v", u)
		}
	}
}

// Снимка нет вовсе -- новостей нет, и выдумывать их не из чего. Падать на
// этом тоже нельзя: роутер, который ещё ни разу не отчитался, -- нормальное
// состояние.
func TestUpdatesWithoutSnapshotAreEmptyNotPanic(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	uid, err := d.Users().Insert("router-a", "tok-snap-000000000000000000000000000000000", "198.51.100.10", "awg11")
	if err != nil {
		t.Fatal(err)
	}

	if updates := updatesFromCacheOrSnapshot(context.Background(), d, nil, wire.VersionAudit{}, false, uid); len(updates) != 0 {
		t.Errorf("без снимка новости выдуманы: %+v", updates)
	}
}
