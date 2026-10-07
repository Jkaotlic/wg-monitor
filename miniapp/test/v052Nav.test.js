import { describe, it, expect } from 'vitest'
import { TABS, PARK_TAB, barTabs, tabLabel, barLabel, normalizeTab, navReducer, MANAGE_FOCUS } from '../src/nav.js'
import { navFromURL, urlFromNav } from '../src/navUrl.js'

const IDS = [7, 9]
const at = (search, opts) => navFromURL(search, IDS, opts)

describe('v0.52: вкладки 4+1', () => {
  it('ключи вкладок роутера и подписи', () => {
    expect(TABS).toEqual(['router', 'tunnels', 'diag', 'manage'])
    expect(TABS.map(tabLabel)).toEqual(['Роутер', 'VPN-туннели', 'Проверки', 'Настройки'])
    expect(tabLabel(PARK_TAB)).toBe('Парк')
    expect(barLabel('tunnels')).toBe('VPN-туннели')
  })
  it('нижняя панель без «Роутеры»: не-админ 4, админ 5, админ без роутера -- Парк', () => {
    expect(barTabs({ isAdmin: false, routerID: 7 })).toEqual(TABS)
    expect(barTabs({ isAdmin: true, routerID: 7 })).toEqual([PARK_TAB, ...TABS])
    expect(barTabs({ isAdmin: true, routerID: null })).toEqual([PARK_TAB])
    for (const isAdmin of [true, false]) {
      for (const routerID of [null, 7]) expect(barTabs({ isAdmin, routerID })).not.toContain('fleet')
    }
  })
  it('псевдонимы старых вкладок', () => {
    expect(normalizeTab('routes')).toBe('tunnels')
    expect(normalizeTab('events')).toBe('diag')
  })
})

describe('v0.52: «Проверки» с видом «Что было»', () => {
  const base = { routerID: 7, tab: 'router', overlay: null, sheet: null }
  it('вкладка events открывает «Проверки» на «Что было»', () => {
    expect(navReducer(base, { type: 'tab', tab: 'events' })).toEqual({ ...base, tab: 'diag', diagView: 'history' })
  })
  it('обычный переход на «Проверки» -- вид «Сейчас», ключа нет', () => {
    const left = navReducer({ ...base, tab: 'diag', diagView: 'history' }, { type: 'tab', tab: 'router' })
    expect('diagView' in left).toBe(false)
    expect('diagView' in navReducer(left, { type: 'tab', tab: 'diag' })).toBe(false)
  })
  it('сегмент переключает вид только на «Проверках»', () => {
    const diag = { ...base, tab: 'diag' }
    expect(navReducer(diag, { type: 'diagView', view: 'history' }).diagView).toBe('history')
    expect('diagView' in navReducer({ ...diag, diagView: 'history' }, { type: 'diagView', view: 'now' })).toBe(false)
    expect(navReducer(base, { type: 'diagView', view: 'history' })).toBe(base)
  })
  it('смена роутера с «Что было» держит вид, на другую вкладку -- нет', () => {
    const s = navReducer({ ...base, tab: 'diag', diagView: 'history' }, { type: 'router', id: 9, keepTab: true })
    expect(s).toMatchObject({ routerID: 9, tab: 'diag', diagView: 'history' })
    expect('diagView' in navReducer({ ...base, tab: 'diag', diagView: 'history' }, { type: 'router', id: 9 })).toBe(false)
  })
})

describe('v0.52: разделы «Настроек»', () => {
  it('старые ссылки и слои раскрывают новые разделы', () => {
    expect(MANAGE_FOCUS).toEqual({ settings: 'agent', admin: 'service', packages: 'service', dnsreset: 'service', porthop: 'service', space: 'service', agentcfg: 'agent', agentconn: 'agent' })
  })
  it('переход в раздел с «Роутера» (плашка обслуживания)', () => {
    const s = navReducer({ routerID: 7, tab: 'router', overlay: null, sheet: null }, { type: 'manage', section: 'service' })
    expect(s).toMatchObject({ tab: 'manage', manageFocus: 'service', overlay: null, sheet: null })
  })
})

// Приёмка спеки: ссылки из уже отправленных тревог открывают осмысленный экран.
const OLD_LINKS = [
  ['?router=7', { tab: 'router', overlay: null }],
  ['?router=7&tab=tunnels', { tab: 'tunnels' }],
  ['?router=7&tab=routes', { tab: 'tunnels' }],
  ['?router=7&tab=diag', { tab: 'diag' }],
  ['?router=7&tab=events', { tab: 'diag', diagView: 'history' }],
  ['?router=7&tab=manage', { tab: 'manage' }],
  ['?router=7&open=settings', { tab: 'manage', manageFocus: 'agent' }],
  ['?router=7&open=admin', { tab: 'manage', manageFocus: 'service' }],
  // «Маршруты» по ссылке без tab: подпись «назад» -- «VPN-туннели», и вернуть
  // должно туда же, а не на «Роутер» (финальное ревью v0.52, п. 8).
  ['?router=7&open=routes', { tab: 'tunnels', overlay: 'routes' }],
  ['?router=7&tab=router&open=routes', { tab: 'tunnels', overlay: 'routes' }],
  ['?router=7&tab=tunnels&open=cabinet', { tab: 'tunnels', overlay: 'cabinet' }],
  ['?router=7&open=agentcfg', { overlay: 'agentcfg' }],
  ['?router=7&open=agentconn', { overlay: 'agentconn' }],
  ['?router=7&open=dnsreset', { overlay: 'dnsreset' }],
  ['?router=7&open=packages', { overlay: 'packages' }],
  ['?router=7&open=porthop', { overlay: 'porthop' }],
  ['?router=7&open=space', { overlay: 'space' }],
]

describe('v0.52: старые ссылки из тревог', () => {
  it.each(OLD_LINKS)('%s', (search, want) => {
    expect(at(search)).toMatchObject({ routerID: 7, ...want })
  })
  it('2–5 роутеров, красный роутер в другом месте: ссылка на конкретный роутер всё равно выигрывает', () => {
    const routers = [
      { id: 7, nickname: 'Дача', status: 'online', reach: 'online', last_seen_age_sec: 30 },
      { id: 9, nickname: 'Офис', status: 'offline', reach: 'offline', last_seen_age_sec: 4000 },
    ]
    expect(at('?router=7&open=routes', { routers })).toMatchObject({ routerID: 7, tab: 'tunnels', overlay: 'routes' })
    expect(at('?router=7', { routers })).toMatchObject({ routerID: 7, tab: 'router', overlay: null })
  })
  it('роутер, к которому нет доступа (не админ): список, а не чужой роутер и не слой', () => {
    const s = at('?router=99&open=routes', { isAdmin: false })
    expect(s).toMatchObject({ routerID: null, overlay: 'fleet' })
  })
  it('админские ссылки Парка', () => {
    expect(at('?tab=park', { isAdmin: true })).toMatchObject({ routerID: null, tab: 'park', overlay: null })
    expect(at('?router=7&tab=park', { isAdmin: true })).toMatchObject({ routerID: 7, tab: 'park' })
    expect(at('?tab=park&open=selfhosted', { isAdmin: true })).toMatchObject({ tab: 'park', overlay: 'selfhosted' })
  })
  it('«Что было» пишется в адрес как tab=events и читается обратно', () => {
    const s = { routerID: 7, tab: 'diag', diagView: 'history', overlay: null, sheet: null }
    expect(urlFromNav(s)).toBe('?router=7&tab=events')
    expect(urlFromNav(at(urlFromNav(s)))).toBe('?router=7&tab=events')
    expect(urlFromNav({ routerID: 7, tab: 'diag', overlay: null, sheet: null })).toBe('?router=7&tab=diag')
  })
})
