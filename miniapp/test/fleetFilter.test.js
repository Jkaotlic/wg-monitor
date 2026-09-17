import { describe, it, expect } from 'vitest'
import {
  FLEET_FILTERS,
  filterBucket,
  matchesQuery,
  applyFleetFilter,
  isFilterShortcut,
  emptyFilterText,
} from '../src/fleetFilter.js'

const R = [
  { id: 1, nickname: 'dom-kiev', status: 'alert', last_seen_age_sec: 10, agent_version: 'v0.35.0', kind: 'static' },
  { id: 2, nickname: 'car‑bmw', status: 'sleeping', last_seen_age_sec: 4000, agent_version: 'v0.34.1', kind: 'mobile' },
  { id: 3, nickname: 'office', status: 'online', last_seen_age_sec: 30 },
  { id: 4, nickname: 'dacha', status: 'offline', last_seen_age_sec: 90000 },
  { id: 5, nickname: 'new-one', status: 'online' },
]

describe('фильтры списка роутеров', () => {
  it('пять фильтров в порядке спеки', () => {
    expect(FLEET_FILTERS.map((f) => f.label)).toEqual(['все', 'тревога', 'на связи', 'спят', 'молчат'])
    expect(FLEET_FILTERS.map((f) => f.key)).toEqual(['all', 'alert', 'online', 'sleeping', 'silent'])
  })

  it('роутер без единого отчёта -- «молчат», даже если статус online', () => {
    expect(R.map(filterBucket)).toEqual(['alert', 'sleeping', 'online', 'silent', 'silent'])
  })
})

describe('поиск', () => {
  it('по имени без учёта регистра и вида дефиса', () => {
    expect(matchesQuery(R[0], 'DOM')).toBe(true)
    expect(matchesQuery(R[1], 'car-bmw')).toBe(true)
    expect(matchesQuery(R[2], 'dom')).toBe(false)
  })

  it('по версии агента и по типу -- и по-английски, и по-русски', () => {
    expect(matchesQuery(R[1], 'v0.34')).toBe(true)
    expect(matchesQuery(R[1], 'mobile')).toBe(true)
    expect(matchesQuery(R[1], 'мобильный')).toBe(true)
    expect(matchesQuery(R[0], 'дома')).toBe(true)
    expect(matchesQuery(R[0], 'v0.36')).toBe(false)
  })

  it('несколько слов -- каждое должно найтись', () => {
    expect(matchesQuery(R[0], 'dom v0.35')).toBe(true)
    expect(matchesQuery(R[0], 'dom v0.34')).toBe(false)
  })

  it('пустой запрос пропускает всех', () => {
    expect(R.every((r) => matchesQuery(r, '   '))).toBe(true)
  })
})

describe('applyFleetFilter', () => {
  it('счётчики без поиска', () => {
    const v = applyFleetFilter(R, { query: '', filter: 'all' })
    expect(v.counts).toEqual({ all: 5, alert: 1, online: 1, sleeping: 1, silent: 2 })
    // Внутри «на связи» -- по имени: «new-one» раньше «office».
    expect(v.visible.map((r) => r.id)).toEqual([1, 4, 2, 5, 3])
    expect(v.active).toBe(false)
  })

  it('фильтр «молчат» -- по срочности', () => {
    const v = applyFleetFilter(R, { query: '', filter: 'silent' })
    expect(v.visible.map((r) => r.id)).toEqual([4, 5])
    expect(v.active).toBe(true)
  })

  it('счётчики считаются по найденному поиском', () => {
    const v = applyFleetFilter(R, { query: 'v0.3', filter: 'all' })
    expect(v.counts).toEqual({ all: 2, alert: 1, online: 0, sleeping: 1, silent: 0 })
    expect(v.active).toBe(true)
  })

  it('незнакомый фильтр -- «все»', () => {
    const v = applyFleetFilter(R, { query: '', filter: 'broken' })
    expect(v.filter).toBe('all')
    expect(v.visible).toHaveLength(5)
  })

  it('пустой список не падает', () => {
    expect(applyFleetFilter(undefined, {}).counts.all).toBe(0)
  })
})

describe('«/» ставит курсор в поиск', () => {
  const div = { tagName: 'DIV' }
  it('только чистый «/» и не во время набора', () => {
    expect(isFilterShortcut({ key: '/', target: div })).toBe(true)
    expect(isFilterShortcut({ key: '/', target: { tagName: 'INPUT' } })).toBe(false)
    expect(isFilterShortcut({ key: '/', target: { tagName: 'TEXTAREA' } })).toBe(false)
    expect(isFilterShortcut({ key: '/', target: { tagName: 'DIV', isContentEditable: true } })).toBe(false)
    expect(isFilterShortcut({ key: '/', ctrlKey: true, target: div })).toBe(false)
    expect(isFilterShortcut({ key: '/', metaKey: true, target: div })).toBe(false)
    expect(isFilterShortcut({ key: 'a', target: div })).toBe(false)
  })
})

describe('пустой результат словами', () => {
  it('по поиску -- с запросом, по фильтру -- без', () => {
    expect(emptyFilterText({ query: ' bmw ', filter: 'all' })).toBe('Ничего не нашлось по «bmw».')
    expect(emptyFilterText({ query: '', filter: 'alert' })).toBe('Таких роутеров сейчас нет.')
  })
})
