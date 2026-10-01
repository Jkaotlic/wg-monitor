// @vitest-environment jsdom
// v0.48: «Парк» -- вкладка админа. Чистая часть: какие вкладки в панели,
// подписи, возврат слоёв парка во вкладку и адрес ?tab=park.
import { describe, it, expect } from 'vitest'
import { TABS, PARK_TAB, barTabs, barLabel, tabLabel, navReducer } from '../src/nav.js'
import { navFromURL, urlFromNav } from '../src/navUrl.js'
import { returnLabel } from '../src/screens/OverlayHost.jsx'

describe('barTabs', () => {
  it('админу с роутером -- Парк первым, потом вкладки роутера', () => {
    expect(barTabs({ isAdmin: true, routerID: 1 })).toEqual([PARK_TAB, ...TABS])
  })
  it('админу без роутера -- только Парк', () => {
    expect(barTabs({ isAdmin: true, routerID: null })).toEqual([PARK_TAB])
  })
  it('не-админу -- прежние пять', () => {
    expect(barTabs({ isAdmin: false, routerID: 1 })).toEqual(TABS)
    expect(barTabs({ isAdmin: false, routerID: null })).toEqual(TABS)
  })
})

describe('подписи', () => {
  it('в панели, в заголовке и шапке -- «VPN-туннели»', () => {
    expect(barLabel('tunnels')).toBe('VPN-туннели')
    expect(tabLabel('tunnels')).toBe('VPN-туннели')
    expect(tabLabel(PARK_TAB)).toBe('Парк')
    expect(barLabel(PARK_TAB)).toBe('Парк')
  })
  it('«назад» слоя парка, открытого из Парка, -- «Парк»', () => {
    expect(returnLabel('park')).toBe('Парк')
  })
})

describe('navReducer и Парк', () => {
  const base = { routerID: null, tab: 'router', overlay: 'fleet', sheet: null }
  it('вкладка Парк открывается и без роутера, список закрывается', () => {
    expect(navReducer(base, { type: 'tab', tab: 'park', closeOverlay: true })).toEqual({ routerID: null, tab: 'park', overlay: null, sheet: null })
  })
  it('слой парка возвращается во вкладку Парк -- и без роутера', () => {
    const s = { routerID: null, tab: 'park', overlay: 'provision', sheet: null, overlayParams: { returnTo: 'park' } }
    expect(navReducer(s, { type: 'back' })).toEqual({ routerID: null, tab: 'park', overlay: null, sheet: null })
    expect(navReducer(s, { type: 'overlay', overlay: 'park' })).toEqual({ routerID: null, tab: 'park', overlay: null, sheet: null })
  })
})

describe('адрес Парка', () => {
  it('?tab=park -- админу, с роутером и без', () => {
    expect(navFromURL('?tab=park', [1, 2], { isAdmin: true })).toEqual({ routerID: null, tab: 'park', overlay: null, sheet: null })
    expect(navFromURL('?router=2&tab=park', [1, 2], { isAdmin: true })).toMatchObject({ routerID: 2, tab: 'park', overlay: null })
  })
  it('не-админу ?tab=park не значит ничего', () => {
    expect(navFromURL('?tab=park', [1, 2], { isAdmin: false })).toMatchObject({ tab: 'router', overlay: 'fleet' })
    expect(navFromURL('?router=2&tab=park', [1, 2], { isAdmin: false })).toMatchObject({ routerID: 2, tab: 'router' })
  })
  it('обратно в адрес', () => {
    expect(urlFromNav({ routerID: null, tab: 'park', overlay: null })).toBe('?tab=park')
    expect(urlFromNav({ routerID: 2, tab: 'park', overlay: null })).toBe('?router=2&tab=park')
  })
  it('свои серверы из Парка -- возврат в Парк', () => {
    expect(navFromURL('?tab=park&open=selfhosted', [1, 2], { isAdmin: true })).toMatchObject({ tab: 'park', overlay: 'selfhosted', overlayParams: { returnTo: 'park' } })
  })
})
