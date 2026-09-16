import { Overlay } from '../ui/Overlay.jsx'
import { Chip } from '../ui/Chip.jsx'
import { sortByUrgency, fleetRow, batchProgress } from '../fleet.js'
import { useFleetRecheck } from '../useFleetRecheck.js'

// Список роутеров: сломанное сверху, состояние -- словами. Открывается только
// у того, кому доступен не один роутер -- владельцу одного показывать список
// незачем.
//
// Пять точек и легенда под ними удалены: строка обязана отвечать сама, а не
// отправлять человека к расшифровке цветов внизу экрана.
export function FleetOverlay({ routers, currentID, onPick, onClose }) {
  const rows = sortByUrgency(routers).map(fleetRow)
  const broken = rows.filter((r) => r.pill.tone === 'danger').length
  const { batch, recheckAll } = useFleetRecheck(routers)

  return (
    <Overlay title="Мои роутеры" onBack={onClose}>
      <div class="screen">
        <h1 class="screen-title">Мои роутеры</h1>
        <p class="router-lastseen">
          {broken === 0
            ? `Все ${rows.length} в порядке.`
            : `Сломанное сверху: ${broken} из ${rows.length} требуют внимания.`}
        </p>

        <div class="card">
          {rows.map((r) => (
            <button
              key={r.id}
              type="button"
              class={`fleet-row${r.id === currentID ? ' fleet-row-current' : ''}`}
              onClick={() => onPick(r.id)}
            >
              <span class="fleet-main">
                <span class="fleet-name">
                  <span class="row-title">{r.nickname}</span>
                  <Chip tone={r.pill.tone}>{r.pill.text}</Chip>
                </span>
                <span class="fleet-sub">{r.sub}</span>
              </span>
              <svg class="list-row-chevron" viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M 6 3 L 11 8 L 6 13" />
              </svg>
            </button>
          ))}
        </div>

        {rows.length > 1 && (
          <>
            <button type="button" class="btn btn-ghost btn-wide" disabled={batch?.running} onClick={recheckAll}>
              {batch?.running ? 'Опрашиваем…' : 'Опросить все'}
            </button>
            <p class="hint">
              {batchProgress(batch) ||
                'Каждый роутер переспросит себя сам. Ничего не меняет; спящие ответят, когда проснутся.'}
            </p>
          </>
        )}
      </div>
    </Overlay>
  )
}
