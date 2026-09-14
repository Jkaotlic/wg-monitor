package callbacks

import (
	"context"
	"log/slog"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/upstream"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// updatesFromCacheOrSnapshot -- какие обновления показать в умном ответе бота.
//
// Свежий ответ роутера главнее: он рассказывает про «сейчас». Но его может не
// быть вовсе -- кэш версий в памяти бота (удалён в цикле 1) умирал с
// рестартом бэкенда. Раньше на этом блок «🟡 Доступны обновления» просто
// пустел до следующего нажатия «Сверить версии», и молчание читалось как «всё
// актуально». Снимок в базе рестарт переживает, поэтому он и есть запасной
// источник.
//
// Пустой снимок (роутер ещё ни разу не отчитался) -- это ответ «новостей нет»,
// а не повод падать: сравнивать попросту не с чем.
//
// Причины «неизвестно» здесь не рисуются намеренно: умный ответ -- разговор о
// поломке, и отсутствие блока в нём не читается как «всё актуально». Про
// незнание словами говорят экраны, которые человек открыл сам.
func updatesFromCacheOrSnapshot(
	ctx context.Context,
	d *db.DB,
	up *upstream.Cache,
	cached wire.VersionAudit,
	haveCached bool,
	userID int64,
) []alerts.UpdateAvailable {
	va := cached
	if !haveCached {
		if d == nil {
			return nil
		}
		row, err := d.RouterVersions().Get(userID)
		if err != nil {
			slog.Warn("smart reply: чтение снимка версий не удалось", "user_id", userID, "err", err)
			return nil
		}
		if row.UpdatedAt.IsZero() {
			return nil
		}
		va = backend.VersionAuditFromSnapshot(row)
	}
	return computeUpdates(ctx, up, va)
}
