import { Chip } from '../ui/Chip.jsx'
import { fleetSummary, batchProgress } from '../fleet.js'
import { useFleetRecheck } from '../useFleetRecheck.js'
import { ParkSection } from './ParkSection.jsx'

// Роутер не выбран на широком экране: сводка вместо пустоты. Три числа --
// ответ на «всё ли в порядке», карточки -- куда идти, если нет.
//
// Админу под сводкой -- Парк целиком: он про весь флот, а не про один
// роутер, и ждать выбора роутера, чтобы до него добраться, незачем.
// «Парк» в боковой колонке при пустом выборе ведёт сюда (id="park").
export function FleetHome({ routers, onPick, isAdmin = false, openSheet, openLayer, onOpenConnection }) {
  const s = fleetSummary(routers)
  const { batch, recheckAll } = useFleetRecheck(routers)
  return (
    <div class="screen fleet-home">
      <h1 class="screen-title">Роутеры</h1>
      <div class="fleet-counts">
        <div class="fleet-count fleet-count-ok">
          <span class="fleet-count-value">{s.ok}</span>
          <span class="fleet-count-label">в порядке</span>
        </div>
        <div class="fleet-count fleet-count-attention">
          <span class="fleet-count-value">{s.attention}</span>
          <span class="fleet-count-label">требуют внимания</span>
        </div>
        <div class="fleet-count fleet-count-silent">
          <span class="fleet-count-value">{s.silent}</span>
          <span class="fleet-count-label">молчат</span>
        </div>
      </div>

      {s.broken.length === 0 ? (
        <p class="state fleet-home-calm">Все {s.total} в порядке.</p>
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

      {s.total > 1 && (
        <section class="section fleet-home-recheck">
          <button type="button" class="btn btn-ghost" disabled={batch?.running} onClick={recheckAll}>
            {batch?.running ? 'Опрашиваем…' : 'Опросить все'}
          </button>
          <p class="hint">
            {batchProgress(batch) || 'Каждый роутер переспросит себя сам. Ничего не меняет; спящие ответят, когда проснутся.'}
          </p>
        </section>
      )}

      {isAdmin && (
        <div class="fleet-home-park" id="park">
          <ParkSection openSheet={openSheet} onOpenRouter={onPick} currentID={null} openLayer={openLayer} onOpenConnection={onOpenConnection} />
        </div>
      )}
    </div>
  )
}
