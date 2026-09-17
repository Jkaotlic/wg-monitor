import { useEffect, useRef } from 'preact/hooks'
import { navFromURL, urlFromNav } from './navUrl.js'
import { navPinned } from './nav.js'

// Навигация веб-управления живёт в адресе: «назад» браузера, обновление
// страницы и закладки. В Telegram (enabled=false) адрес не трогаем вовсе.
//
// Первая синхронизация -- replaceState: открытие страницы не должно
// оставлять в истории лишнюю запись, по которой «назад» ведёт на то же самое.
// Она же переводит /dashboard/login в /dashboard/ после входа.
export function useNavURL({ enabled, nav, dispatch, routerIDs = [], isAdmin = false, basePath = '/dashboard/' }) {
  const synced = useRef(false)
  const idsRef = useRef(routerIDs)
  idsRef.current = routerIDs
  const adminRef = useRef(isAdmin)
  adminRef.current = isAdmin
  const navRef = useRef(nav)
  navRef.current = nav

  useEffect(() => {
    if (!enabled) {
      synced.current = false
      return
    }
    const next = basePath + urlFromNav(nav)
    const now = window.location.pathname + window.location.search
    if (next !== now) {
      if (synced.current) window.history.pushState(null, '', next)
      else window.history.replaceState(null, '', next)
    }
    synced.current = true
  }, [enabled, nav.routerID, nav.tab, nav.overlay])

  useEffect(() => {
    if (!enabled) return undefined
    // «Назад»/«вперёд» браузера: навигация берётся из адреса, а нормализованный
    // адрес (удалённый роутер, старый tab=routes) пишется сразу заменой. Иначе
    // эффект выше счёл бы его новым местом и сделал pushState -- а новая запись
    // стирает историю «вперёд».
    const onPop = () => {
      // Закреплённый слой (мастер во время отправки, раскатка бэкенда) не
      // отпускает и «назад» браузера: место остаётся, адрес возвращается.
      if (navPinned(navRef.current)) {
        window.history.pushState(null, '', basePath + urlFromNav(navRef.current))
        return
      }
      const state = navFromURL(window.location.search, idsRef.current, { isAdmin: adminRef.current })
      const next = basePath + urlFromNav(state)
      if (next !== window.location.pathname + window.location.search) window.history.replaceState(null, '', next)
      dispatch({ type: 'init', state, source: 'popstate' })
    }
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [enabled, basePath])
}
