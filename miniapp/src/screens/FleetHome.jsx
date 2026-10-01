import { Chip } from '../ui/Chip.jsx'
import { FleetCounts } from '../ui/FleetCounts.jsx'
import { fleetSummary, fleetSummaryLine, batchProgress } from '../fleet.js'
import { useFleetRecheck } from '../useFleetRecheck.js'

// Роутер не выбран на широком экране: сводка вместо пустоты. Три числа --
// ответ на «всё ли в порядке», карточки -- куда идти, если нет.
//
// Парк здесь больше не живёт (v0.48): у админа это своя вкладка, в боковой
// колонке -- «Парк». Туда же уехал его «Проверить заново все»; не-админу с
// несколькими роутерами кнопка остаётся здесь.
export function FleetHome({ routers, onPick, isAdmin = false }) {
  const s = fleetSummary(routers)
  const { batch, recheckAll } = useFleetRecheck(routers)
  return (
    <div class="screen fleet-home">
      <h1 class="screen-title">Роутеры</h1>
      <FleetCounts summary={s} />

      {s.broken.length === 0 ? (
        <p class="state fleet-home-calm">{fleetSummaryLine(s)}</p>
      ) : (
        <section class="section">
          <h2 class="section-title">Требуют внимания</h2>
          <div class="fleet-cards">
            {s.broken.map((r) => (
              <button key={r.id} type="button" class="fleet-card" onClick={() => onPick(r.id)}>
                <span class="fleet-card-head">
                  <span class="row-title">{r.nickname}</span>
                  <Chip tone={r.pill.tone}>{r.pill.text}</Chip>
                </span>
                <span class="fleet-card-sub">{r.sub}</span>
              </button>
            ))}
          </div>
        </section>
      )}

      {s.total > 1 && !isAdmin && (
        <section class="section fleet-home-recheck">
          <button type="button" class="btn btn-ghost" disabled={batch?.running} onClick={recheckAll}>
            {batch?.running ? 'Проверяем…' : 'Проверить заново все'}
          </button>
          <p class="hint">
            {batchProgress(batch) || 'Каждый роутер переспросит себя сам. Ничего не меняет; спящие ответят, когда проснутся.'}
          </p>
        </section>
      )}
    </div>
  )
}
