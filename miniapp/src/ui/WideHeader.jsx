import { TABS, tabLabel } from '../nav.js'
import { fleetRow } from '../fleet.js'
import { Chip } from './Chip.jsx'
import { PanelLine } from './PanelLine.jsx'

// Шапка основной области: какой роутер, в каком он состоянии и что с ним --
// одной строкой; справа вкладки (вместо нижнего таббара). Шестерёнки больше
// нет: настройки стали вкладкой «Управление» (v0.41). Под именем -- адрес
// панели awg-manager, если сервер его отдал.
export function WideHeader({ router, tab, onTab }) {
  const row = fleetRow(router)
  return (
    <header class="main-head">
      <div class="main-head-id">
        <div class="main-head-line">
          <h1 class="main-head-name">{row.nickname}</h1>
          <Chip tone={row.pill.tone}>{row.pill.text}</Chip>
        </div>
        <PanelLine url={row.panelURL} />
        <p class="main-head-sub">{row.sub}</p>
      </div>
      <nav class="main-tabs" aria-label="Вкладки">
        {TABS.map((key) => (
          <button
            key={key}
            type="button"
            class={`main-tab${key === tab ? ' main-tab-active' : ''}`}
            aria-current={key === tab ? 'page' : undefined}
            onClick={() => onTab(key)}
          >
            {tabLabel(key)}
          </button>
        ))}
      </nav>
    </header>
  )
}
