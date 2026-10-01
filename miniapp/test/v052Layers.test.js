import { describe, it, expect } from 'vitest'
import { navReducer, backButtonVisible, escapeAction, navPinned, fleetIsHome, tabOwnsLayer, TAB_LAYERS, CHILD_LAYERS, ROUTER_LAYERS, LOCAL_LAYERS, OPEN_OVERLAYS } from '../src/nav.js'
import { urlFromNav } from '../src/navUrl.js'

const base = { routerID: 7, tab: 'router', overlay: null, sheet: null }
const open = (s, overlay, params) => navReducer(s, { type: 'overlay', overlay, params })

describe('v0.52: семь локальных слоёв -- оверлеи nav.js', () => {
  it('ровно семь, ни один не открывается по адресу', () => {
    expect(LOCAL_LAYERS.sort()).toEqual(['cabinetissue', 'confimport', 'repair', 'replace', 'routeadd', 'routepick', 'tunnel'])
    for (const l of LOCAL_LAYERS) expect(OPEN_OVERLAYS).not.toContain(l)
  })

  it('слой вкладки открывает свою вкладку и закрывается назад в неё', () => {
    const s = open(base, 'tunnel', { tunnelID: 'awg10' })
    expect(s).toMatchObject({ tab: 'tunnels', overlay: 'tunnel', overlayParams: { tunnelID: 'awg10' } })
    expect(tabOwnsLayer(s)).toBe(true)
    expect(backButtonVisible(s, { wide: false })).toBe(true)
    expect(escapeAction(s, { wide: true })).toEqual({ type: 'back' })
    expect(navReducer(s, { type: 'back' })).toEqual({ ...base, tab: 'tunnels' })
  })

  it('уход с вкладки закрывает её слой', () => {
    const s = open(base, 'replace', { tunnel: { id: 'awg10', title: 'nl' }, policyName: 'Policy0' })
    const moved = navReducer(s, { type: 'tab', tab: 'diag' })
    expect(moved).toEqual({ ...base, tab: 'diag' })
    // Та же вкладка -- слой остаётся.
    expect(navReducer(s, { type: 'tab', tab: 'tunnels' }).overlay).toBe('replace')
  })

  it('слой в слое -- только поверх родителя; назад -- в родителя с его параметрами', () => {
    expect(open(base, 'routeadd')).toBe(base)
    const routes = open(base, 'routes', { rebindFrom: 'awg10', returnTo: 'tunnel', returnParams: { tunnelID: 'awg10' } })
    const pick = open(routes, 'routepick', { pick: 'rebind', from: 'awg10' })
    expect(pick.overlay).toBe('routepick')
    expect(pick.overlayParams).toEqual({ pick: 'rebind', from: 'awg10', returnTo: 'routes', returnParams: routes.overlayParams })
    const back = navReducer(pick, { type: 'back' })
    expect(back).toMatchObject({ overlay: 'routes', overlayParams: routes.overlayParams })
    // Из «Маршрутов», открытых с экрана VPN-туннеля, -- обратно на него.
    expect(navReducer(back, { type: 'back' })).toMatchObject({ tab: 'tunnels', overlay: 'tunnel', overlayParams: { tunnelID: 'awg10' } })
  })

  it('выпуск в кабинете -- слой в слое; выпуск закрепляет слой', () => {
    const cab = open({ ...base, tab: 'tunnels' }, 'cabinet', { tab: 'hidemy' })
    const issue = open(cab, 'cabinetissue', { pending: { provider: 'amnezia', title: 'Amnezia', option: { id: 'nl', label: 'Нидерланды', note: '' } } })
    const pinned = navReducer(issue, { type: 'pin', pinned: true })
    expect(navPinned(pinned)).toBe(true)
    expect(navReducer(pinned, { type: 'back' })).toBe(pinned)
    expect(navReducer(pinned, { type: 'tab', tab: 'diag' })).toBe(pinned)
    const unpinned = navReducer(pinned, { type: 'pin', pinned: false })
    expect(navReducer(unpinned, { type: 'back' })).toMatchObject({ overlay: 'cabinet', overlayParams: { tab: 'hidemy' } })
  })

  it('pin не трогает слои вне списка', () => {
    const s = open(base, 'tunnel', { tunnelID: 'awg10' })
    expect(navReducer(s, { type: 'pin', pinned: true })).toBe(s)
  })

  it('починка -- слой роутера: назад на ту же вкладку, в адрес не пишется', () => {
    expect(ROUTER_LAYERS).toEqual(['repair'])
    const s = open(base, 'repair', { checkName: 'tunnel_awg10', lineName: 'nl' })
    expect(s).toMatchObject({ tab: 'router', overlay: 'repair' })
    expect(urlFromNav(s)).toBe('?router=7')
    expect(navReducer(s, { type: 'back' })).toEqual(base)
  })

  it('без роутера слои роутера не открываются', () => {
    const none = { routerID: null, tab: 'park', overlay: null, sheet: null }
    for (const l of [...Object.keys(TAB_LAYERS), 'repair']) expect(open(none, l)).toBe(none)
    expect(Object.values(CHILD_LAYERS).sort()).toEqual(['cabinet', 'routes', 'routes'])
  })

  it('адрес слоя в слое -- адрес родителя', () => {
    const routes = open({ ...base, tab: 'tunnels' }, 'routes')
    expect(urlFromNav(open(routes, 'routeadd'))).toBe('?router=7&tab=tunnels&open=routes')
  })
})

describe('v0.52: список роутеров, открытый с Парка', () => {
  it('закрывается назад -- это не главный экран', () => {
    const s = { routerID: null, tab: 'park', overlay: 'fleet', sheet: null }
    expect(fleetIsHome(s)).toBe(false)
    expect(backButtonVisible(s, { wide: false })).toBe(true)
    expect(navReducer(s, { type: 'back' })).toEqual({ routerID: null, tab: 'park', overlay: null, sheet: null })
  })
  it('без Парка (не-админ, 6+ роутеров) -- по-прежнему главный экран', () => {
    expect(fleetIsHome({ routerID: null, tab: 'router', overlay: 'fleet', sheet: null })).toBe(true)
  })
})
