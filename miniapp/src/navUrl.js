import { initialNav, normalizeTab, deepLinkOverlay, TABS, OPEN_OVERLAYS, URL_FLEET_OVERLAYS } from './nav.js'

// Адрес веб-управления -- то же, что deep-link из тревоги, плюс вкладка:
// ?router=<id>&tab=<tab>&open=<overlay>. Лист подтверждения в адрес не
// попадает никогда: обновление страницы не должно заново спрашивать «точно
// перезагрузить?» -- и тем более не должно выглядеть так, будто спрашивает.
// isAdmin -- слои парка с адресом (свои серверы) открываются только админу:
// остальным сервер ответит 404, и адрес ведёт на обычный экран.
export function navFromURL(search, routerIDs = [], { isAdmin = false } = {}) {
  const params = new URLSearchParams(search ?? '')
  const raw = params.get('router')
  const id = raw ? Number(raw) : NaN
  const state = initialNav({ routerIDs, deepLinkID: Number.isFinite(id) ? id : null })
  const tab = normalizeTab(params.get('tab'))
  const open = params.get('open')
  // Слой парка с адресом («Свои VPN-серверы») открывается и без роутера.
  // Возврат -- в «Обслуживание», если роутер выбран (Парк живёт там), иначе
  // к сводке.
  if (isAdmin && URL_FLEET_OVERLAYS.includes(open)) {
    if (state.routerID != null && TABS.includes(tab)) state.tab = tab
    state.overlay = open
    state.overlayParams = { returnTo: state.routerID != null ? 'admin' : null }
    return state
  }
  if (state.routerID == null) return state
  if (TABS.includes(tab)) state.tab = tab
  state.overlay = deepLinkOverlay(search ?? '', state)
  return state
}

// Слой, который пишется в адрес: сам открытый слой, если у него есть адрес;
// иначе -- слой, откуда его открыли (мастер, ход работы, экран сервера):
// обновление страницы вернёт туда, а открытие не добавит запись в историю.
function addressLayer(nav) {
  const listed = (o) => OPEN_OVERLAYS.includes(o) || URL_FLEET_OVERLAYS.includes(o)
  if (listed(nav?.overlay)) return nav.overlay
  const back = nav?.overlayParams?.returnTo
  return listed(back) ? back : null
}

export function urlFromNav(nav) {
  const open = addressLayer(nav)
  if (nav?.routerID == null) return URL_FLEET_OVERLAYS.includes(open) ? `?open=${open}` : ''
  const params = new URLSearchParams()
  params.set('router', String(nav.routerID))
  if (nav.tab && nav.tab !== 'router') params.set('tab', nav.tab)
  if (open) params.set('open', open)
  return '?' + params.toString()
}
