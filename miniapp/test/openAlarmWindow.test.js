import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { withCheckVerdict, withOpenAlarms } from '../src/routes.js'
import { tunnelsView } from '../src/tunnelsView.js'
import { tunnelList, tunnelCard, tunnelsTabSummary, ALARM_OPEN_LABEL } from '../src/tunnelDelete.js'

// Окно «тревога по VPN-туннелю ещё открыта, а последняя проверка уже ok»
// (восстановление занимает 2-3 отчёта): экран VPN-туннеля, герой и цепочка
// вкладки, строка списка и счёт говорят одно и то же.

const snapshot = {
  tunnels: [
    { id: 'awg10', name: 'amsterdam', type: 'managed', status: 'running', enabled: true, has_handshake: true, handshake_age_sec: 20 },
    { id: 'awg11', name: 'berlin', type: 'managed', status: 'running', enabled: true },
  ],
  counts: { awg10: { dns: 5, static: 0, hr_neo: 0 } },
  policies: [
    {
      name: 'main',
      active_tunnel_id: 'awg10',
      via_vpn: true,
      dns: 5,
      hr_neo: 0,
      interfaces: [
        { tunnel_id: 'awg10', name: 'amsterdam', bind: 'OpkgTun10', role: 'primary' },
        { tunnel_id: 'awg11', name: 'berlin', bind: 'OpkgTun11', role: 'backup' },
      ],
    },
  ],
}
const events = {
  checks: [],
  tunnels: [
    { tunnel_id: 'awg10', name: 'amsterdam', status: 'ok', run_state: 'running', enabled: true, handshake_age_sec: 20 },
    { tunnel_id: 'awg11', name: 'berlin', status: 'ok', run_state: 'running', enabled: true, handshake_age_sec: 20 },
  ],
  traffic: { mode: 'split', carrier_basis: 'policy', carrier_tunnel_id: 'awg10', carrier_alive: true },
}
const open = [{ check_name: 'tunnel_awg10', severity: 'HARD' }]

// Так вкладка «VPN-туннели» собирает всё, что показывает.
function screens(incidents) {
  const checks = { ...events, incidents }
  const shown = withOpenAlarms(withCheckVerdict(snapshot, checks), incidents)
  const view = tunnelsView(shown, checks.traffic)
  const list = tunnelList(shown, incidents)
  return {
    tunnelScreen: tunnelCard(shown, 'awg10').stateLabel,
    hero: view.active.alarmOpen ? ALARM_OPEN_LABEL : view.active.live === 'down' ? 'down' : 'поднят',
    chain: view.chain.find((c) => c.tunnelID === 'awg10').note,
    chainRole: view.chain.find((c) => c.tunnelID === 'awg10').role,
    row: list.find((r) => r.id === 'awg10').stateLabel,
    working: tunnelsTabSummary(list, checks).counts.working,
  }
}

describe('окно «тревога открыта, проверка ok»', () => {
  it('экран VPN-туннеля, герой, цепочка, список и счёт говорят одно', () => {
    const s = screens(open)
    expect(s.tunnelScreen).toBe(ALARM_OPEN_LABEL)
    expect(s.hero).toBe(ALARM_OPEN_LABEL)
    expect(s.chain).toBe(ALARM_OPEN_LABEL)
    expect(s.row).toBe(ALARM_OPEN_LABEL)
    expect(s.working).toBe(1) // берлин работает, амстердам -- нет
  })

  it('тревоги нет -- «работает» везде', () => {
    const s = screens([])
    expect(s.tunnelScreen).toBe('работает')
    expect(s.hero).toBe('поднят')
    expect(s.row).toBe('работает')
    expect(s.chainRole).toBe('active')
    expect(s.working).toBe(2)
  })

  it('чужая тревога не задевает другой VPN-туннель', () => {
    const shown = withOpenAlarms(withCheckVerdict(snapshot, events), open)
    expect(tunnelCard(shown, 'awg11').stateLabel).toBe('работает')
  })

  it('экран вкладки отдаёт тревоги в общий снимок', () => {
    const tab = readFileSync(new URL('../src/screens/TunnelsTab.jsx', import.meta.url), 'utf8')
    expect(tab).toMatch(/withOpenAlarms\(/)
  })
})
