// Выбор роутера (v0.52, спека §4). Число роутеров считается по доступным
// этому человеку -- тот же список GET /routers, что у прав (сервер уже отобрал
// по miniappRouterAllowed). Админу полосы нет никогда: у него Парк.
import { fleetRow, redAlert, sortByUrgency } from './fleet.js'
import { reachStatus } from './staleness.js'

export const STRIP_MAX = 5

export function routerPickMode({ count = 0, isAdmin = false } = {}) {
  if (count <= 1) return 'none'
  if (isAdmin) return 'list'
  return count <= STRIP_MAX ? 'strip' : 'list'
}

// Слова состояния -- словарь Парка: «в порядке», «тревога», «молчит».
function stateWord(router) {
  const s = reachStatus(router)
  if (router?.last_seen_age_sec == null || s === 'offline' || s === 'sleeping') return 'молчит'
  // Тревога только по запасному звену -- то же слово, что у пилюли списка.
  if (s === 'alert') return router?.reserve_only_alert ? fleetRow(router).pill.text : 'тревога'
  return 'в порядке'
}

const byName = (a, b) => (a.nickname ?? '').localeCompare(b.nickname ?? '', 'ru')

export function stripChips(routers = [], currentID = null) {
  const red = routers.filter(redAlert).sort(byName)
  const rest = routers.filter((r) => !redAlert(r)).sort(byName)
  return [...red, ...rest].map((r) => ({
    id: r.id,
    name: r.nickname ?? '',
    tone: fleetRow(r).pill.tone,
    state: stateWord(r),
    // Красная тревога -- по состоянию, а не по тону: у молчащего роутера тон
    // тоже danger, но чинить нечем (N2).
    alert: redAlert(r),
    current: r.id === currentID,
  }))
}

export function otherAlertRouter(routers = [], currentID = null) {
  const hit = sortByUrgency(routers).find((r) => r.id !== currentID && redAlert(r))
  return hit ? { id: hit.id, nickname: hit.nickname ?? '' } : null
}

export function landingRouterID({ routerIDs = [], routers = null, lastID = null } = {}) {
  const mine = (routers ?? []).filter((r) => routerIDs.includes(r.id))
  const red = sortByUrgency(mine).find(redAlert)
  if (red) return red.id
  if (lastID != null && routerIDs.includes(lastID)) return lastID
  return routerIDs[0] ?? null
}

// Последний открытый роутер -- удобство одного зрителя: хранилище может быть
// недоступно (приватное окно, запрет), тогда просто null.
export const LAST_ROUTER_KEY = 'wgm.lastRouterID'

export function loadLastRouter() {
  try {
    const raw = globalThis.localStorage?.getItem(LAST_ROUTER_KEY)
    const id = raw == null ? NaN : Number(raw)
    return Number.isInteger(id) && id > 0 ? id : null
  } catch {
    return null
  }
}

export function saveLastRouter(id) {
  try {
    if (Number.isInteger(id) && id > 0) globalThis.localStorage?.setItem(LAST_ROUTER_KEY, String(id))
  } catch {
    // хранилище закрыто -- главный экран просто не вспомнит последний
  }
}
