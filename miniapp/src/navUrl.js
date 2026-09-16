import { initialNav, normalizeTab, deepLinkOverlay, TABS, OPEN_OVERLAYS } from './nav.js'

// Адрес веб-управления -- то же, что deep-link из тревоги, плюс вкладка:
// ?router=<id>&tab=<tab>&open=<overlay>. Лист подтверждения в адрес не
// попадает никогда: обновление страницы не должно заново спрашивать «точно
// перезагрузить?» -- и тем более не должно выглядеть так, будто спрашивает.
export function navFromURL(search, routerIDs = []) {
  const params = new URLSearchParams(search ?? '')
  const raw = params.get('router')
  const id = raw ? Number(raw) : NaN
  const state = initialNav({ routerIDs, deepLinkID: Number.isFinite(id) ? id : null })
  if (state.routerID == null) return state
  const tab = normalizeTab(params.get('tab'))
  if (TABS.includes(tab)) state.tab = tab
  state.overlay = deepLinkOverlay(search ?? '', state)
  return state
}

export function urlFromNav(nav) {
  if (nav?.routerID == null) return ''
  const params = new URLSearchParams()
  params.set('router', String(nav.routerID))
  if (nav.tab && nav.tab !== 'router') params.set('tab', nav.tab)
  if (OPEN_OVERLAYS.includes(nav.overlay)) params.set('open', nav.overlay)
  return '?' + params.toString()
}
