import { TABS, tabLabel } from '../nav.js'
import { fleetRow } from '../fleet.js'
import { Chip } from './Chip.jsx'
import { GearIcon } from './GearIcon.jsx'

// Шапка основной области: какой роутер, в каком он состоянии и что с ним --
// одной строкой; справа вкладки (вместо нижнего таббара) и настройки.
export function WideHeader({ router, tab, onTab, onSettings }) {
  const row = fleetRow(router)
  return (
    <header class="main-head">
      <div class="main-head-id">
        <div class="main-head-line">
          <h1 class="main-head-name">{row.nickname}</h1>
          <Chip tone={row.pill.tone}>{row.pill.text}</Chip>
        </div>
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
      <button type="button" class="main-gear" onClick={onSettings} aria-label="Настройки">
        <GearIcon size={18} />
      </button>
    </header>
  )
}
