import { Sidebar } from './Sidebar.jsx'
import { WideHeader } from './WideHeader.jsx'
import { SheetHost } from './Sheet.jsx'
import { OverlayHost } from '../screens/OverlayHost.jsx'
import { TabBody } from '../screens/TabBody.jsx'
import { FleetHome } from '../screens/FleetHome.jsx'
import { FLEET_OVERLAYS, PARK_TAB, TAB_LAYERS, barTabs } from '../nav.js'

// Широкая раскладка (v0.52): колонка -- вкладки и роутеры, справа шапка роутера
// и содержимое. Оверлеи -- в основной области; слой вкладки рисует вкладка.
export function WideLayout({ mode, nav, dispatch, routers, isAdmin, onLogout, refreshRouters }) {
  const current = routers.find((r) => r.id === nav.routerID)
  const fleetLayer = Boolean(isAdmin && FLEET_OVERLAYS.includes(nav.overlay))
  const overlayOpen = Boolean(nav.overlay && nav.overlay !== 'fleet' && !TAB_LAYERS[nav.overlay] && (current || fleetLayer))
  const parkTab = Boolean(isAdmin) && nav.tab === PARK_TAB
  const narrow = overlayOpen || nav.tab !== 'router'
  const host = <OverlayHost nav={nav} dispatch={dispatch} routers={routers} isAdmin={isAdmin} refreshRouters={refreshRouters} />
  const body = <TabBody nav={nav} dispatch={dispatch} routers={routers} isAdmin={isAdmin} />

  return (
    <div class="wide-shell">
      <Sidebar
        mode={mode}
        routers={routers}
        currentID={nav.routerID}
        isAdmin={isAdmin}
        // Без выбранного роутера вкладкам роутера показывать нечего: у не-админа
        // (6+ роутеров) в основной области сводка, у админа -- один Парк.
        tabs={nav.routerID == null && !isAdmin ? [] : barTabs({ isAdmin: Boolean(isAdmin), routerID: nav.routerID })}
        // Слой парка без роутера -- место Парка, даже если ключ вкладки другой.
        tab={!current && fleetLayer ? PARK_TAB : nav.tab}
        onTab={(tab) => dispatch({ type: 'tab', tab, closeOverlay: true })}
        onPick={(id) => dispatch({ type: 'router', id, keepTab: true })}
        onLogout={onLogout}
        shortcut={!nav.sheet}
      />
      <main class="main">
        {current && !parkTab && <WideHeader router={current} />}
        {overlayOpen ? (
          <div class={`main-content${narrow ? ' main-content-narrow' : ''}`}>{host}</div>
        ) : current || parkTab ? (
          <div class={`main-content${narrow ? ' main-content-narrow' : ''}`}>{body}</div>
        ) : (
          <div class="main-content main-content-narrow">
            <FleetHome routers={routers} isAdmin={isAdmin} onPick={(id) => dispatch({ type: 'router', id })} />
          </div>
        )}
      </main>
      <SheetHost nav={nav} dispatch={dispatch} />
    </div>
  )
}
