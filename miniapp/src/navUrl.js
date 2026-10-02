import { initialNav, normalizeTab, diagViewFor, deepLinkOverlay, TABS, PARK_TAB, OPEN_OVERLAYS, URL_FLEET_OVERLAYS, OVERLAY_TABS, MANAGE_FOCUS } from './nav.js'

// Адрес веб-управления -- то же, что deep-link из тревоги, плюс вкладка:
// ?router=<id>&tab=<tab>&open=<overlay>. Лист подтверждения в адрес не
// попадает никогда: обновление страницы не должно заново спрашивать «точно
// перезагрузить?» -- и тем более не должно выглядеть так, будто спрашивает.
// isAdmin -- слои парка с адресом (свои серверы) открываются только админу:
// остальным сервер ответит 404, и адрес ведёт на обычный экран.
export function navFromURL(search, routerIDs = [], { isAdmin = false, routers = null, lastID = null } = {}) {
  const params = new URLSearchParams(search ?? '')
  const raw = params.get('router')
  const id = raw ? Number(raw) : NaN
  const state = initialNav({ routerIDs, deepLinkID: Number.isFinite(id) ? id : null, isAdmin, routers, lastID })
  const rawTab = params.get('tab')
  const tab = normalizeTab(rawTab)
  const open = params.get('open')
  // Вкладка «Парк» (?tab=park, v0.48) -- только админу, с роутером и без:
  // Парк от роутера не зависит. Остальным адрес ведёт на обычный экран.
  const park = isAdmin && tab === PARK_TAB
  // Слой парка с адресом («Серверы»: свои VPS и панели) открывается и без роутера.
  // Возврат -- во вкладку Парка, если адрес про неё; иначе к списку
  // роутеров, если роутер выбран, иначе к сводке.
  if (isAdmin && URL_FLEET_OVERLAYS.includes(open)) {
    if (park) state.tab = PARK_TAB
    else if (state.routerID != null && TABS.includes(tab)) {
      state.tab = tab
      if (diagViewFor(rawTab)) state.diagView = 'history'
    }
    state.overlay = open
    state.overlayParams = { returnTo: park || state.tab === PARK_TAB ? PARK_TAB : state.routerID != null ? 'fleet' : null }
    return state
  }
  if (park) {
    state.tab = PARK_TAB
    state.overlay = null
    return state
  }
  if (state.routerID == null) return state
  if (TABS.includes(tab)) {
    state.tab = tab
    if (diagViewFor(rawTab)) state.diagView = 'history'
  }
  // Настройки и «Обслуживание» стали вкладкой «Управление»: старая ссылка из
  // уведомления открывает её, а не пустой экран.
  if (OVERLAY_TABS[open]) {
    state.tab = OVERLAY_TABS[open]
    if (MANAGE_FOCUS[open]) state.manageFocus = MANAGE_FOCUS[open]
    return state
  }
  state.overlay = deepLinkOverlay(search ?? '', state)
  // «Маршруты» -- слой вкладки «VPN-туннели» (подпись «назад» -- она): ссылка
  // без вкладки, а равно и со «Роутером», встаёт на неё, иначе «назад» вёл бы
  // на другую вкладку, чем обещает подпись.
  if (state.overlay === 'routes') state.tab = 'tunnels'
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
  if (nav?.routerID == null) {
    const q = new URLSearchParams()
    if (nav?.tab === PARK_TAB) q.set('tab', PARK_TAB)
    if (URL_FLEET_OVERLAYS.includes(open)) q.set('open', open)
    const str = q.toString()
    return str ? `?${str}` : ''
  }
  const params = new URLSearchParams()
  params.set('router', String(nav.routerID))
  if (nav.tab && nav.tab !== 'router') params.set('tab', nav.tab === 'diag' && nav.diagView === 'history' ? 'events' : nav.tab)
  if (open) params.set('open', open)
  return '?' + params.toString()
}
