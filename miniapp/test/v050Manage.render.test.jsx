// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ settings: null, versions: null, settingsFn: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => (mocks.settingsFn ? mocks.settingsFn() : Promise.resolve(mocks.settings)),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [{ tunnel_id: 'awg10', name: 'vpn-nl', run_state: 'running', status: 'ok', ping_check_status: 'pass' }] }),
  fetchRouterVersions: () => Promise.resolve(mocks.versions),
  fetchAccess: () => Promise.resolve({ owner: null, operators: [] }),
  fetchAgentConnection: () => Promise.resolve({ awgm_url: 'https://awg.example.com' }),
  fetchRouter: () => Promise.resolve({ router: { id: 2, nickname: 'home', status: 'online', last_seen_age_sec: 30 }, incidents: [] }),
  fetchRouterFacts: () => Promise.resolve(null),
}))

const { ManageTab } = await import('../src/screens/ManageTab.jsx')
const { DiagTab } = await import('../src/screens/DiagTab.jsx')
const { AppContext } = await import('../src/appContext.js')

const noop = () => {}
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)

async function mount(node) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<AppContext.Provider value={{ mode: 'miniapp', wide: false }}>{node}</AppContext.Provider>, root))
  await flush()
  await flush()
  return root
}
const manage = (isAdmin, focusGroup = null) =>
  mount(
    <ManageTab
      routerID={2}
      routerName="home"
      isAdmin={isAdmin}
      focusGroup={focusGroup}
      openSheet={noop}
      openLayer={noop}
      onOpenAgentConfig={noop}
      onOpenAgentConnection={noop}
      onOpenDNSReset={noop}
      onOpenPackages={noop}
    />,
  )
const cleanup = (root) => {
  render(null, root)
  root.remove()
}
const opened = (root) => Object.fromEntries([...root.querySelectorAll('details.manage-group')].map((d) => [d.id, d.open]))
const chips = (root) => [...root.querySelectorAll('.manage-anchors .manage-anchor')].map((b) => b.textContent)

beforeEach(() => {
  mocks.settings = { role: 'admin', agent_version: 'v0.47.0', panel_known: true, panel_scope: 'public', panel_url: 'https://awg.example.com' }
  mocks.versions = null
  mocks.settingsFn = null
})

describe('«Проверка связи» и «Осмотр» -- на вкладке «Проверки»', () => {
  it('разделы на месте, журнал -- владельцу с агентом v0.47+', async () => {
    mocks.settings = { role: 'owner', agent_version: 'v0.47.0' }
    const root = await mount(<DiagTab routerID={2} asleep={false} openSheet={noop} />)
    expect(root.textContent).toContain('Проверка связи VPN-туннелей')
    expect(button(root, 'Проверить связь сейчас')).toBeTruthy()
    expect(button(root, 'Осмотреть роутер')).toBeTruthy()
    expect(root.textContent).toContain('Журнал awg-manager')
    expect(root.querySelectorAll('.btn-primary')).toHaveLength(1)
    cleanup(root)
  })
})
