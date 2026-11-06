// @vitest-environment jsdom
// MINI-06, проводка: вкладка «VPN-туннели» при сбое загрузки проверок не
// рисует поднятый интерфейс «работает».
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const SNAP = {
  tunnels: [{ id: 'awg10', name: 'vymysel-nl', type: 'managed', status: 'running', enabled: true, has_handshake: true, handshake_age_sec: 30 }],
  policies: [],
}
const mocks = vi.hoisted(() => ({ checks: null, runs: [] }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterChecks: () => mocks.checks(),
  // Вкладки читают проверки вместе с тревогами (I1): тот же ответ, тревог нет.
  fetchRouterChecksWithIncidents: (id) => Promise.resolve((() => mocks.checks())(id)).then((ev) => ({ incidents: [], ...ev })),
  fetchRouterSettings: () => Promise.resolve({ role: 'owner' }),
}))
// Один и тот же объект на каждый рендер: вкладка перечитывает снимок по
// смене result, и новый объект на рендер зациклил бы её.
const CMD = vi.hoisted(() => ({ value: null }))
vi.mock('../src/useCommand.js', () => ({ useCommand: () => CMD.value }))
CMD.value = {
  busy: false,
  result: { status: 'ok', output: JSON.stringify(SNAP) },
  error: null,
  errorCode: null,
  run: (action) => {
    mocks.runs.push(action)
    return Promise.resolve(null)
  },
}

const { TunnelsTab, requestTunnelStatus } = await import('../src/screens/TunnelsTab.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function mount() {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<TunnelsTab routerID={4} asleep={false} openSheet={() => {}} />, root))
  await flush()
  return root
}

describe('MINI-06: вкладка туннелей', () => {
  it('на старом агенте один раз откатывается к полному route_status', async () => {
    const run = vi.fn()
      .mockResolvedValueOnce({ status: 'err', output: 'unknown action: tunnels_status' })
      .mockResolvedValueOnce({ status: 'ok', output: JSON.stringify(SNAP) })
    const deadline = { deadlineMs: 90_000 }
    await requestTunnelStatus(run, deadline)
    expect(run.mock.calls).toEqual([
      ['tunnels_status', {}, deadline],
      ['route_status', {}, deadline],
    ])
  })

  it('запрашивает компактный tunnels_status, а не полный список правил', async () => {
    mocks.runs.length = 0
    mocks.checks = () => Promise.resolve({ checks: [], tunnels: [] })
    const root = await mount()
    expect(mocks.runs).toContain('tunnels_status')
    expect(mocks.runs).not.toContain('route_status')
    render(null, root)
    root.remove()
  })

  it('проверки не загрузились -- не «работает»', async () => {
    mocks.checks = () => Promise.reject(new Error('сервер не ответил'))
    const root = await mount()
    expect(root.textContent).toContain('vymysel-nl')
    // Строка туннеля: неизвестна проверка (сервер не ответил), а не «работает».
    expect(root.textContent).toMatch(/vymysel-nlподнят, проверка не пришла: сервер не ответил/)
    expect(root.textContent).not.toContain('роутер не сказал')
    render(null, root)
    root.remove()
  })
  it('проверки ок -- «работает»', async () => {
    mocks.checks = () => Promise.resolve({ checks: [], tunnels: [{ tunnel_id: 'awg10', status: 'ok', run_state: 'running', ts: new Date().toISOString() }] })
    const root = await mount()
    expect(root.textContent).toMatch(/vymysel-nlработает/)
    render(null, root)
    root.remove()
  })
})
