import { fleetRow } from '../fleet.js'
import { emptyFilterText } from '../fleetFilter.js'
import { useFleetFilter } from '../useFleetFilter.js'
import { FleetFilterBar } from './FleetFilterBar.jsx'
import { PanelLine } from './PanelLine.jsx'
import { tabLabel } from '../nav.js'

// Боковая колонка широкой раскладки (v0.52): бренд, вкладки, поиск и список
// роутеров (тот же порядок и те же слова, что в «Моих роутерах»), внизу выход.
// У Парка один вход -- вкладка (пункта «Парк» в подвале больше нет).
//
// Поиск -- только когда искать есть в чём (два роутера и больше).
export function Sidebar({ mode, routers, currentID, isAdmin, tabs = [], tab, onTab, onPick, onLogout, shortcut = true }) {
  const f = useFleetFilter(routers)
  const rows = f.view.visible.map(fleetRow)
  const searchable = (routers?.length ?? 0) > 1
  return (
    <aside class="side">
      <div class="side-brand">
        <span class="side-brand-name">wg-monitor</span>
        {mode === 'web' && <span class="side-brand-mode">веб-управление</span>}
      </div>

      {/* v0.52: колонка = те же вкладки + список роутеров; у Парка один вход
          -- вкладка (пункта «Парк» в подвале больше нет). */}
      <nav class="side-tabs" aria-label="Вкладки">
        {tabs.map((key) => (
          <button
            key={key}
            type="button"
            class={`side-link${key === tab ? ' side-link-active' : ''}`}
            aria-current={key === tab ? 'page' : undefined}
            onClick={() => onTab(key)}
          >
            <span>{tabLabel(key)}</span>
          </button>
        ))}
      </nav>

      <div class="side-head">
        <span>{isAdmin ? 'Роутеры парка' : 'Мои роутеры'}</span>
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

      {mode === 'web' && (
        <div class="side-foot">
          <button type="button" class="side-link" onClick={onLogout}>
            <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
              <path d="M 6 2.5 H 3.5 a 1 1 0 0 0 -1 1 v 9 a 1 1 0 0 0 1 1 H 6" />
              <path d="M 10 5 L 13 8 L 10 11 M 13 8 H 6.5" />
            </svg>
            <span>Выйти</span>
          </button>
        </div>
      )}
    </aside>
  )
}
