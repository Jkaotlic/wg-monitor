import { describe, it, expect, beforeEach } from 'vitest'
import { STRIP_MAX, routerPickMode, landingRouterID, stripChips, otherAlertRouter, loadLastRouter, saveLastRouter, LAST_ROUTER_KEY } from '../src/routerPick.js'
import { initialNav } from '../src/nav.js'
import { fleetRow, redAlert } from '../src/fleet.js'
import { parkRank } from '../src/fleetAdmin.js'

const R = (id, nickname, extra = {}) => ({ id, nickname, status: 'online', reach: 'online', last_seen_age_sec: 30, ...extra })
const ALERT = { status: 'alert', reach: 'online' }

describe('режим выбора роутера', () => {
  it('1 / 2–5 / 6+ по числу доступных', () => {
    expect(STRIP_MAX).toBe(5)
    expect(routerPickMode({ count: 1, isAdmin: false })).toBe('none')
    expect(routerPickMode({ count: 2, isAdmin: false })).toBe('strip')
    expect(routerPickMode({ count: 5, isAdmin: false })).toBe('strip')
    expect(routerPickMode({ count: 6, isAdmin: false })).toBe('list')
  })
  it('админу полосы нет никогда', () => {
    expect(routerPickMode({ count: 1, isAdmin: true })).toBe('none')
    expect(routerPickMode({ count: 3, isAdmin: true })).toBe('list')
  })
})

describe('чипы полосы', () => {
  it('тревога первой и окрашена, остальные по имени; текущий помечен', () => {
    const chips = stripChips([R(1, 'дача-северная'), R(2, 'router4car4new', ALERT), R(3, 'home', { reach: 'offline', status: 'offline', last_seen_age_sec: 7200 })], 1)
    expect(chips.map((c) => c.id)).toEqual([2, 1, 3])
    expect(chips[0]).toMatchObject({ name: 'router4car4new', tone: 'danger', state: 'тревога', current: false })
    expect(chips[1]).toMatchObject({ state: 'в порядке', current: true })
    expect(chips[2]).toMatchObject({ state: 'молчит' })
  })
  it('тревога только по запасному -- не первая и не красная', () => {
    const chips = stripChips([R(1, 'a'), R(2, 'b', { ...ALERT, reserve_only_alert: true })], 1)
    expect(chips.map((c) => c.id)).toEqual([1, 2])
    expect(chips[1].tone).toBe('warn')
  })
})

describe('единый предикат красной тревоги и слово состояния', () => {
  it('redAlert: тревога не по запасному и не молчащая', () => {
    expect(redAlert(R(1, 'a', ALERT))).toBe(true)
    expect(redAlert(R(1, 'a', { ...ALERT, reserve_only_alert: true }))).toBe(false)
    expect(redAlert(R(1, 'a', { status: 'alert', reach: 'offline', last_seen_age_sec: 7200 }))).toBe(false)
    expect(redAlert(R(1, 'a'))).toBe(false)
  })
  it('запасной: слово чипа -- то же, что у пилюли списка', () => {
    const r = R(2, 'b', { ...ALERT, reserve_only_alert: true })
    expect(stripChips([R(1, 'a'), r], 1)[1].state).toBe(fleetRow(r).pill.text)
  })
  it('Парк: карточка с тревогой только по запасному -- не первая', () => {
    expect(parkRank(R(2, 'b', { ...ALERT, reserve_only_alert: true }))).toBeGreaterThan(parkRank(R(1, 'a', ALERT)))
  })
})

describe('тревога на другом роутере', () => {
  it('есть -- первый по срочности, кроме текущего', () => {
    expect(otherAlertRouter([R(1, 'a', ALERT), R(2, 'b', ALERT)], 1)).toEqual({ id: 2, nickname: 'b' })
  })
  it('нет -- null; тревога на текущем и молчащая тревога не в счёт', () => {
    expect(otherAlertRouter([R(1, 'a', ALERT), R(2, 'b')], 1)).toBe(null)
    expect(otherAlertRouter([R(1, 'a'), R(2, 'b', { status: 'alert', reach: 'offline' })], 1)).toBe(null)
  })
})

describe('главный экран', () => {
  const routers = [R(1, 'a'), R(2, 'b', ALERT), R(3, 'c')]
  it('2–5: роутер в тревоге, иначе последний открытый, иначе первый', () => {
    expect(landingRouterID({ routerIDs: [1, 2, 3], routers, lastID: 3 })).toBe(2)
    expect(landingRouterID({ routerIDs: [1, 3], routers, lastID: 3 })).toBe(3)
    expect(landingRouterID({ routerIDs: [1, 3], routers, lastID: 99 })).toBe(1)
  })
  it('initialNav: 1 -- он; 2–5 -- выбор без списка; 6+ -- список', () => {
    expect(initialNav({ routerIDs: [4], routers: [R(4, 'x')] })).toMatchObject({ routerID: 4, overlay: null })
    expect(initialNav({ routerIDs: [1, 2, 3], routers, lastID: null })).toMatchObject({ routerID: 2, tab: 'router', overlay: null })
    expect(initialNav({ routerIDs: [1, 2, 3, 4, 5, 6] })).toMatchObject({ routerID: null, overlay: 'fleet' })
  })
  it('чужая ссылка -- список, а не подмена другим роутером', () => {
    expect(initialNav({ routerIDs: [1, 2, 3], routers, deepLinkID: 99 })).toMatchObject({ routerID: null, overlay: 'fleet' })
  })
  it('админ: один роутер -- он; несколько -- Парк без роутера', () => {
    expect(initialNav({ routerIDs: [4], isAdmin: true })).toMatchObject({ routerID: 4, tab: 'router' })
    expect(initialNav({ routerIDs: [1, 2, 3], routers, isAdmin: true })).toEqual({ routerID: null, tab: 'park', overlay: null, sheet: null })
    expect(initialNav({ routerIDs: [1, 2, 3], isAdmin: true, deepLinkID: 3 })).toMatchObject({ routerID: 3, tab: 'router' })
  })
})

describe('последний открытый роутер', () => {
  beforeEach(() => {
    try {
      globalThis.localStorage?.removeItem(LAST_ROUTER_KEY)
    } catch {
      // нет хранилища -- тесты ниже это и проверяют
    }
  })
  it('без хранилища -- null и без исключений', () => {
    const saved = globalThis.localStorage
    globalThis.localStorage = { getItem() { throw new Error('blocked') }, setItem() { throw new Error('blocked') } }
    expect(loadLastRouter()).toBe(null)
    expect(() => saveLastRouter(5)).not.toThrow()
    globalThis.localStorage = saved
  })
  it('пишет и читает число', () => {
    const store = new Map()
    const saved = globalThis.localStorage
    globalThis.localStorage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, String(v)) }
    saveLastRouter(12)
    expect(loadLastRouter()).toBe(12)
    store.set(LAST_ROUTER_KEY, 'мусор')
    expect(loadLastRouter()).toBe(null)
    globalThis.localStorage = saved
  })
})

describe('N2: молчащий роутер -- не тревога', () => {
  const SILENT = { status: 'alert', reach: 'offline', last_seen_age_sec: 7200 }
  it('чип молчащего: tone danger, но не тревога, не первый', () => {
    const chips = stripChips([R(1, 'a'), R(2, 'b', SILENT)], 1)
    expect(chips.map((c) => c.id)).toEqual([1, 2])
    expect(chips[1]).toMatchObject({ tone: 'danger', state: 'молчит', alert: false })
  })
  it('чип тревоги несёт признак alert; запасной -- нет', () => {
    const chips = stripChips([R(1, 'a', ALERT), R(2, 'b', { ...ALERT, reserve_only_alert: true })], 1)
    expect(chips[0].alert).toBe(true)
    expect(chips[1].alert).toBe(false)
  })
  it('главный экран и «на другом роутере» молчащий не выбирают', () => {
    const routers = [R(1, 'a'), R(2, 'b', SILENT)]
    expect(landingRouterID({ routerIDs: [1, 2], routers, lastID: null })).toBe(1)
    expect(otherAlertRouter(routers, 1)).toBe(null)
  })
})
