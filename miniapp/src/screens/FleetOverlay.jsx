import { Overlay } from '../ui/Overlay.jsx'
import { Chip } from '../ui/Chip.jsx'
import { PanelLine } from '../ui/PanelLine.jsx'
import { ParkSection } from './ParkSection.jsx'
import { FleetFilterBar } from '../ui/FleetFilterBar.jsx'
import { sortByUrgency, fleetRow, batchProgress } from '../fleet.js'
import { emptyFilterText } from '../fleetFilter.js'
import { useFleetFilter } from '../useFleetFilter.js'
import { useFleetRecheck } from '../useFleetRecheck.js'

// Список роутеров: сломанное сверху, состояние -- словами. Открывается только
// у того, кому доступен не один роутер -- владельцу одного показывать список
// незачем.
//
// Пять точек и легенда под ними удалены: строка обязана отвечать сама, а не
// отправлять человека к расшифровке цветов внизу экрана.
//
// Поиск и фильтры сужают только список; заголовок и «Опросить все» говорят
// про весь парк -- иначе «все в порядке» читалось бы про отфильтрованных.
//
// Админу под списком -- Парк (v0.41): он про весь флот, и держать его в
// «Обслуживании» одного роутера значило искать его не там. Список поэтому
// открывается админу и при одном роутере.
export function FleetOverlay({ routers, currentID, onPick, onClose, shortcut = true, isAdmin = false, openSheet, openLayer, onOpenConnection }) {
  const all = sortByUrgency(routers).map(fleetRow)
  const broken = all.filter((r) => r.pill.tone === 'danger').length
  const f = useFleetFilter(routers)
  const rows = f.view.visible.map(fleetRow)
  const { batch, recheckAll } = useFleetRecheck(routers)

  return (
    <Overlay title="Мои роутеры" onBack={onClose}>
      <div class="screen">
        <h1 class="screen-title">Мои роутеры</h1>
        <p class="router-lastseen">
          {broken === 0
            ? `Все ${all.length} в порядке.`
            : `Сломанное сверху: ${broken} из ${all.length} требуют внимания.`}
        </p>

        {all.length > 1 && (
          <FleetFilterBar
            query={f.query}
            filter={f.filter}
            counts={f.view.counts}
            onQuery={f.setQuery}
            onFilter={f.setFilter}
            shortcut={shortcut}
          />
        )}

        {rows.length > 0 ? (
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
                  <PanelLine url={r.panelURL} nested />
                  <span class="fleet-sub">{r.sub}</span>
                </span>
                <svg class="list-row-chevron" viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                  <path d="M 6 3 L 11 8 L 6 13" />
                </svg>
              </button>
            ))}
          </div>
        ) : (
          all.length > 1 && (
            <p class="filter-empty">
              {emptyFilterText({ query: f.query, filter: f.filter })}{' '}
              <button type="button" class="btn btn-ghost btn-row" onClick={f.reset}>
                Сбросить
              </button>
            </p>
          )
        )}

        {all.length > 1 && (
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

        {isAdmin && (
          <div class="fleet-park" id="park">
            <ParkSection openSheet={openSheet} onOpenRouter={onPick} currentID={currentID} openLayer={openLayer} onOpenConnection={onOpenConnection} />
          </div>
        )}
      </div>
    </Overlay>
  )
}
