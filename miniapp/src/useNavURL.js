import { useEffect, useRef } from 'preact/hooks'
import { navFromURL, urlFromNav } from './navUrl.js'
import { navPinned, navReducer, localLayerDepth, LOCAL_LAYERS } from './nav.js'
import { loadLastRouter } from './routerPick.js'

// Запись-метка слоя без адреса в истории браузера: только номер по глубине.
// Ни параметров слоя, ни паролей, ни содержимого конфига в истории нет.
const MARK = 'wgmLayer'
const markHere = () => Number(window.history.state?.[MARK]) || 0
const here = () => window.location.pathname + window.location.search
// Сколько ждать popstate от собственного history.go(): если перехода не было
// (записей под нами нет), следующий настоящий «назад» не должен пропасть.
const SELF_GO_MS = 1000

// Сколько записей-меток держит состояние: слои без адреса и лист поверх
// (A1.4) -- «назад» браузера закрывает лист, а не уводит с места целиком.
const markDepth = (state) => localLayerDepth(state) + (state?.sheet ? 1 : 0)

// Навигация веб-управления живёт в адресе: «назад» браузера, обновление
// страницы и закладки. В Telegram (enabled=false) адрес не трогаем вовсе.
//
// Первая синхронизация -- replaceState: открытие страницы не должно
// оставлять в истории лишнюю запись, по которой «назад» ведёт на то же самое.
// Она же переводит /dashboard/login в /dashboard/ после входа.
//
// Слои без адреса (экран VPN-туннеля, «Заменить конфиг», загрузка .conf, ход
// починки, слои «Маршрутов» и выпуск конфига) адрес не меняют -- и «назад»
// браузера уводил с места целиком. Теперь на каждый такой слой в истории
// лежит запись-метка с тем же адресом: «назад» закрывает слой тем же действием
// back, что кнопка Telegram. Слой, закрытый кнопкой приложения, свою метку
// снимает сам (history.go назад), а «вперёд» и обновление страницы слой не
// воскрешают: его параметры в историю не пишутся, и запись-метка без слоя
// просто отматывается к месту-родителю.
export function useNavURL({ enabled, nav, dispatch, routerIDs = [], routers = null, isAdmin = false, basePath = '/dashboard/' }) {
  const synced = useRef(false)
  const idsRef = useRef(routerIDs)
  idsRef.current = routerIDs
  // N1: «назад» на голый адрес выбирает главный экран так же, как старт, --
  // по роутеру в беде, а не по первому в списке.
  const routersRef = useRef(routers)
  routersRef.current = routers
  const adminRef = useRef(isAdmin)
  adminRef.current = isAdmin
  const navRef = useRef(nav)
  navRef.current = nav
  // depth -- сколько записей-меток лежит в истории под текущей записью
  // включительно; selfGo -- свои переходы history.go(), чей popstate не событие.
  const depth = useRef(0)
  const selfGo = useRef({ n: 0, until: 0 })

  // Привести записи-метки к числу открытых слоёв без адреса.
  function reconcile(state) {
    const want = markDepth(state)
    const have = depth.current
    if (want > have) {
      for (let i = have + 1; i <= want; i++) window.history.pushState({ [MARK]: i }, '', here())
    } else if (want < have) {
      selfGo.current = { n: selfGo.current.n + 1, until: Date.now() + SELF_GO_MS }
      window.history.go(want - have)
    }
    depth.current = want
  }

  useEffect(() => {
    if (!enabled) {
      synced.current = false
      depth.current = 0
      return
    }
    // Страницу обновили на записи-метке: слоя уже нет, метка отматывается.
    if (!synced.current) depth.current = markHere()
    const next = basePath + urlFromNav(nav)
    if (next !== here()) {
      if (synced.current) window.history.pushState(null, '', next)
      else window.history.replaceState(null, '', next)
      // Новая запись с адресом: меток над ней ещё нет.
      depth.current = 0
    }
    synced.current = true
    reconcile(nav)
  }, [enabled, nav.routerID, nav.tab, nav.overlay, nav.diagView, nav.overlayParams, nav.sheet])

  useEffect(() => {
    if (!enabled) return undefined
    // «Назад»/«вперёд» браузера: навигация берётся из адреса, а нормализованный
    // адрес (удалённый роутер, старый tab=routes) пишется сразу заменой. Иначе
    // эффект выше счёл бы его новым местом и сделал pushState -- а новая запись
    // стирает историю «вперёд».
    const onPop = () => {
      if (selfGo.current.n > 0 && Date.now() <= selfGo.current.until) {
        selfGo.current = { ...selfGo.current, n: selfGo.current.n - 1 }
        // Свой переход мог приземлиться на запись другого места: закреплённый
        // слой возвращает метку поверх той записи, куда ушёл «назад» (Chrome
        // пропускает записи без жеста, есть и меню истории). Экран уже верный --
        // адрес приводится к нему заменой, без новой записи.
        const url = basePath + urlFromNav(navRef.current)
        if (url !== here()) window.history.replaceState(window.history.state, '', url)
        return
      }
      selfGo.current = { n: 0, until: 0 }
      const cur = navRef.current
      const mark = markHere()
      // Закреплённый слой (мастер во время отправки, выпуск конфига, раскатка
      // бэкенда) не отпускает и «назад» браузера: место остаётся, запись
      // возвращается -- вместе с меткой, если слой без адреса.
      if (navPinned(cur)) {
        const local = markDepth(cur) > 0
        window.history.pushState(local ? { [MARK]: mark + 1 } : null, '', basePath + urlFromNav(cur))
        depth.current = local ? mark + 1 : 0
        return
      }
      // «Вперёд» на запись-метку того же места (лист или слой закрыли кнопкой,
      // метку сняли «назад»): навигация остаётся как есть -- иначе адрес
      // пересобрал бы место и потерял цель возврата. Метка отматывается.
      if (mark > depth.current && here() === basePath + urlFromNav(cur)) {
        depth.current = mark
        reconcile(cur)
        return
      }
      // «Назад» с записи-метки: закрыть слой без адреса тем же back, что кнопка
      // Telegram (лист поверх слоя закрывается первым -- reconcile вернёт метку).
      if ((LOCAL_LAYERS.includes(cur.overlay) || cur.sheet) && mark < depth.current && here() === basePath + urlFromNav(cur)) {
        let next = cur
        for (let i = depth.current - mark; i > 0; i--) {
          next = navReducer(next, { type: 'back' })
          dispatch({ type: 'back' })
        }
        depth.current = mark
        const url = basePath + urlFromNav(next)
        if (url !== here()) window.history.replaceState(window.history.state, '', url)
        reconcile(next)
        return
      }
      // «Назад» со слоя с адресом, открытого из слоя без адреса («Маршруты» с
      // экрана VPN-туннеля): запись под ним -- метка того слоя. Возврат берётся
      // из самой навигации (returnTo/returnParams) -- тем же back, что кнопка
      // приложения; в истории по-прежнему только номер метки. После обновления
      // страницы returnTo нет -- и «назад» ведёт к месту-родителю, как раньше.
      if (mark > 0 && !LOCAL_LAYERS.includes(cur.overlay) && !cur.sheet) {
        const back = navReducer(cur, { type: 'back' })
        if (back !== cur && localLayerDepth(back) === mark && here() === basePath + urlFromNav(back)) {
          dispatch({ type: 'back' })
          depth.current = mark
          return
        }
      }
      const state = navFromURL(window.location.search, idsRef.current, { isAdmin: adminRef.current, routers: routersRef.current, lastID: loadLastRouter() })
      const next = basePath + urlFromNav(state)
      if (next !== here()) window.history.replaceState(window.history.state, '', next)
      dispatch({ type: 'init', state, source: 'popstate' })
      // Запись-метка без слоя («вперёд» или «назад» на неё): слой из истории не
      // восстанавливается -- метка отматывается к месту-родителю.
      depth.current = mark
      reconcile(state)
    }
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [enabled, basePath])
}
