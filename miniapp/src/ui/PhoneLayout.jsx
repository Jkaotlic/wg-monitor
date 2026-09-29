import { barTabs, fleetIsHome, PARK_TAB } from '../nav.js'
import { routerSwitchSheet } from '../fleet.js'
import { Header } from './Header.jsx'
import { TabBar } from './TabBar.jsx'
import { SheetHost } from './Sheet.jsx'
import { OverlayHost } from '../screens/OverlayHost.jsx'
import { TabBody } from '../screens/TabBody.jsx'

// Телефонная раскладка -- та же, что была до веб-управления: шапка, вкладки
// внизу, оверлей крышкой, лист снизу.
//
// Главный экран -- список роутеров без выбранного роутера -- крышка поверх
// всего. Админу панель встаёт поверх неё (tabbar-over): Парк достижим и
// отсюда. Её вкладки тогда -- Парк и сам список (barTabs); остальным панель,
// как и раньше, скрыта под списком.
export function PhoneLayout({ nav, dispatch, routers, isAdmin, onLogout, refreshRouters }) {
  const home = fleetIsHome(nav)
  const over = home && Boolean(isAdmin)
  const onTab = (tab) => {
    if (tab === 'fleet') dispatch({ type: 'overlay', overlay: 'fleet' })
    else dispatch({ type: 'tab', tab, closeOverlay: over })
  }
  // Без роутера у админа активна та из двух, что на экране: список (крышка)
  // или Парк.
  const active = nav.routerID == null && isAdmin ? (home ? 'fleet' : nav.tab) : nav.tab
  // Переключатель в шапке -- когда открыт роутер вкладки роутера и есть из
  // кого выбирать (v0.50). На «Парке» роутер не показан -- и выбирать нечего.
  const current = routers.find((r) => r.id === nav.routerID) ?? null
  const canSwitch = Boolean(current) && routers.length > 1 && nav.tab !== PARK_TAB && !home
  const openSwitch = () =>
    dispatch({ type: 'sheet', sheet: routerSwitchSheet(routers, current.id, (id) => dispatch({ type: 'router', id, keepTab: true })) })
  return (
    <>
      <Header
        router={canSwitch ? current : null}
        onSwitch={openSwitch}
        fleetVisible={routers.length > 1 || Boolean(isAdmin)}
        onFleet={() => dispatch({ type: 'overlay', overlay: 'fleet' })}
        onLogout={onLogout}
      />
      <div class="app-body">
        <TabBody nav={nav} dispatch={dispatch} routers={routers} isAdmin={isAdmin} />
      </div>
      <TabBar tabs={barTabs({ isAdmin: Boolean(isAdmin), routerID: nav.routerID })} tab={active} over={over} onTab={onTab} />
      <OverlayHost nav={nav} dispatch={dispatch} routers={routers} isAdmin={isAdmin} refreshRouters={refreshRouters} />
      <SheetHost nav={nav} dispatch={dispatch} />
    </>
  )
}
