import { fleetRow } from '../fleet.js'
import { emptyFilterText } from '../fleetFilter.js'
import { useFleetFilter } from '../useFleetFilter.js'
import { FleetFilterBar } from './FleetFilterBar.jsx'
import { PanelLine } from './PanelLine.jsx'

// Боковая колонка широкой раскладки: бренд, поиск и список роутеров (тот же
// порядок и те же слова, что в «Моих роутерах»), внизу Парк и выход. Колонка
// нужна и при одном роутере: Парк и «Выйти» живут здесь.
//
// «Парк» не гаснет никогда: Парк -- про весь флот и от выбранного роутера не
// зависит. Ведёт он на сводку (#park), куда -- решает оболочка (WideLayout).
//
// Поиск -- только когда искать есть в чём (два роутера и больше).
export function Sidebar({ mode, routers, currentID, isAdmin, parkActive, onPick, onPark, onLogout, shortcut = true }) {
  const f = useFleetFilter(routers)
  const rows = f.view.visible.map(fleetRow)
  const searchable = (routers?.length ?? 0) > 1
  return (
    <aside class="side">
      <div class="side-brand">
        <span class="side-brand-name">wg-monitor</span>
        {mode === 'web' && <span class="side-brand-mode">веб-управление</span>}
      </div>

      <div class="side-head">
        <span>Роутеры</span>
        <span class="side-count">{routers?.length ?? 0}</span>
      </div>
      {searchable && (
        <div class="side-filter">
          <FleetFilterBar
            query={f.query}
            filter={f.filter}
            counts={f.view.counts}
            onQuery={f.setQuery}
            onFilter={f.setFilter}
            shortcut={shortcut}
          />
        </div>
      )}
      <nav class="side-list" aria-label="Роутеры">
        {rows.map((r) => (
          <button
            key={r.id}
            type="button"
            class={`side-row${r.id === currentID ? ' side-row-current' : ''}`}
            aria-current={r.id === currentID ? 'page' : undefined}
            onClick={() => onPick(r.id)}
          >
            <span class={`side-dot side-dot-${r.pill.tone}`} aria-hidden="true" />
            <span class="side-row-main">
              <span class="side-row-name">{r.nickname}</span>
              <PanelLine url={r.panelURL} nested />
              <span class="side-row-state">{r.pill.text}</span>
            </span>
          </button>
        ))}
        {searchable && rows.length === 0 && (
          <p class="filter-empty">
            {emptyFilterText({ query: f.query, filter: f.filter })}{' '}
            <button type="button" class="btn btn-ghost btn-row" onClick={f.reset}>
              Сбросить
            </button>
          </p>
        )}
      </nav>

      {(isAdmin || mode === 'web') && (
        <div class="side-foot">
          {isAdmin && (
            <button
              type="button"
              class={`side-link${parkActive ? ' side-link-active' : ''}`}
              onClick={onPark}
            >
              <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <rect x="2" y="2.5" width="12" height="4" rx="1.2" />
                <rect x="2" y="9.5" width="12" height="4" rx="1.2" />
              </svg>
              <span>Парк</span>
            </button>
          )}
          {mode === 'web' && (
            <button type="button" class="side-link" onClick={onLogout}>
              <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M 6 2.5 H 3.5 a 1 1 0 0 0 -1 1 v 9 a 1 1 0 0 0 1 1 H 6" />
                <path d="M 10 5 L 13 8 L 10 11 M 13 8 H 6.5" />
              </svg>
              <span>Выйти</span>
            </button>
          )}
        </div>
      )}
    </aside>
  )
}
