import { Sidebar } from './Sidebar.jsx'
import { WideHeader } from './WideHeader.jsx'
import { SheetHost } from './Sheet.jsx'
import { OverlayHost } from '../screens/OverlayHost.jsx'
import { TabBody } from '../screens/TabBody.jsx'
import { FleetHome } from '../screens/FleetHome.jsx'
import { FLEET_OVERLAYS, PARK_TAB, TAB_LAYERS } from '../nav.js'

// Широкая раскладка: колонка роутеров слева, справа шапка с вкладками и
// содержимое. Оверлеи открываются в основной области -- список роутеров
// остаётся на виду. «Мои роутеры» (fleet) здесь не нужен: колонка и есть список.
//
// Слои парка (мастер, ход работы, ожидание раскатки) не требуют выбранного
// роутера: без него они занимают место сводки.
export function WideLayout({ mode, nav, dispatch, routers, isAdmin, onLogout, refreshRouters }) {
  const current = routers.find((r) => r.id === nav.routerID)
  const fleetLayer = Boolean(isAdmin && FLEET_OVERLAYS.includes(nav.overlay))
  const overlayOpen = Boolean(nav.overlay && nav.overlay !== 'fleet' && !TAB_LAYERS[nav.overlay] && (current || fleetLayer))
  const narrow = overlayOpen || nav.tab !== 'router'
  // «Парк» (v0.48) -- вкладка, а не раздел под сводкой: открывается и с
  // выбранным роутером (тогда над ним шапка роутера с вкладкой «Парк»), и
  // без него -- в основной области вместо сводки.
  const parkTab = Boolean(isAdmin) && nav.tab === PARK_TAB

  function openPark() {
    dispatch({ type: 'tab', tab: PARK_TAB, closeOverlay: true })
  }

  const host = <OverlayHost nav={nav} dispatch={dispatch} routers={routers} isAdmin={isAdmin} refreshRouters={refreshRouters} />

  return (
    <div class="wide-shell">
      <Sidebar
        mode={mode}
        routers={routers}
        currentID={nav.routerID}
        isAdmin={isAdmin}
        parkActive={Boolean(isAdmin) && ((parkTab && !overlayOpen) || (!current && fleetLayer))}
        onPick={(id) => dispatch({ type: 'router', id })}
        onPark={openPark}
        onLogout={onLogout}
        shortcut={!nav.sheet}
      />
      <main class="main">
        {current ? (
          <>
            <WideHeader
              router={current}
              tab={nav.tab}
              isAdmin={isAdmin}
              onTab={(tab) => dispatch({ type: 'tab', tab, closeOverlay: true })}
            />
            <div class={`main-content${narrow ? ' main-content-narrow' : ''}`}>
              {overlayOpen ? host : <TabBody nav={nav} dispatch={dispatch} routers={routers} isAdmin={isAdmin} />}
            </div>
          </>
        ) : fleetLayer ? (
          <div class="main-content main-content-narrow">{host}</div>
        ) : parkTab ? (
          <div class="main-content main-content-narrow">
            <TabBody nav={nav} dispatch={dispatch} routers={routers} isAdmin={isAdmin} />
          </div>
        ) : (
          <div class="main-content main-content-narrow">
            <FleetHome
              routers={routers}
              isAdmin={isAdmin}
              onPick={(id) => dispatch({ type: 'router', id })}
            />
          </div>
        )}
      </main>
      <SheetHost nav={nav} dispatch={dispatch} />
    </div>
  )
}
