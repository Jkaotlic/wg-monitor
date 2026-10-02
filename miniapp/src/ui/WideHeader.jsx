import { fleetRow } from '../fleet.js'
import { Chip } from './Chip.jsx'
import { PanelLine } from './PanelLine.jsx'

// Шапка основной области: какой роутер, в каком он состоянии и что с ним --
// одной строкой. Вкладки живут в боковой колонке (v0.52). Под именем -- адрес
// панели awg-manager, если сервер его отдал.
export function WideHeader({ router }) {
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
    </header>
  )
}
