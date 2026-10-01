// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const SNAP = {
  tunnels: [{ id: 'awg10', name: 'vpn-nl', type: 'managed', status: 'running', enabled: true, has_handshake: true, handshake_age_sec: 30 }],
  policies: [{ name: 'Policy0', active_tunnel_id: 'awg10', interfaces: [] }],
}
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [{ tunnel_id: 'awg10', status: 'ok', run_state: 'running' }] }),
  fetchRouterSettings: () => Promise.resolve({ role: 'owner' }),
  fetchAwg3Issuable: () => Promise.resolve({ panels: [] }),
  fetchRouterFacts: () => Promise.resolve(null),
  fetchVPNAccounts: () => Promise.resolve({ accounts: [] }),
  fetchReplaceStatus: () => Promise.resolve(null),
}))
const CMD = vi.hoisted(() => ({ value: null }))
vi.mock('../src/useCommand.js', () => ({ useCommand: () => CMD.value }))
CMD.value = { busy: false, result: { status: 'ok', output: JSON.stringify(SNAP) }, error: null, errorCode: null, run: () => Promise.resolve(null) }

const { TunnelsTab } = await import('../src/screens/TunnelsTab.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function mount(props) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<TunnelsTab routerID={4} asleep={false} openSheet={() => {}} {...props} />, root))
  await flush()
  return root
}
const cleanup = (root) => {
  render(null, root)
  root.remove()
}

describe('v0.52: слои «VPN-туннелей» -- через навигацию', () => {
  it('строка списка открывает слой tunnel с id, а не локальный экран', async () => {
    const calls = []
    const root = await mount({ layer: null, layerParams: {}, openLayer: (o, p) => calls.push([o, p]), closeLayer: () => {} })
    const row = [...root.querySelectorAll('.list-row')].find((r) => r.textContent.includes('vpn-nl'))
    await act(async () => row.click())
    expect(calls).toEqual([['tunnel', { tunnelID: 'awg10' }]])
    expect(root.querySelector('.overlay')).toBe(null)
    cleanup(root)
  })

  it('layer=tunnel рисует экран VPN-туннеля; «Назад» зовёт closeLayer', async () => {
    let closed = 0
    const root = await mount({ layer: 'tunnel', layerParams: { tunnelID: 'awg10' }, openLayer: () => {}, closeLayer: () => closed++ })
    expect(root.querySelector('.overlay-title').textContent).toBe('VPN-туннель')
    await act(async () => root.querySelector('.overlay-back').click())
    expect(closed).toBe(1)
    cleanup(root)
  })

  it('«Заменить конфиг» живёт в экране работающего VPN-туннеля и открывает слой replace', async () => {
    const calls = []
    const root = await mount({ layer: 'tunnel', layerParams: { tunnelID: 'awg10' }, openLayer: (o, p) => calls.push([o, p]), closeLayer: () => {} })
    const btn = [...root.querySelectorAll('.overlay button')].find((b) => b.textContent.trim() === 'Заменить конфиг')
    await act(async () => btn.click())
    expect(calls[0][0]).toBe('replace')
    expect(calls[0][1]).toMatchObject({ tunnel: { id: 'awg10' }, policyName: 'Policy0', returnTo: 'tunnel', returnParams: { tunnelID: 'awg10' } })
    cleanup(root)
  })

  it('layer=replace и layer=confimport рисуют свои экраны', async () => {
    const r1 = await mount({ layer: 'replace', layerParams: { tunnel: { id: 'awg10', title: 'vpn-nl' }, policyName: 'Policy0' }, openLayer: () => {}, closeLayer: () => {} })
    expect(r1.querySelector('.overlay-title').textContent).toBe('Заменить конфиг')
    cleanup(r1)
    const r2 = await mount({ layer: 'confimport', layerParams: {}, openLayer: () => {}, closeLayer: () => {} })
    expect(r2.querySelector('.overlay-title').textContent).toBe('Загрузить конфиг .conf')
    cleanup(r2)
  })

  it('fix 1: переход из родителя в дочерний слой не перечитывает снимок, закрытие родителя -- один раз', async () => {
    const runs = []
    CMD.value = { ...CMD.value, run: (...a) => { runs.push(a); return Promise.resolve(null) } }
    const root = document.createElement('div')
    document.body.appendChild(root)
    const draw = (props) => act(async () => render(<TunnelsTab routerID={4} asleep={false} openSheet={() => {}} layer={null} layerParams={{}} openLayer={() => {}} closeLayer={() => {}} {...props} />, root))
    await draw({ cabinetOpen: true })
    await flush()
    const before = runs.length
    await draw({ cabinetOpen: true })
    await draw({ cabinetOpen: true })
    expect(runs.length).toBe(before)
    await draw({ cabinetOpen: false })
    expect(runs.length).toBe(before + 1)
    cleanup(root)
  })

  it('строк «Загрузить конфиг .conf» и «Заменить конфиг VPN-туннеля» на вкладке больше нет', async () => {
    const root = await mount({ layer: null, layerParams: {}, openLayer: () => {}, closeLayer: () => {} })
    expect(root.textContent).not.toContain('Заменить конфиг VPN-туннеля')
    expect([...root.querySelectorAll('.list-row')].some((r) => r.textContent.includes('Загрузить конфиг .conf'))).toBe(false)
    cleanup(root)
  })
})
