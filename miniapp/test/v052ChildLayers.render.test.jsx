// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const SNAP = {
  tunnels: [
    { id: 'awg10', name: 'vpn-nl', type: 'managed', status: 'running', enabled: true, has_handshake: true, handshake_age_sec: 30 },
    { id: 'awg14', name: 'vpn-de', type: 'managed', status: 'running', enabled: true, has_handshake: true, handshake_age_sec: 30 },
  ],
  policies: [{ name: 'Policy0', active_tunnel_id: 'awg10', interfaces: [] }],
}
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve({ role: 'owner' }),
  fetchRouterFacts: () => Promise.resolve(null),
  fetchCabinets: () => Promise.resolve({ amnezia: { keys: [] }, hidemy: { codes: [] }, selfhosted: { available: false } }),
  fetchVPNAccounts: () => Promise.resolve({ accounts: [] }),
  fetchAwg3Issuable: () => Promise.resolve({ panels: [] }),
}))
const CMD = vi.hoisted(() => ({ value: null }))
vi.mock('../src/useCommand.js', () => ({ useCommand: () => CMD.value }))
CMD.value = { busy: false, result: { status: 'ok', output: JSON.stringify(SNAP) }, error: null, errorCode: null, run: () => Promise.resolve(null) }

const { RoutesTab } = await import('../src/screens/RoutesTab.jsx')
const { CabinetScreen } = await import('../src/screens/CabinetScreen.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
async function mount(node) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(node, root))
  await flush()
  await flush()
  return root
}
const cleanup = (root) => {
  render(null, root)
  root.remove()
}
const btn = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)

describe('v0.52: «Маршруты» -- выбор цели и добавление слоями', () => {
  it('«Добавить сайт или адрес» открывает слой routeadd', async () => {
    const calls = []
    const root = await mount(<RoutesTab routerID={4} asleep={false} openSheet={() => {}} layer="routes" layerParams={{}} openLayer={(o, p) => calls.push([o, p])} closeLayer={() => {}} />)
    await act(async () => btn(root, 'Добавить сайт или адрес').click())
    expect(calls).toEqual([['routeadd', undefined]])
    cleanup(root)
  })
  it('layer=routepick строит выбор цели из параметров и снимка', async () => {
    const root = await mount(<RoutesTab routerID={4} asleep={false} openSheet={() => {}} layer="routepick" layerParams={{ pick: 'rebind', from: 'awg10' }} openLayer={() => {}} closeLayer={() => {}} />)
    expect(root.querySelector('.overlay-title').textContent).toBe('Куда перенести')
    expect(root.querySelector('.overlay').textContent).toContain('vpn-de')
    cleanup(root)
  })
  it('layer=routeadd рисует мастер добавления', async () => {
    const root = await mount(<RoutesTab routerID={4} asleep={false} openSheet={() => {}} layer="routeadd" layerParams={{}} openLayer={() => {}} closeLayer={() => {}} />)
    expect(root.querySelector('.overlay-title').textContent).toBe('Добавить сайт или адрес')
    cleanup(root)
  })
})

describe('v0.52: выпуск в кабинете -- слой cabinetissue', () => {
  const pending = { provider: 'amnezia', title: 'Amnezia', option: { id: 'nl', label: 'Нидерланды', note: '' } }
  it('layer=cabinetissue рисует «Что произойдёт» с выбранным вариантом', async () => {
    const root = await mount(<CabinetScreen routerID={4} routerName="home" openSheet={() => {}} onClose={() => {}} layer="cabinetissue" layerParams={{ pending }} openLayer={() => {}} closeLayer={() => {}} onPin={() => {}} />)
    expect(root.textContent).toContain('Что произойдёт')
    expect(root.textContent).toContain('Нидерланды')
    cleanup(root)
  })
  it('заголовок слоя -- «Откуда взять конфиг», слова «кабинет» в заголовке нет', async () => {
    const root = await mount(<CabinetScreen routerID={4} routerName="home" openSheet={() => {}} onClose={() => {}} layer="cabinet" layerParams={{}} openLayer={() => {}} closeLayer={() => {}} onPin={() => {}} />)
    expect(root.querySelector('.overlay-title').textContent).toBe('Откуда взять конфиг «home»')
    cleanup(root)
  })
})

describe('v0.52 fix 3: выход из cabinetissue перечитывает подписку', () => {
  it('закрытие слоя выпуска (в том числе «назад» Telegram) -- fetchVPNAccounts заново', async () => {
    const api = await import('../src/api.js')
    const spy = vi.spyOn(api, 'fetchVPNAccounts')
    const pending = { provider: 'amnezia', title: 'Amnezia', option: { id: 'nl', label: 'Нидерланды', note: '' } }
    const root = document.createElement('div')
    document.body.appendChild(root)
    const draw = (layer, layerParams) => act(async () => render(<CabinetScreen routerID={4} routerName="home" openSheet={() => {}} onClose={() => {}} layer={layer} layerParams={layerParams} openLayer={() => {}} closeLayer={() => {}} onPin={() => {}} />, root))
    await draw('cabinetissue', { pending })
    await flush()
    const before = spy.mock.calls.length
    await draw('cabinet', {})
    await flush()
    expect(spy.mock.calls.length).toBe(before + 1)
    cleanup(root)
    spy.mockRestore()
  })
})
