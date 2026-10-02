// @vitest-environment jsdom
import { describe, it, expect, beforeEach } from 'vitest'
import { render } from 'preact'
import { useReducer } from 'preact/hooks'
import { act } from 'preact/test-utils'
import { navReducer, localLayerDepth } from '../src/nav.js'
import { navFromURL, urlFromNav } from '../src/navUrl.js'
import { useNavURL } from '../src/useNavURL.js'

// «Назад» браузера в веб-управлении закрывает слои вкладок (v0.52, доводка).
// Слои без адреса (tunnel, replace, confimport, repair, routeadd, routepick,
// cabinetissue) адрес не меняют -- прежде «назад» уводил с места целиком.
// Теперь открытие слоя кладёт в историю запись-метку с тем же адресом.
const IDS = [3, 7]
let api = null

function Probe({ enabled = true }) {
  const [nav, dispatch] = useReducer(navReducer, navFromURL(window.location.search, IDS))
  useNavURL({ enabled, nav, dispatch, routerIDs: IDS })
  api = { nav, dispatch }
  return null
}

let root = null
async function mount(enabled = true) {
  root = document.createElement('div')
  await act(async () => render(<Probe enabled={enabled} />, root))
}
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 30)) })
const go = (fn) => act(async () => { fn(); await new Promise((r) => setTimeout(r, 30)) })
const browserBack = () => go(() => window.history.back())
const browserForward = () => go(() => window.history.forward())
const d = (action) => act(async () => api.dispatch(action))
const address = () => window.location.pathname + window.location.search

const START = '/dashboard/?router=7&tab=tunnels'
beforeEach(async () => {
  if (root) render(null, root)
  // Своя запись на каждый тест: «назад» с неё ведёт на чистый адрес того же места.
  window.history.pushState(null, '', START)
  window.history.pushState(null, '', START)
})

describe('nav: глубина слоёв без адреса', () => {
  const base = { routerID: 7, tab: 'tunnels', overlay: null, sheet: null }
  it('считает подряд идущие слои без адреса от верхнего', () => {
    expect(localLayerDepth(base)).toBe(0)
    expect(localLayerDepth({ ...base, overlay: 'routes' })).toBe(0)
    expect(localLayerDepth({ ...base, overlay: 'tunnel', overlayParams: { tunnelID: 'awg10' } })).toBe(1)
    expect(localLayerDepth({ ...base, overlay: 'replace', overlayParams: { returnTo: 'tunnel', returnParams: { tunnelID: 'awg10' } } })).toBe(2)
    expect(localLayerDepth({ ...base, overlay: 'routeadd', overlayParams: { returnTo: 'routes', returnParams: null } })).toBe(1)
    expect(localLayerDepth({ ...base, overlay: 'routepick', overlayParams: { returnTo: 'routes', returnParams: { returnTo: 'tunnel', returnParams: { tunnelID: 'awg10' } } } })).toBe(1)
    expect(localLayerDepth({ ...base, tab: 'router', overlay: 'repair' })).toBe(1)
    expect(localLayerDepth(null)).toBe(0)
  })
})

describe('«назад» браузера закрывает слой вкладки', () => {
  it('открыли слой -- запись-метка с тем же адресом; «назад» закрывает слой, место остаётся', async () => {
    await mount()
    const before = window.history.length
    await d({ type: 'overlay', overlay: 'tunnel', params: { tunnelID: 'awg10' } })
    expect(api.nav.overlay).toBe('tunnel')
    expect(address()).toBe(START)
    expect(window.history.length).toBe(before + 1)
    await browserBack()
    expect(api.nav.overlay).toBe(null)
    expect(api.nav.routerID).toBe(7)
    expect(api.nav.tab).toBe('tunnels')
    expect(address()).toBe(START)
  })

  it('в истории -- только метка: ни параметров, ни содержимого конфига', async () => {
    await mount()
    await d({ type: 'overlay', overlay: 'replace', params: { tunnel: { id: 'awg10', config: 'PrivateKey = SECRET' }, policyName: 'p' } })
    expect(JSON.stringify(window.history.state)).toBe('{"wgmLayer":1}')
    expect(JSON.stringify(window.history.state)).not.toContain('SECRET')
  })

  it('слой в слое (VPN-туннель → «Заменить конфиг») -- два «назад»', async () => {
    await mount()
    await d({ type: 'overlay', overlay: 'tunnel', params: { tunnelID: 'awg10' } })
    await d({ type: 'overlay', overlay: 'replace', params: { returnTo: 'tunnel', returnParams: { tunnelID: 'awg10' } } })
    expect(window.history.state).toEqual({ wgmLayer: 2 })
    await browserBack()
    expect(api.nav.overlay).toBe('tunnel')
    expect(api.nav.overlayParams).toEqual({ tunnelID: 'awg10' })
    expect(address()).toBe(START)
    await browserBack()
    expect(api.nav.overlay).toBe(null)
    expect(api.nav.tab).toBe('tunnels')
    expect(address()).toBe(START)
  })

  it('слой в слое с адресом («Маршруты» → «Добавить сайт») -- два «назад»', async () => {
    await mount()
    await d({ type: 'overlay', overlay: 'routes' })
    expect(window.location.search).toBe('?router=7&tab=tunnels&open=routes')
    await d({ type: 'overlay', overlay: 'routeadd', params: { kind: 'domain' } })
    expect(window.location.search).toBe('?router=7&tab=tunnels&open=routes')
    expect(window.history.state).toEqual({ wgmLayer: 1 })
    await browserBack()
    expect(api.nav.overlay).toBe('routes')
    expect(window.location.search).toBe('?router=7&tab=tunnels&open=routes')
    await browserBack()
    expect(api.nav.overlay).toBe(null)
    expect(address()).toBe(START)
  })

  it('«Починить» на «Роутере»: «назад» закрывает ход починки, вкладка остаётся', async () => {
    await mount()
    await d({ type: 'tab', tab: 'router' })
    await d({ type: 'overlay', overlay: 'repair' })
    expect(address()).toBe('/dashboard/?router=7')
    await browserBack()
    expect(api.nav.overlay).toBe(null)
    expect(api.nav.tab).toBe('router')
    expect(address()).toBe('/dashboard/?router=7')
  })

  it('закреплённый слой (выпуск конфига) «назад» не отпускает: место и запись-метка возвращены', async () => {
    await mount()
    await d({ type: 'overlay', overlay: 'cabinet' })
    await d({ type: 'overlay', overlay: 'cabinetissue', params: { optionId: 'nl' } })
    await d({ type: 'pin', pinned: true })
    const pinned = api.nav
    const len = window.history.length
    await browserBack()
    expect(api.nav).toBe(pinned)
    expect(window.history.state).toEqual({ wgmLayer: 1 })
    expect(window.history.length).toBe(len)
    expect(window.location.search).toBe('?router=7&tab=tunnels&open=cabinet')
    // Открепили -- тот же «назад» закрывает слой в родителя.
    await d({ type: 'pin', pinned: false })
    await browserBack()
    expect(api.nav.overlay).toBe('cabinet')
  })

  it('закреплённый слой, «назад» через несколько записей: после открепления и закрытия адрес совпадает с местом', async () => {
    await mount()
    await d({ type: 'overlay', overlay: 'cabinet' })
    await d({ type: 'overlay', overlay: 'cabinetissue', params: { optionId: 'nl' } })
    await d({ type: 'pin', pinned: true })
    const pinned = api.nav
    // Chrome пропускает записи, созданные без жеста, и есть меню истории: назад сразу на две.
    await go(() => window.history.go(-2))
    expect(api.nav).toBe(pinned)
    await d({ type: 'pin', pinned: false })
    await d({ type: 'back' })
    await settle()
    expect(api.nav.overlay).toBe('cabinet')
    expect(address()).toBe('/dashboard/' + urlFromNav(api.nav))
    expect(window.location.search).toBe('?router=7&tab=tunnels&open=cabinet')
  })

  it('лист поверх слоя: «назад» закрывает лист, слой и его запись остаются', async () => {
    await mount()
    await d({ type: 'overlay', overlay: 'tunnel', params: { tunnelID: 'awg10' } })
    await d({ type: 'sheet', sheet: { title: 'Удалить?' } })
    await browserBack()
    expect(api.nav.sheet).toBe(null)
    expect(api.nav.overlay).toBe('tunnel')
    expect(window.history.state).toEqual({ wgmLayer: 1 })
    await browserBack()
    expect(api.nav.overlay).toBe(null)
  })

  it('слой закрыли кнопкой приложения -- запись-метка снята; «вперёд» слой не воскрешает', async () => {
    await mount()
    await d({ type: 'overlay', overlay: 'tunnel', params: { tunnelID: 'awg10' } })
    await d({ type: 'back' })
    await settle()
    expect(api.nav.overlay).toBe(null)
    expect(window.history.state).toBe(null)
    await browserForward()
    await settle()
    expect(api.nav.overlay).toBe(null)
    expect(api.nav.tab).toBe('tunnels')
    expect(window.history.state).toBe(null)
    expect(address()).toBe(START)
  })

  it('«назад» закрыл слой, «вперёд» его не воскрешает', async () => {
    await mount()
    await d({ type: 'overlay', overlay: 'confimport' })
    await browserBack()
    expect(api.nav.overlay).toBe(null)
    await browserForward()
    await settle()
    expect(api.nav.overlay).toBe(null)
    expect(window.history.state).toBe(null)
    expect(address()).toBe(START)
  })

  it('обновление страницы на записи-метке: адрес -- место-родитель, слоя нет, метка снята', async () => {
    window.history.pushState({ wgmLayer: 1 }, '', START)
    await mount()
    await settle()
    expect(api.nav.overlay).toBe(null)
    expect(address()).toBe(START)
    expect(window.history.state).toBe(null)
  })

  it('Telegram (выключено): слой историю не трогает', async () => {
    await mount(false)
    const before = window.history.length
    await d({ type: 'overlay', overlay: 'tunnel', params: { tunnelID: 'awg10' } })
    expect(window.history.length).toBe(before)
    expect(window.history.state).toBe(null)
  })
})
