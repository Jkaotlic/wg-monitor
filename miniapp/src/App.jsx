import { useEffect, useReducer, useState } from 'preact/hooks'
import {
  initTelegram,
  getInitData,
  onBackButtonClick,
  paintChrome,
  setBackButtonVisible,
} from './telegram.js'
import { applyPalette } from './theme.js'
import { createSession, fetchRouters } from './api.js'
import { initialNav, navReducer, backButtonVisible, TABS } from './nav.js'
import { Header } from './ui/Header.jsx'
import { TabBar } from './ui/TabBar.jsx'
import { RouterDetail } from './screens/RouterDetail.jsx'
import { FleetOverlay } from './screens/FleetOverlay.jsx'
import { AdminOverlay } from './screens/AdminOverlay.jsx'
import { NoAccess } from './screens/NoAccess.jsx'
import { RoutesTab } from './screens/RoutesTab.jsx'
import { SettingsScreen } from './screens/SettingsScreen.jsx'
import { TunnelsTab } from './screens/TunnelsTab.jsx'
import { Overlay } from './ui/Overlay.jsx'
import { DiagTab } from './screens/DiagTab.jsx'
import { EventsTab } from './screens/EventsTab.jsx'
import { Sheet } from './ui/Sheet.jsx'

function deepLinkRouterID() {
  const params = new URLSearchParams(window.location.search)
  const raw = params.get('router')
  const id = raw ? Number(raw) : NaN
  return Number.isFinite(id) ? id : null
}

export function App() {
  const [status, setStatus] = useState('loading')
  const [isAdmin, setIsAdmin] = useState(false)
  const [routers, setRouters] = useState([])
  const [nav, dispatch] = useReducer(navReducer, initialNav({ routerIDs: [], deepLinkID: null }))

  useEffect(() => {
    initTelegram()
    // Тема одна, поэтому палитра применяется один раз и подписки на смену
    // схемы больше нет: Telegram может сколько угодно переключаться между
    // светлой и тёмной -- приложение остаётся тёмным намеренно.
    paintChrome(applyPalette())
    // Сессия и список роутеров грузятся вместе: без списка нельзя решить,
    // открывать ли конкретный роутер, показывать список или экран пустого
    // доступа -- а решать это один раз при входе честнее, чем перерешать
    // на каждом рендере.
    createSession(getInitData())
      .then((s) => {
        setIsAdmin(!!s.is_admin)
        return fetchRouters()
      })
      .then((data) => {
        const list = data.routers ?? []
        setRouters(list)
        dispatch({
          type: 'init',
          state: initialNav({ routerIDs: list.map((r) => r.id), deepLinkID: deepLinkRouterID() }),
        })
        setStatus('ready')
      })
      .catch(() => setStatus('error'))
  }, [])

  // Кнопкой "назад" владеет оболочка, а не экраны: слоёв несколько, кнопка
  // одна, и порядок их закрытия описан в navReducer.
  useEffect(() => {
    setBackButtonVisible(backButtonVisible(nav))
    return onBackButtonClick(() => dispatch({ type: 'back' }))
  }, [nav.overlay, nav.sheet])

  if (status === 'loading') return <p class="state">Загрузка…</p>
  if (status === 'error') {
    return (
      <p class="state state-error">
        Не удалось войти. Откройте mini-app из Telegram заново.
      </p>
    )
  }
  // Доступ мог появиться, пока приложение было открыто: экран пустого доступа
  // умеет переспросить, и тогда оболочка продолжает как при обычном входе.
  if (routers.length === 0) {
    return (
      <NoAccess
        onRetry={(list) => {
          setRouters(list)
          dispatch({ type: 'init', state: initialNav({ routerIDs: list.map((r) => r.id), deepLinkID: deepLinkRouterID() }) })
        }}
      />
    )
  }

  // Статус берём из списка флота: экраны табов не грузят карточку роутера
  // сами, а спящему роутеру нужно обещать отложенный ответ, а не мгновенный.
  const current = routers.find((r) => r.id === nav.routerID)
  const asleep = current?.status === 'offline' || current?.status === 'sleeping'

  const overlay = nav.overlay === 'fleet'
    ? (
      <FleetOverlay
        routers={routers}
        currentID={nav.routerID}
        onPick={(id) => dispatch({ type: 'router', id })}
        onClose={() => dispatch({ type: 'overlay', overlay: null })}
      />
    )
    : nav.overlay === 'settings' && nav.routerID != null
      ? (
        <SettingsScreen
          routerID={nav.routerID}
          routerName={current?.nickname}
          asleep={asleep}
          openSheet={(sheet) => dispatch({ type: 'sheet', sheet })}
          onClose={() => dispatch({ type: 'overlay', overlay: null })}
        />
      )
    : nav.overlay === 'admin' && nav.routerID != null
      ? <AdminOverlay routerID={nav.routerID} onClose={() => dispatch({ type: 'overlay', overlay: null })} />
      : nav.overlay === 'routes' && nav.routerID != null
        ? (
          <Overlay title="Маршруты" backLabel="Туннели" onBack={() => dispatch({ type: 'overlay', overlay: null })}>
            <RoutesTab
              routerID={nav.routerID}
              asleep={asleep}
              openSheet={(sheet) => dispatch({ type: 'sheet', sheet })}
            />
          </Overlay>
        )
        : null

  return (
    <>
      <Header
        fleetVisible={routers.length > 1}
        onFleet={() => dispatch({ type: 'overlay', overlay: 'fleet' })}
        onSettings={nav.routerID != null ? () => dispatch({ type: 'overlay', overlay: 'settings' }) : undefined}
      />
      <div class="app-body">
        {nav.routerID == null ? (
          <p class="state">Выберите роутер в списке.</p>
        ) : nav.tab === 'router' ? (
          <RouterDetail
            id={nav.routerID}
            isAdmin={isAdmin}
            onOpenAdmin={() => dispatch({ type: 'overlay', overlay: 'admin' })}
            openSheet={(sheet) => dispatch({ type: 'sheet', sheet })}
            onTab={(tab) => dispatch({ type: 'tab', tab })}
          />
        ) : nav.tab === 'tunnels' ? (
          <TunnelsTab
            routerID={nav.routerID}
            asleep={asleep}
            onOpenRoutes={() => dispatch({ type: 'overlay', overlay: 'routes' })}
            openSheet={(sheet) => dispatch({ type: 'sheet', sheet })}
          />
        ) : nav.tab === 'diag' ? (
          <DiagTab routerID={nav.routerID} asleep={asleep} />
        ) : (
          <EventsTab routerID={nav.routerID} routerName={current?.nickname} />
        )}
      </div>
      <TabBar tabs={TABS} tab={nav.tab} onTab={(tab) => dispatch({ type: 'tab', tab })} />
      {overlay}
      {nav.sheet && (
        <Sheet
          sheet={nav.sheet}
          asleep={nav.sheet.asleep}
          onClose={() => dispatch({ type: 'sheet', sheet: null })}
        />
      )}
    </>
  )
}
