import { barTabs, fleetIsHome, PARK_TAB } from '../nav.js'
import { routerPickMode, stripChips } from '../routerPick.js'
import { Header } from './Header.jsx'
import { RouterStrip } from './RouterStrip.jsx'
import { TabBar } from './TabBar.jsx'
import { SheetHost } from './Sheet.jsx'
import { OverlayHost } from '../screens/OverlayHost.jsx'
import { TabBody } from '../screens/TabBody.jsx'

// Телефонная раскладка (v0.52): шапка с именем роутера (один вход выбора),
// под ней полоса «Мои роутеры» для 2–5 роутеров, вкладки внизу. Список
// роутеров -- слой для 6+ и для админа; админу без роутера панель -- один Парк,
// и одной вкладке панель не нужна.
export function PhoneLayout({ nav, dispatch, routers, isAdmin, onLogout, refreshRouters }) {
  const home = fleetIsHome(nav)
  const mode = routerPickMode({ count: routers.length, isAdmin: Boolean(isAdmin) })
  const current = routers.find((r) => r.id === nav.routerID) ?? null
  const tabs = barTabs({ isAdmin: Boolean(isAdmin), routerID: nav.routerID })
  const title = current?.nickname ?? (isAdmin && mode === 'list' ? 'Выбрать роутер' : 'wg-monitor')
  const onPick = mode === 'list' && !home ? () => dispatch({ type: 'overlay', overlay: 'fleet' }) : null
  const strip = mode === 'strip' && current && nav.tab !== PARK_TAB && !home
  return (
    <>
      <Header title={title} onPick={onPick} onLogout={onLogout} />
      {strip && <RouterStrip chips={stripChips(routers, current.id)} onPick={(id) => dispatch({ type: 'router', id, keepTab: true })} />}
      <div class="app-body">
        <TabBody nav={nav} dispatch={dispatch} routers={routers} isAdmin={isAdmin} />
      </div>
      {tabs.length > 1 && !home && <TabBar tabs={tabs} tab={nav.tab} onTab={(tab) => dispatch({ type: 'tab', tab })} />}
      <OverlayHost nav={nav} dispatch={dispatch} routers={routers} isAdmin={isAdmin} refreshRouters={refreshRouters} />
      <SheetHost nav={nav} dispatch={dispatch} />
    </>
  )
}
