import { RouterDetail } from './RouterDetail.jsx'
import { TunnelsTab } from './TunnelsTab.jsx'
import { DiagTab } from './DiagTab.jsx'
import { EventsTab } from './EventsTab.jsx'
import { ManageTab } from './ManageTab.jsx'
import { ParkTab } from './ParkTab.jsx'
import { PARK_TAB, tabOwnsLayer } from '../nav.js'
import { routerContext } from './OverlayHost.jsx'

export function TabBody({ nav, dispatch, routers, isAdmin }) {
  // «Парк» (v0.48) от роутера не зависит: открывается и без него. Слои парка
  // возвращаются сюда же (returnTo 'park'). Вкладка -- только админу; сервер
  // остальным всё равно ответит 404.
  if (nav.tab === PARK_TAB && isAdmin) {
    return (
      <ParkTab
        routers={routers}
        onPick={(id) => dispatch({ type: 'router', id })}
        openSheet={(sheet) => dispatch({ type: 'sheet', sheet })}
        openLayer={(overlay, extra = {}) => dispatch({ type: 'overlay', overlay, params: { ...extra, returnTo: PARK_TAB } })}
        onOpenConnection={(id) => {
          dispatch({ type: 'router', id })
          dispatch({ type: 'overlay', overlay: 'agentconn' })
        }}
      />
    )
  }
  if (nav.routerID == null) return <p class="state">Выберите роутер в списке.</p>
  const { current, asleep } = routerContext(routers, nav.routerID)
  const openSheet = (sheet) => dispatch({ type: 'sheet', sheet })
  // Переход к переносу с экрана VPN-туннеля: «Маршруты» сами откроют выбор
  // цели и вернут на экран VPN-туннеля. Id живёт в параметрах слоя, в адрес
  // пишется только open=routes.
  const openRebind = (tunnelID) =>
    dispatch({ type: 'overlay', overlay: 'routes', params: { rebindFrom: tunnelID, returnTo: 'tunnel', returnParams: { tunnelID } } })
  // key -- номер роутера: переход A→B пересоздаёт вкладку, и ни состояние,
  // ни поздний ответ по A не переезжают на экран B (MINI-04).
  const key = nav.routerID
  switch (nav.tab === PARK_TAB ? 'router' : nav.tab) {
    case 'router':
      return (
        <RouterDetail
          key={key}
          id={nav.routerID}
          panelURL={current?.panel_url}
          reserveOnlyAlert={current?.reserve_only_alert}
          openSheet={openSheet}
          onTab={(tab) => dispatch({ type: 'tab', tab })}
          openLayer={(overlay, params) => dispatch({ type: 'overlay', overlay, params })}
          repairOpen={nav.overlay === 'repair'}
        />
      )
    case 'tunnels':
      return (
        <TunnelsTab
          key={key}
          routerID={nav.routerID}
          asleep={asleep}
          isAdmin={isAdmin}
          layer={tabOwnsLayer(nav) ? nav.overlay : null}
          layerParams={nav.overlayParams ?? {}}
          openLayer={(overlay, params) => dispatch({ type: 'overlay', overlay, params })}
          closeLayer={() => dispatch({ type: 'back' })}
          onOpenRoutes={() => dispatch({ type: 'overlay', overlay: 'routes' })}
          onOpenRebind={openRebind}
          cabinetOpen={nav.overlay === 'cabinet'}
          routesOpen={nav.overlay === 'routes'}
          openSheet={openSheet}
        />
      )
    case 'diag':
      return <DiagTab key={key} routerID={nav.routerID} asleep={asleep} isAdmin={isAdmin} openSheet={openSheet} />
    // Экраны глубже «Управления» (настройки и подключение агента, сброс DNS,
    // пакеты) -- слои с адресом; закрываются обратно во вкладку. «Ход
    // работы» перенаправления возвращает сюда же (returnTo 'manage').
    case 'manage':
      return (
        <ManageTab
          key={key}
          routerID={nav.routerID}
          routerName={current?.nickname}
          isAdmin={isAdmin}
          focusGroup={nav.manageFocus ?? null}
          focusNonce={nav.manageFocusSeq ?? 0}
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
      return <EventsTab key={key} routerID={nav.routerID} routerName={current?.nickname} />
  }
}
