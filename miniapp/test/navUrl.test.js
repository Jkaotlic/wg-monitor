import { describe, it, expect } from 'vitest'
import { navFromURL, urlFromNav } from '../src/navUrl.js'
import { TABS, OPEN_OVERLAYS } from '../src/nav.js'

const IDS = [3, 7, 12]
const pick = (s) => ({ routerID: s.routerID, tab: s.tab, overlay: s.overlay, sheet: s.sheet ?? null })

describe('urlFromNav', () => {
  it('роутер не выбран -- пустой адрес', () => {
    expect(urlFromNav({ routerID: null, tab: 'router', overlay: 'fleet', sheet: null })).toBe('')
  })

  it('вкладка «Сейчас» не пишется, остальные пишутся', () => {
    expect(urlFromNav({ routerID: 7, tab: 'router', overlay: null, sheet: null })).toBe('?router=7')
    expect(urlFromNav({ routerID: 7, tab: 'tunnels', overlay: null, sheet: null })).toBe('?router=7&tab=tunnels')
  })

  it('оверлей роутера пишется, лист и список роутеров -- нет', () => {
    expect(urlFromNav({ routerID: 7, tab: 'diag', overlay: 'admin', sheet: { title: 'Точно?' } })).toBe('?router=7&tab=diag&open=admin')
    expect(urlFromNav({ routerID: 7, tab: 'router', overlay: 'fleet', sheet: null })).toBe('?router=7')
  })
})

describe('navFromURL', () => {
  it('пустой адрес при нескольких роутерах -- список', () => {
    expect(pick(navFromURL('', IDS))).toEqual({ routerID: null, tab: 'router', overlay: 'fleet', sheet: null })
  })

  it('чужой роутер не открывается, вкладка и open при этом игнорируются', () => {
    expect(pick(navFromURL('?router=99&tab=diag&open=admin', IDS))).toEqual({ routerID: null, tab: 'router', overlay: 'fleet', sheet: null })
  })

  it('неизвестные tab и open игнорируются', () => {
    expect(pick(navFromURL('?router=7&tab=hack&open=evil', IDS))).toEqual({ routerID: 7, tab: 'router', overlay: null, sheet: null })
  })

  it('старый псевдоним tab=routes ведёт на VPN-туннели', () => {
    expect(navFromURL('?router=7&tab=routes', IDS).tab).toBe('tunnels')
  })

  it('единственный роутер открывается и без router=', () => {
    expect(pick(navFromURL('?tab=events', [5]))).toEqual({ routerID: 5, tab: 'events', overlay: null, sheet: null })
  })

  it('мусор в router= -- как без него', () => {
    expect(navFromURL('?router=abc', IDS).routerID).toBe(null)
  })

  it('лист из адреса не открывается никогда', () => {
    expect(navFromURL('?router=7&sheet=reboot', IDS).sheet).toBe(null)
  })
})

describe('круговое свойство', () => {
  it('адрес -> навигация -> адрес сохраняет роутер, вкладку и оверлей', () => {
    const states = [{ routerID: null, tab: 'router', overlay: 'fleet', sheet: null }]
    for (const routerID of IDS) {
      for (const tab of TABS) {
        for (const overlay of [null, ...OPEN_OVERLAYS]) states.push({ routerID, tab, overlay, sheet: null })
      }
    }
    for (const s of states) {
      expect(pick(navFromURL(urlFromNav(s), IDS)), JSON.stringify(s)).toEqual(s)
      expect(urlFromNav(navFromURL(urlFromNav(s), IDS))).toBe(urlFromNav(s))
    }
  })

  it('открытый лист теряется при круге -- обновление страницы не повторяет подтверждение', () => {
    const s = { routerID: 7, tab: 'router', overlay: 'settings', sheet: { title: 'Перезагрузить?' } }
    expect(navFromURL(urlFromNav(s), IDS).sheet).toBe(null)
  })
})
