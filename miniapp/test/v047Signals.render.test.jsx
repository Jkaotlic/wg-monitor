// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ facts: null, sent: [], result: null, settings: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterFacts: () => Promise.resolve(mocks.facts),
  sendCommand: (routerID, action, args) => {
    mocks.sent.push({ routerID, action, args })
    return Promise.resolve({ cmd_id: 'c1' })
  },
  fetchCommandResult: () => Promise.resolve(mocks.result),
  fetchRouterSettings: () => Promise.resolve(mocks.settings),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [] }),
  fetchRouterVersions: () => Promise.resolve({ rows: [], unknown: [], installed: {} }),
}))

const { ExitRow, ExitIPSection, WANSection, HooksRow, AwgmLogsSection, NativeDNSSection } = await import('../src/screens/SignalSections.jsx')
const { SettingsSections } = await import('../src/screens/SettingsScreen.jsx')

const FACTS = {
  supported: true,
  agent_version: 'v0.47.0',
  ping_fails_24h: {},
  exit: { stale: false, tunnels: { awg11: { vpn_ip: '203.0.113.7', direct_ip: '198.51.100.4', changed: true, at: new Date().toISOString() } } },
  wan: { stale: false, links: [
    { label: 'Подключение Ethernet', role: 'primary', up: true, pingcheck: '' },
    { label: 'Huawei Mobile Broadband', role: 'backup', up: false, pingcheck: 'unset' },
  ] },
  hooks: { stale: false, state: 'installed', wakes_1h: 0, suppressed_1h: 0 },
  native_dns: { stale: false, lists: [{ name: 'youtube', domains: 14, tunnel_id: 'awg11', owner: 'firmware', issue: 'target_down' }] },
}

async function mount(vnode) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(vnode, root)
  })
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
  return { root, unmount: () => { render(null, root); root.remove() } }
}

const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent === text)

describe('секции v0.47', () => {
  it('адрес выхода по VPN-туннелю и кнопка замера', async () => {
    mocks.facts = FACTS
    mocks.sent = []
    mocks.result = { status: 'ok', output: '{}' }
    const { root, unmount } = await mount(<ExitIPSection routerID={2} tunnels={[{ tunnel_id: 'awg11', name: 'NL', run_state: 'running' }]} deadline={{ deadlineMs: 1000 }} />)
    expect(root.textContent).toContain('через VPN-туннель 203.0.113.7, напрямую 198.51.100.4')
    await act(async () => button(root, 'Проверить сейчас').click())
    expect(mocks.sent[0]).toEqual({ routerID: 2, action: 'exit_ip_probe', args: { tunnel_id: 'awg11' } })
    unmount()
  })

  it('экран VPN-туннеля: адрес и неудачи за сутки', async () => {
    mocks.facts = { ...FACTS, ping_fails_24h: { awg11: 7 } }
    const { root, unmount } = await mount(<ExitRow routerID={2} tunnelID="awg11" />)
    expect(root.textContent).toContain('Куда выходит трафик')
    expect(root.textContent).toContain('Неудачных проверок связи за сутки')
    expect(root.textContent).toContain('7')
    unmount()
  })

  it('старый агент -- секции нет', async () => {
    mocks.facts = { supported: false, ping_fails_24h: {} }
    const { root, unmount } = await mount(<ExitIPSection routerID={2} tunnels={[{ tunnel_id: 'awg11', name: 'NL', run_state: 'running' }]} />)
    expect(root.textContent).toBe('')
    unmount()
  })

  it('резервная линия с подсказкой про Ping-Check', async () => {
    mocks.facts = FACTS
    const { root, unmount } = await mount(<WANSection routerID={2} />)
    expect(root.textContent).toContain('Резервный интернет')
    expect(root.textContent).toContain('Ping-Check не задан')
    expect(root.textContent).toContain('Проверка доступности')
    unmount()
  })

  it('строка хука', async () => {
    mocks.facts = FACTS
    const { root, unmount } = await mount(<HooksRow routerID={2} />)
    expect(root.textContent).toContain('Мгновенная реакция на смену линии')
    expect(root.textContent).toContain('включена')
    unmount()
  })

  it('журнал awg-manager по кнопке', async () => {
    mocks.sent = []
    mocks.result = { status: 'ok', output: JSON.stringify({ enabled: true, entries: [{ ts: '2026-09-28T07:00:00Z', level: 'warn', group: 'routing', action: 'boot', message: 'listen-порт переехал: 12*****.1:9000' }] }) }
    const { root, unmount } = await mount(<AwgmLogsSection routerID={2} deadline={{ deadlineMs: 1000 }} />)
    await act(async () => button(root, 'предупреждения').click())
    await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
    expect(mocks.sent[0]).toEqual({ routerID: 2, action: 'awgm_logs', args: { level: 'warn', limit: 100 } })
    expect(root.textContent).toContain('listen-порт переехал: 12*****.1:9000')
    unmount()
  })

  it('списки сайтов прошивки', async () => {
    mocks.facts = FACTS
    const { root, unmount } = await mount(<NativeDNSSection routerID={2} tunnels={[{ id: 'awg11', name: 'NL' }]} />)
    expect(root.textContent).toContain('14 сайтов → VPN-туннель «NL»')
    expect(root.textContent).toContain('сайты из списка сейчас идут напрямую')
    unmount()
  })

  it('журнал видит владелец, но не оператор', async () => {
    mocks.facts = FACTS
    for (const [role, visible] of [['owner', true], ['operator', false]]) {
      mocks.settings = { role, agent_version: 'v0.47.0' }
      const { root, unmount } = await mount(<SettingsSections routerID={2} routerName="home" asleep={false} openSheet={() => {}} />)
      expect(root.textContent.includes('Журнал awg-manager')).toBe(visible)
      unmount()
    }
  })

  it('владелец со старым агентом -- секции журнала нет', async () => {
    mocks.facts = FACTS
    mocks.settings = { role: 'owner', agent_version: 'v0.46.0' }
    const { root, unmount } = await mount(<SettingsSections routerID={2} routerName="home" asleep={false} openSheet={() => {}} />)
    expect(root.textContent.includes('Журнал awg-manager')).toBe(false)
    unmount()
  })

  it('ни одна секция не обещает «весь трафик через VPN»', async () => {
    mocks.facts = FACTS
    const { root, unmount } = await mount(
      <div>
        <ExitIPSection routerID={2} tunnels={[{ tunnel_id: 'awg11', name: 'NL', run_state: 'running' }]} />
        <WANSection routerID={2} />
        <NativeDNSSection routerID={2} tunnels={[]} />
      </div>,
    )
    expect(root.textContent).not.toMatch(/весь трафик/i)
    unmount()
  })
})
