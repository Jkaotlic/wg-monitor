// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// A1.1 (v0.55): «проверка главнее» и там, где человек выбирает, куда пустить
// трафик: выбор цели переноса, выбор нового главного и «Добавить сайт».
const mocks = vi.hoisted(() => ({ snap: null, checks: null }))

vi.mock('../src/useCommand.js', async () => {
  const { useState } = await import('preact/hooks')
  return {
    useCommand: () => {
      const [state, setState] = useState({ busy: false, result: null, error: null, errorCode: null, sleepNote: '' })
      return {
        ...state,
        run: (action) => {
          const res = action === 'route_status' ? { status: 'ok', output: JSON.stringify(mocks.snap) } : null
          setState({ busy: false, result: res, error: null, errorCode: null, sleepNote: '' })
          return Promise.resolve(res)
        },
      }
    },
  }
})
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve({ role: 'owner' }),
  fetchRouterChecks: () => Promise.resolve(mocks.checks),
}))

const { RoutesTab } = await import('../src/screens/RoutesTab.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

const SNAP = {
  policy_model: true,
  default_egress: 'direct',
  tunnels: [
    { id: 'nwg1', name: 'amsterdam', type: 'managed', status: 'running', enabled: true },
    { id: 'nwg3', name: 'old-home', type: 'managed', status: 'running', enabled: true, default_route: true },
    { id: 'nwg4', name: 'vpn-de', type: 'managed', status: 'down', enabled: true },
    { id: 'nwg5', name: 'off-one', type: 'managed', status: 'disabled', enabled: false },
  ],
  counts: { nwg1: { dns: 1, static: 0, hr_neo: 0 } },
  policies: [
    {
      name: 'VPN',
      active_tunnel_id: 'nwg1',
      via_vpn: true,
      interfaces: [
        { bind: 'nwg1', name: 'amsterdam', role: 'active', tunnel_id: 'nwg1', via_vpn: true },
        { bind: 'nwg3', name: 'old-home', role: 'fallback', tunnel_id: 'nwg3' },
      ],
    },
  ],
}

async function mount(layer, layerParams) {
  mocks.snap = SNAP
  mocks.checks = { tunnels: [{ tunnel_id: 'nwg3', status: 'fail' }] }
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<RoutesTab routerID={7} asleep={false} openSheet={() => {}} layer={layer} layerParams={layerParams} openLayer={() => {}} closeLayer={() => {}} />, root))
  for (let i = 0; i < 3; i++) await flush()
  return root
}

describe('выбор цели: проверка главнее', () => {
  it('«Куда перенести» -- «поднят, но не отвечает», не «работает»', async () => {
    const root = await mount('routepick', { pick: 'rebind', from: 'nwg1' })
    const row = [...root.querySelectorAll('.list-row-btn')].find((b) => b.textContent.includes('old-home'))
    expect(row.textContent).toContain('поднят, но не отвечает')
    expect(row.textContent).not.toContain('работает')
    render(null, root)
  })

  it('«Кто станет главным» -- то же', async () => {
    const root = await mount('routepick', { pick: 'promote', from: 'nwg1' })
    const row = [...root.querySelectorAll('.list-row-btn')].find((b) => b.textContent.includes('old-home'))
    expect(row.textContent).toContain('поднят, но не отвечает')
    render(null, root)
  })

  it('«Добавить сайт» -- «Куда вести трафик»', async () => {
    const root = await mount('routeadd', {})
    const row = [...root.querySelectorAll('.list-row-btn')].find((b) => b.textContent.includes('old-home'))
    expect(row.textContent).toContain('поднят, но не отвечает')
    expect(row.textContent).not.toContain('работает')
    render(null, root)
  })
})

describe('A1.1, раунд 2: плашка основного и «включён, но не поднялся»', () => {
  it('плашка основного у упавшей по проверке -- «не отвечает», а не «выключен»', async () => {
    const root = await mount('routes', {})
    expect(root.textContent).toContain('назначен основным, но не отвечает')
    expect(root.textContent).not.toContain('назначен основным, но выключен')
    render(null, root)
  })

  it('лист переноса: включённый, но не поднявшийся -- «не отвечает», выключенный настройкой -- «выключен»', async () => {
    const root = await mount('routepick', { pick: 'rebind', from: 'nwg1' })
    const row = (name) => [...root.querySelectorAll('.list-row-btn')].find((b) => b.textContent.includes(name))
    expect(row('vpn-de').textContent).toContain('не отвечает')
    expect(row('vpn-de').textContent).not.toContain('выключен')
    expect(row('off-one').textContent).toContain('выключен')
    render(null, root)
  })
})


describe('заголовок «Маршрутов» говорит то же, что плашка', () => {
  it('главный поднят, но проверка упала -- «не отвечает», не «Обход идёт»', async () => {
    const root = await mount('routes', {})
    expect(root.textContent).toContain('назначен основным, но не отвечает')
    expect(root.textContent).toContain('Главный VPN-туннель «old-home» не отвечает')
    expect(root.textContent).not.toContain('Обход идёт через «old-home»')
    render(null, root)
  })

  it('главный включён, но не поднялся -- «не отвечает», не «выключен»', async () => {
    const snap = { ...SNAP, tunnels: SNAP.tunnels.map((t) => (t.id === 'nwg3' ? { ...t, status: 'down' } : t)) }
    const root = document.createElement('div')
    document.body.appendChild(root)
    mocks.snap = snap
    mocks.checks = { tunnels: [] }
    await act(async () => render(<RoutesTab routerID={7} asleep={false} openSheet={() => {}} layer="routes" layerParams={{}} openLayer={() => {}} closeLayer={() => {}} />, root))
    for (let i = 0; i < 3; i++) await flush()
    expect(root.textContent).toContain('назначен основным, но не отвечает')
    expect(root.textContent).toContain('Главный VPN-туннель «old-home» не отвечает')
    expect(root.textContent).not.toContain('но он выключен')
    render(null, root)
  })
})
