import { useEffect, useReducer, useState } from 'preact/hooks'
import {
  initTelegram,
  getInitData,
  getColorScheme,
  onColorSchemeChanged,
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
    const paint = () => paintChrome(applyPalette(getColorScheme()))
    paint()
    const offTheme = onColorSchemeChanged(paint)
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
    return offTheme
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
  if (routers.length === 0) return <NoAccess />

  const overlay = nav.overlay === 'fleet'
    ? (
      <FleetOverlay
        routers={routers}
        currentID={nav.routerID}
        onPick={(id) => dispatch({ type: 'router', id })}
        onClose={() => dispatch({ type: 'overlay', overlay: null })}
      />
    )
    : nav.overlay === 'admin' && nav.routerID != null
      ? <AdminOverlay routerID={nav.routerID} onClose={() => dispatch({ type: 'overlay', overlay: null })} />
      : null

  return (
    <>
      <Header fleetVisible={routers.length > 1} onFleet={() => dispatch({ type: 'overlay', overlay: 'fleet' })} />
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
        ) : (
          <p class="state">Скоро</p>
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
