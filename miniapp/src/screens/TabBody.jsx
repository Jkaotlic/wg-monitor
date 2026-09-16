import { RouterDetail } from './RouterDetail.jsx'
import { TunnelsTab } from './TunnelsTab.jsx'
import { DiagTab } from './DiagTab.jsx'
import { EventsTab } from './EventsTab.jsx'
import { routerContext } from './OverlayHost.jsx'

export function TabBody({ nav, dispatch, routers, isAdmin }) {
  if (nav.routerID == null) return <p class="state">Выберите роутер в списке.</p>
  const { current, asleep } = routerContext(routers, nav.routerID)
  const openSheet = (sheet) => dispatch({ type: 'sheet', sheet })
  switch (nav.tab) {
    case 'router':
      return (
        <RouterDetail
          id={nav.routerID}
          isAdmin={isAdmin}
          onOpenAdmin={() => dispatch({ type: 'overlay', overlay: 'admin' })}
          openSheet={openSheet}
          onTab={(tab) => dispatch({ type: 'tab', tab })}
        />
      )
    case 'tunnels':
      return (
        <TunnelsTab
          routerID={nav.routerID}
          asleep={asleep}
          onOpenRoutes={() => dispatch({ type: 'overlay', overlay: 'routes' })}
          openSheet={openSheet}
        />
      )
    case 'diag':
      return <DiagTab routerID={nav.routerID} asleep={asleep} />
    default:
      return <EventsTab routerID={nav.routerID} routerName={current?.nickname} />
  }
}
