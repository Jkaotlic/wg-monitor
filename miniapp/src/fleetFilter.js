// Поиск и фильтры списка роутеров: боковая колонка широкой раскладки и
// «Мои роутеры» на телефоне. Только клиент: список уже отдан человеку, и
// фильтр ничего не открывает, а только прячет лишнее.
//
// Состояние в адрес не пишется (спека, п. 11): закладка на «молчащих» через
// неделю показывала бы других роутеров под тем же словом.
import { sortByUrgency } from './fleet.js'
import { plainHyphens } from './text.js'

export const FLEET_FILTERS = [
  { key: 'all', label: 'все' },
  { key: 'alert', label: 'тревога' },
  { key: 'online', label: 'на связи' },
  { key: 'sleeping', label: 'спят' },
  { key: 'silent', label: 'молчат' },
]

const KEYS = new Set(FLEET_FILTERS.map((f) => f.key))

// Тип роутера ищется и словом человека: в интерфейсе он «дома» и «в машине»,
// а в данных -- static и mobile.
const KIND_WORDS = {
  static: 'static стационарный дома',
  mobile: 'mobile мобильный в машине',
}

// Роутер без единого отчёта -- «молчит», какой бы статус ни стоял: то же
// правило, что у сводки широкого экрана (fleet.js, bucket).
export function filterBucket(router) {
  if (router?.last_seen_age_sec == null) return 'silent'
  switch (router.status) {
    case 'alert':
      return 'alert'
    case 'online':
      return 'online'
    case 'sleeping':
      return 'sleeping'
    default:
      return 'silent'
  }
}

function norm(text) {
  return plainHyphens(String(text ?? '')).trim().toLowerCase()
}

function haystack(router) {
  const kind = router?.kind ? KIND_WORDS[router.kind] ?? router.kind : ''
  return [router?.nickname, router?.agent_version, kind].filter(Boolean).map(norm).join(' ')
}

export function matchesQuery(router, query) {
  const q = norm(query)
  if (!q) return true
  const hay = haystack(router)
  return q.split(/\s+/).every((word) => hay.includes(word))
}

export function applyFleetFilter(routers, { query = '', filter = 'all' } = {}) {
  const key = KEYS.has(filter) ? filter : 'all'
  const matched = (routers ?? []).filter((r) => matchesQuery(r, query))
  const counts = { all: matched.length, alert: 0, online: 0, sleeping: 0, silent: 0 }
  for (const r of matched) counts[filterBucket(r)]++
  const visible = key === 'all' ? matched : matched.filter((r) => filterBucket(r) === key)
  return { visible: sortByUrgency(visible), counts, filter: key, active: key !== 'all' || norm(query) !== '' }
}

function typing(el) {
  return Boolean(el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT' || el.isContentEditable))
}

export function isFilterShortcut(e) {
  return e?.key === '/' && !e.ctrlKey && !e.metaKey && !e.altKey && !typing(e.target)
}

export function emptyFilterText({ query = '', filter = 'all' } = {}) {
  const q = String(query ?? '').trim()
  if (q) return `Ничего не нашлось по «${q}».`
  return 'Таких роутеров сейчас нет.'
}
