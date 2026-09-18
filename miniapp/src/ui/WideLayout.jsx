import { Sidebar } from './Sidebar.jsx'
import { WideHeader } from './WideHeader.jsx'
import { SheetHost } from './Sheet.jsx'
import { OverlayHost } from '../screens/OverlayHost.jsx'
import { TabBody } from '../screens/TabBody.jsx'
import { FleetHome } from '../screens/FleetHome.jsx'
import { FLEET_OVERLAYS } from '../nav.js'

// Широкая раскладка: колонка роутеров слева, справа шапка с вкладками и
// содержимое. Оверлеи открываются в основной области -- список роутеров
// остаётся на виду. «Мои роутеры» (fleet) здесь не нужен: колонка и есть список.
//
// Слои парка (мастер, ход работы, ожидание раскатки) не требуют выбранного
// роутера: без него они занимают место сводки.
export function WideLayout({ mode, nav, dispatch, routers, isAdmin, onLogout, refreshRouters }) {
  const current = routers.find((r) => r.id === nav.routerID)
  const fleetLayer = Boolean(isAdmin && FLEET_OVERLAYS.includes(nav.overlay))
  const overlayOpen = Boolean(nav.overlay && nav.overlay !== 'fleet' && (current || fleetLayer))
  const narrow = overlayOpen || nav.tab !== 'router'

  // Парк от выбранного роутера не зависит и живёт под сводкой (#park).
  // Роутер выбран -- снимаем выбор, чтобы сводка встала на место, и едем к
  // Парку, когда она нарисуется.
  function openPark() {
    const scroll = () => document.getElementById('park')?.scrollIntoView?.({ behavior: 'smooth', block: 'start' })
    if (current || nav.overlay) {
      dispatch({ type: 'router', id: null })
      setTimeout(scroll, 0)
      return
    }
    scroll()
  }

  const host = <OverlayHost nav={nav} dispatch={dispatch} routers={routers} isAdmin={isAdmin} refreshRouters={refreshRouters} />

  return (
    <div class="wide-shell">
      <Sidebar
        mode={mode}
        routers={routers}
        currentID={nav.routerID}
        isAdmin={isAdmin}
        parkActive={Boolean(isAdmin) && !current}
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
              onTab={(tab) => dispatch({ type: 'tab', tab, closeOverlay: true })}
            />
            <div class={`main-content${narrow ? ' main-content-narrow' : ''}`}>
              {overlayOpen ? host : <TabBody nav={nav} dispatch={dispatch} routers={routers} isAdmin={isAdmin} />}
            </div>
          </>
        ) : fleetLayer ? (
          <div class="main-content main-content-narrow">{host}</div>
        ) : (
          <div class="main-content main-content-narrow">
            <FleetHome
              routers={routers}
              isAdmin={isAdmin}
              onPick={(id) => dispatch({ type: 'router', id })}
              openSheet={(sheet) => dispatch({ type: 'sheet', sheet })}
              openLayer={(overlay, extra = {}) => dispatch({ type: 'overlay', overlay, params: { ...extra, returnTo: null } })}
              onOpenConnection={(id) => {
                dispatch({ type: 'router', id })
                dispatch({ type: 'overlay', overlay: 'agentconn' })
              }}
            />
          </div>
        )}
      </main>
      <SheetHost nav={nav} dispatch={dispatch} />
    </div>
  )
}
