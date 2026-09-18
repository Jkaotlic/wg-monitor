import { RouterDetail } from './RouterDetail.jsx'
import { TunnelsTab } from './TunnelsTab.jsx'
import { DiagTab } from './DiagTab.jsx'
import { EventsTab } from './EventsTab.jsx'
import { ManageTab } from './ManageTab.jsx'
import { routerContext } from './OverlayHost.jsx'

export function TabBody({ nav, dispatch, routers, isAdmin }) {
  if (nav.routerID == null) return <p class="state">Выберите роутер в списке.</p>
  const { current, asleep } = routerContext(routers, nav.routerID)
  const openSheet = (sheet) => dispatch({ type: 'sheet', sheet })
  // Переход к переносу с экрана VPN-туннеля: «Маршруты» сами откроют выбор
  // цели. Id VPN-туннеля живёт в параметрах слоя, в адрес пишется только
  // open=routes.
  const openRebind = (tunnelID) => dispatch({ type: 'overlay', overlay: 'routes', params: { rebindFrom: tunnelID } })
  switch (nav.tab) {
    case 'router':
      return (
        <RouterDetail
          id={nav.routerID}
          panelURL={current?.panel_url}
          reserveOnlyAlert={current?.reserve_only_alert}
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
          onOpenRebind={openRebind}
          onOpenCabinet={() => dispatch({ type: 'overlay', overlay: 'cabinet' })}
          cabinetOpen={nav.overlay === 'cabinet'}
          routesOpen={nav.overlay === 'routes'}
          openSheet={openSheet}
        />
      )
    case 'diag':
      return <DiagTab routerID={nav.routerID} asleep={asleep} />
    // Экраны глубже «Управления» (настройки и подключение агента, сброс DNS,
    // пакеты) -- слои с адресом; закрываются обратно во вкладку. «Ход
    // работы» перенаправления возвращает сюда же (returnTo 'manage').
    case 'manage':
      return (
        <ManageTab
          routerID={nav.routerID}
          routerName={current?.nickname}
          isAdmin={isAdmin}
          asleep={asleep}
          openSheet={openSheet}
          openLayer={(overlay, extra = {}) => dispatch({ type: 'overlay', overlay, params: { ...extra, returnTo: 'manage' } })}
          onOpenAgentConfig={() => dispatch({ type: 'overlay', overlay: 'agentcfg' })}
          onOpenAgentConnection={() => dispatch({ type: 'overlay', overlay: 'agentconn' })}
          onOpenDNSReset={() => dispatch({ type: 'overlay', overlay: 'dnsreset' })}
          onOpenPackages={() => dispatch({ type: 'overlay', overlay: 'packages' })}
        />
      )
    default:
      return <EventsTab routerID={nav.routerID} routerName={current?.nickname} />
  }
}
