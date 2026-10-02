// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ fleet: null }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchFleet: () => Promise.resolve(mocks.fleet),
  fetchAccess: () => Promise.resolve({ owner: null, operators: [] }),
}))
const { ParkTab } = await import('../src/screens/ParkTab.jsx')
const { AppContext } = await import('../src/appContext.js')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const R = (over) => ({ id: 0, nickname: '', status: 'online', last_seen_age_sec: 30, agent_version: 'v0.49.0', pending_version: '', agent_behind: false, agent_update_warning: '', notify_muted: false, ...over })
const LIST = [R({ id: 1, nickname: 'ok-router' }), R({ id: 2, nickname: 'alarm', status: 'alert', active_incidents: [{ check_name: 'dns' }] })]

async function mount(openLayer = () => {}) {
  mocks.fleet = { generated_at: '2026-10-01T10:00:00Z', totals: { routers: 2 }, backend: { version: 'v0.52.0', latest_version: '', update_available: false }, routers: LIST, notify: { unreachable: [], routers_without_recipients: [] } }
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () =>
    render(
      <AppContext.Provider value={{ mode: 'miniapp', wide: false }}>
        <ParkTab routers={LIST} onPick={() => {}} openSheet={() => {}} openLayer={openLayer} onOpenConnection={() => {}} />
      </AppContext.Provider>,
      root,
    ),
  )
  await flush()
  await flush()
  return root
}
const cleanup = (root) => {
  render(null, root)
  root.remove()
}

describe('v0.52: Парк -- раздел «Серверы»', () => {
  it('свои VPS и панели -- двумя входами раздела «Серверы»', async () => {
    const layers = []
    const root = await mount((o, p) => layers.push([o, p]))
    const servers = [...root.querySelectorAll('.section')].find((s) => s.querySelector('.section-title')?.textContent === 'Серверы')
    const rows = [...servers.querySelectorAll('.list-row')]
    expect(rows.some((r) => r.textContent.includes('Свои VPS'))).toBe(true)
    expect(rows.some((r) => r.textContent.includes('Открыть в браузере'))).toBe(true)
    await act(async () => rows.find((r) => r.textContent.includes('Панели VPN-серверов')).click())
    expect(layers).toContainEqual(['selfhosted', { part: 'awg3' }])
    expect(root.textContent).not.toContain('Свои VPN-серверы')
    cleanup(root)
  })
  it('массовый опрос -- «Проверить заново все»', async () => {
    const root = await mount()
    expect([...root.querySelectorAll('button')].some((b) => b.textContent.trim() === 'Проверить заново все')).toBe(true)
    expect(root.textContent).not.toContain('Опросить все')
    cleanup(root)
  })
})
