import { RouterDetail } from './RouterDetail.jsx'
import { TunnelsTab } from './TunnelsTab.jsx'
import { ChecksTab } from './ChecksTab.jsx'
import { ManageTab } from './ManageTab.jsx'
import { ParkTab } from './ParkTab.jsx'
import { PARK_TAB, tabOwnsLayer, layerFamily } from '../nav.js'
import { routerContext } from './OverlayHost.jsx'
import { routerPickMode, otherAlertRouter } from '../routerPick.js'

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
  // Неизвестная вкладка (старый снимок навигации, опечатка в ссылке) и Парк
  // не-админа -- «Роутер», а не пустой экран.
  switch (nav.tab) {
    case 'router':
    default: {
      const strip = routerPickMode({ count: routers.length, isAdmin: Boolean(isAdmin) }) === 'strip'
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
          otherAlert={strip ? otherAlertRouter(routers, nav.routerID) : null}
          onOpenRouter={(id) => dispatch({ type: 'router', id })}
          onOpenService={() => dispatch({ type: 'manage', section: 'service' })}
        />
      )
    }
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
          cabinetOpen={layerFamily(nav.overlay) === 'cabinet'}
          routesOpen={layerFamily(nav.overlay) === 'routes'}
          openSheet={openSheet}
        />
      )
    case 'diag':
      return (
        <ChecksTab
          key={key}
          routerID={nav.routerID}
          routerName={current?.nickname}
          asleep={asleep}
          isAdmin={isAdmin}
          openSheet={openSheet}
          view={nav.diagView ?? 'now'}
          onView={(view) => dispatch({ type: 'diagView', view })}
        />
      )
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
          onOpenPorthop={() => dispatch({ type: 'overlay', overlay: 'porthop' })}
          onOpenSpace={() => dispatch({ type: 'overlay', overlay: 'space' })}
        />
      )
  }
}
