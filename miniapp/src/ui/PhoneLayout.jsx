import { TABS } from '../nav.js'
import { Header } from './Header.jsx'
import { TabBar } from './TabBar.jsx'
import { SheetHost } from './Sheet.jsx'
import { OverlayHost } from '../screens/OverlayHost.jsx'
import { TabBody } from '../screens/TabBody.jsx'

// Телефонная раскладка -- та же, что была до веб-управления: шапка, вкладки
// внизу, оверлей крышкой, лист снизу.
export function PhoneLayout({ nav, dispatch, routers, isAdmin, onLogout, refreshRouters }) {
  return (
    <>
      <Header
        fleetVisible={routers.length > 1 || Boolean(isAdmin)}
        onFleet={() => dispatch({ type: 'overlay', overlay: 'fleet' })}
        onLogout={onLogout}
      />
      <div class="app-body">
        <TabBody nav={nav} dispatch={dispatch} routers={routers} isAdmin={isAdmin} />
      </div>
      <TabBar tabs={TABS} tab={nav.tab} onTab={(tab) => dispatch({ type: 'tab', tab })} />
      <OverlayHost nav={nav} dispatch={dispatch} routers={routers} isAdmin={isAdmin} refreshRouters={refreshRouters} />
      <SheetHost nav={nav} dispatch={dispatch} />
    </>
  )
}
