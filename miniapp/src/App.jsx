import { useEffect, useReducer, useState } from 'preact/hooks'
import { initTelegram, onBackButtonClick, paintChrome, setBackButtonVisible } from './telegram.js'
import { applyPalette } from './theme.js'
import { setUnauthorizedHandler } from './api.js'
import { initialNav, navReducer, backButtonVisible, escapeAction } from './nav.js'
import { navFromURL } from './navUrl.js'
import { useNavURL } from './useNavURL.js'
import { appMode } from './mode.js'
import { takeHashToken } from './login.js'
import { useWide } from './useWide.js'
import { AppContext } from './appContext.js'
import { useBoot } from './useBoot.js'
import { NoAccess } from './screens/NoAccess.jsx'
import { LoginScreen } from './screens/LoginScreen.jsx'
import { ServerDown } from './ui/ServerDown.jsx'
import { PhoneLayout } from './ui/PhoneLayout.jsx'
import { WideLayout } from './ui/WideLayout.jsx'
import { PULSE_MS } from './pulse.js'

// Поле ввода -- не место для Esc-закрытия слоя: человек набирает маршрут или
// имя и теряет набранное одним промахом. Лист подтверждения ловит Esc сам.
function typingTarget(el) {
  return Boolean(el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT'))
}

export function App() {
  // Режим не меняется за жизнь страницы: /dashboard и /miniapp -- разные входы.
  const mode = appMode(window.location.pathname)
  const wide = useWide()
  // Токен личной ссылки снимается с адреса синхронно, в первом рендере -- до
  // любого запроса. Иначе при молчащем сервере он висел бы в адресе и
  // истории, пока не откроется экран входа. Дальше живёт только в памяти.
  const [linkToken, setLinkToken] = useState(() => (mode === 'web' ? takeHashToken() : ''))
  const [nav, dispatch] = useReducer(navReducer, initialNav())
  const boot = useBoot(mode, {
    onReady: (list, info) => dispatch({ type: 'init', state: navFromURL(window.location.search, list.map((r) => r.id), { isAdmin: info?.isAdmin === true }) }),
  })
  const routerIDs = boot.routers.map((r) => r.id)

  useEffect(() => {
    initTelegram()
    // Тема одна, поэтому палитра применяется один раз и подписки на смену
    // схемы больше нет: Telegram может сколько угодно переключаться между
    // светлой и тёмной -- приложение остаётся тёмным намеренно.
    paintChrome(applyPalette())
    boot.start()
  }, [])

  useNavURL({ enabled: mode === 'web' && boot.status === 'ready', nav, dispatch, routerIDs, isAdmin: boot.isAdmin })

  // 401 посреди работы в браузере -- истекла кука: на экран входа, место в
  // адресе остаётся, после входа useBoot откроет его снова.
  useEffect(() => {
    if (mode !== 'web' || boot.status !== 'ready') return undefined
    return setUnauthorizedHandler(() => boot.expire())
  }, [mode, boot.status])

  // Колонка роутеров на широком экране видна всегда, и слова состояния в ней
  // не должны застывать на моменте входа: список переспрашивается тем же
  // пульсом, что экран роутера, и только пока вкладка браузера видна.
  useEffect(() => {
    if (!wide || boot.status !== 'ready') return undefined
    const timer = setInterval(() => {
      if (document.visibilityState === 'visible') boot.refreshRouters()
    }, PULSE_MS)
    return () => clearInterval(timer)
  }, [wide, boot.status])

  // Кнопкой "назад" владеет оболочка, а не экраны: слоёв несколько, кнопка
  // одна, и порядок их закрытия описан в navReducer.
  useEffect(() => {
    setBackButtonVisible(backButtonVisible(nav, { wide }))
    return onBackButtonClick(() => dispatch({ type: 'back' }))
  }, [nav.overlay, nav.sheet, wide])

  useEffect(() => {
    const onKey = (e) => {
      if (e.key !== 'Escape' || typingTarget(e.target)) return
      const action = escapeAction(nav, { wide })
      if (action) dispatch(action)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [nav.overlay, nav.sheet, wide])

  let body
  if (boot.status === 'loading') {
    body = <p class="state">Загрузка…</p>
  } else if (boot.status === 'error') {
    body = <p class="state state-error">Не удалось войти. Откройте mini-app из Telegram заново.</p>
  } else if (boot.status === 'down') {
    body = <ServerDown onRetry={() => boot.start()} />
  } else if (boot.status === 'login') {
    body = (
      <LoginScreen
        notice={boot.notice}
        linkToken={linkToken}
        onLinkUsed={() => setLinkToken('')}
        onSuccess={() => boot.start({ afterLogin: true })}
      />
    )
  } else if (boot.routers.length === 0) {
    // Доступ мог появиться, пока приложение было открыто: экран пустого доступа
    // умеет переспросить, и тогда оболочка продолжает как при обычном входе.
    body = (
      <NoAccess
        telegramUserID={boot.telegramUserID}
        onRetry={(list) => {
          dispatch({ type: 'init', state: navFromURL(window.location.search, list.map((r) => r.id), { isAdmin: boot.isAdmin }) })
          boot.setRouters(list)
        }}
      />
    )
  } else {
    const layout = {
      nav,
      dispatch,
      routers: boot.routers,
      isAdmin: boot.isAdmin,
      onLogout: mode === 'web' ? () => boot.logout() : undefined,
      // Слои парка переспрашивают список роутеров: после установки агента
      // новый роутер должен появиться в колонке и открыться по кнопке.
      refreshRouters: () => boot.refreshRouters(),
    }
    body = wide ? <WideLayout mode={mode} {...layout} /> : <PhoneLayout {...layout} />
  }

  return <AppContext.Provider value={{ mode, wide }}>{body}</AppContext.Provider>
}
