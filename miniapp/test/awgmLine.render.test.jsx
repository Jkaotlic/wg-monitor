// @vitest-environment jsdom
// v0.65: у каждого VPN-туннеля -- строка «awg-manager: <слово>» рядом с
// вердиктом бота. Расхождение router4car4 02.11 (бот «работает», awg-manager
// «Нет связи») обязано быть видно на звене цепочки, у неиспользуемого и на
// экране VPN-туннеля.
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const SNAP = {
  policy_model: true,
  tunnels: [
    { id: 'awg11', name: 'vpn-nl', iface: 'opkgtun11', type: 'managed', status: 'running', enabled: true, has_handshake: true, handshake_age_sec: 18 },
    { id: 'awg20', name: 'vpn-de', iface: 'nwg0', type: 'managed', status: 'running', enabled: true, has_handshake: true, handshake_age_sec: 40 },
    { id: 'awg21', name: 'vpn-fi', iface: 'opkgtun21', type: 'managed', status: 'running', enabled: true, has_handshake: true, handshake_age_sec: 40 },
  ],
  policies: [
    {
      name: 'HydraRoute', dns: 26, hr_neo: 26, via_vpn: true, active_tunnel_id: 'awg11',
      interfaces: [
        { bind: 'OpkgTun11', name: 'vpn-nl', role: 'active', tunnel_id: 'awg11', via_vpn: true },
        { bind: 'Wireguard0', name: 'vpn-de', role: 'fallback', tunnel_id: 'awg20', via_vpn: true },
      ],
    },
  ],
  counts: {},
}

// Бот считает все три рабочими; awg-manager у резерва и неиспользуемого
// пишет «Нет связи», у несущего -- «Активен».
const EVENT_TUNNELS = [
  { tunnel_id: 'awg11', status: 'ok', run_state: 'running', awgm_conn: 'connected', awgm_conn_latency_ms: 64 },
  { tunnel_id: 'awg20', status: 'ok', run_state: 'running', awgm_conn: 'disconnected', awgm_conn_reason: 'timeout' },
  { tunnel_id: 'awg21', status: 'ok', run_state: 'running', awgm_conn: 'disconnected' },
]

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: EVENT_TUNNELS }),
  fetchRouterChecksWithIncidents: () => Promise.resolve({ incidents: [], checks: [], tunnels: EVENT_TUNNELS }),
  fetchRouterSettings: () => Promise.resolve({ role: 'owner' }),
  listAutorepair: () => Promise.resolve({ tunnels: [] }),
  getAutorepair: () => Promise.reject(new Error('нет')),
}))
const CMD = vi.hoisted(() => ({ value: null }))
vi.mock('../src/useCommand.js', () => ({ useCommand: () => CMD.value }))
CMD.value = { busy: false, result: { status: 'ok', output: JSON.stringify(SNAP) }, error: null, errorCode: null, run: () => Promise.resolve(null) }

const { TunnelsTab } = await import('../src/screens/TunnelsTab.jsx')
const { TunnelScreen } = await import('../src/screens/TunnelScreen.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function mount(node) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(node, root))
  await flush()
  return root
}

const lines = (root, sel) => [...root.querySelectorAll(sel)].map((el) => el.textContent)

describe('строка «awg-manager» на вкладке «VPN-туннели»', () => {
  it('звено цепочки: бот «готов», awg-manager «Нет связи» -- обе', async () => {
    const root = await mount(<TunnelsTab routerID={7} asleep={false} openSheet={() => {}} />)
    const link = [...root.querySelectorAll('.chain-link')].find((el) => el.textContent.includes('vpn-de'))
    expect(link).toBeTruthy()
    expect(link.querySelector('.awgm-line').textContent).toBe('awg-manager: Нет связи · timeout')
    // Цвет -- существующими классами тона экрана.
    expect(link.querySelector('.awgm-line .data-row-dot-danger')).toBeTruthy()
    expect(link.querySelector('.awgm-line .data-row-value-danger')).toBeTruthy()
    render(null, root)
    root.remove()
  })

  it('неиспользуемый -- «Нет связи»', async () => {
    const root = await mount(<TunnelsTab routerID={7} asleep={false} openSheet={() => {}} />)
    const row = [...root.querySelectorAll('.settings-row')].find((el) => el.textContent.includes('vpn-fi'))
    expect(row).toBeTruthy()
    expect(row.querySelector('.awgm-line').textContent).toBe('awg-manager: Нет связи')
    render(null, root)
    root.remove()
  })

  it('несущий -- слово рядом с героем', async () => {
    const root = await mount(<TunnelsTab routerID={7} asleep={false} openSheet={() => {}} />)
    expect(lines(root, '.hero .awgm-line, .awgm-line-hero')).toContain('awg-manager: Активен · 64 мс')
    expect(root.querySelector('.awgm-line .data-row-dot-ok')).toBeTruthy()
    render(null, root)
    root.remove()
  })
})

describe('строка «awg-manager» на экране VPN-туннеля', () => {
  it('«Нет связи» с причиной', async () => {
    const root = await mount(
      <TunnelScreen routerID={7} snapshot={SNAP} eventTunnels={EVENT_TUNNELS} tunnelID="awg20" role="owner" openSheet={() => {}} onClose={() => {}} />,
    )
    const row = [...root.querySelectorAll('.data-row')].find((el) => el.textContent.startsWith('awg-manager'))
    expect(row).toBeTruthy()
    expect(row.textContent).toBe('awg-managerНет связиtimeout')
    expect(row.querySelector('.data-row-value-danger')).toBeTruthy()
    render(null, root)
    root.remove()
  })

  it('без проверки -- строки нет, а не догадка', async () => {
    const root = await mount(<TunnelScreen routerID={7} snapshot={SNAP} tunnelID="awg20" role="owner" openSheet={() => {}} onClose={() => {}} />)
    expect(root.textContent).not.toContain('awg-manager')
    render(null, root)
    root.remove()
  })
})
