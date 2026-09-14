package backend

import (
	"context"

	"github.com/Jkaotlic/wg-monitor/internal/backend/upstream"
)

// dashboardUpdateRow -- «этому роутеру пора обновиться, и вот до чего».
//
// Отдаётся только в операторскую сводку дашборда: там читатель один -- админ,
// у которого и без того есть весь парк. В /v1/miniapp/* эта форма не уезжает --
// у него свой срез, собранный поимённо по белому списку полей.
type dashboardUpdateRow struct {
	Component string `json:"component"`
	Name      string `json:"name"`
	Installed string `json:"installed"`
	Available string `json:"available"`
}

// dashboardRouterUpdates дописывает в сводку новости об обновлениях по каждому
// роутеру.
//
// Источник -- снимок версий в базе, а не поход в GitHub по каждому роутеру:
// лимит анонимного API 60 запросов в час, и парк сжёг бы его за минуты.
// Сравнение идёт тем же ComputeUpdates, что у экрана мини-аппа и у панели в
// боте -- иначе три поверхности разошлись бы в том, что считать обновлением.
//
// Причины «неизвестно» здесь не рисуются: сводка отвечает на вопрос «кому пора
// обновляться», а «почему мы не знаем» админ читает на экране роутера. Пустой
// список поэтому означает «новостей нет», а не «всё актуально».
//
// Снимок читается одним запросом на весь парк (строк столько же, сколько
// роутеров); в горячие events за этим лезть не надо вовсе.
func dashboardRouterUpdates(ctx context.Context, d Deps, agents []dashboardSummaryAgent) {
	if d.DB == nil {
		return
	}
	snaps, err := d.DB.RouterVersions().All()
	if err != nil {
		if d.Logger != nil {
			d.Logger.Warn("dashboard: чтение снимков версий не удалось", "err", err)
		}
		return
	}
	for i := range agents {
		row, ok := snaps[agents[i].ID]
		if !ok || row.UpdatedAt.IsZero() {
			continue
		}
		updates, _ := upstream.ComputeUpdates(ctx, d.Upstream, VersionAuditFromSnapshot(row))
		for _, u := range updates {
			agents[i].Updates = append(agents[i].Updates, dashboardUpdateRow{
				Component: u.Component,
				Name:      u.Name,
				Installed: u.Installed,
				Available: u.Available,
			})
		}
	}
}
