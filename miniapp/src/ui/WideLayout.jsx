import { Sidebar } from './Sidebar.jsx'
import { WideHeader } from './WideHeader.jsx'
import { SheetHost } from './Sheet.jsx'
import { OverlayHost } from '../screens/OverlayHost.jsx'
import { TabBody } from '../screens/TabBody.jsx'
import { FleetHome } from '../screens/FleetHome.jsx'

const PARK_OVERLAYS = ['admin', 'agentcfg', 'dnsreset']

// Широкая раскладка: колонка роутеров слева, справа шапка с вкладками и
// содержимое. Оверлеи открываются в основной области -- список роутеров
// остаётся на виду. «Мои роутеры» (fleet) здесь не нужен: колонка и есть список.
export function WideLayout({ mode, nav, dispatch, routers, isAdmin, onLogout }) {
  const current = routers.find((r) => r.id === nav.routerID)
  const overlayOpen = Boolean(nav.overlay && nav.overlay !== 'fleet' && current)
  const narrow = overlayOpen || nav.tab !== 'router'

  // Парк от выбранного роутера не зависит. Роутер выбран -- обслуживание (там
  // Парк и доступы этого роутера); не выбран -- Парк уже стоит под сводкой, и
  // кнопка просто ведёт к нему.
  function openPark() {
    if (current) {
      dispatch({ type: 'overlay', overlay: 'admin' })
      return
    }
    document.getElementById('park')?.scrollIntoView?.({ behavior: 'smooth', block: 'start' })
  }

  return (
    <div class="wide-shell">
      <Sidebar
        mode={mode}
        routers={routers}
        currentID={nav.routerID}
        isAdmin={isAdmin}
        parkActive={PARK_OVERLAYS.includes(nav.overlay)}
        onPick={(id) => dispatch({ type: 'router', id })}
        onPark={openPark}
        onLogout={onLogout}
      />
      <main class="main">
        {current ? (
          <>
            <WideHeader
              router={current}
              tab={nav.tab}
              onTab={(tab) => dispatch({ type: 'tab', tab, closeOverlay: true })}
              onSettings={() => dispatch({ type: 'overlay', overlay: 'settings' })}
            />
            <div class={`main-content${narrow ? ' main-content-narrow' : ''}`}>
              {overlayOpen ? (
                <OverlayHost nav={nav} dispatch={dispatch} routers={routers} isAdmin={isAdmin} />
              ) : (
                <TabBody nav={nav} dispatch={dispatch} routers={routers} isAdmin={isAdmin} />
              )}
            </div>
          </>
        ) : (
          <div class="main-content main-content-narrow">
            <FleetHome
              routers={routers}
              isAdmin={isAdmin}
              onPick={(id) => dispatch({ type: 'router', id })}
              openSheet={(sheet) => dispatch({ type: 'sheet', sheet })}
            />
          </div>
        )}
      </main>
      <SheetHost nav={nav} dispatch={dispatch} />
    </div>
  )
}
