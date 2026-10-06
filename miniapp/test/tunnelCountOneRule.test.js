import { describe, it, expect } from 'vitest'
import { workingTunnelCount, uncheckedTunnelCount, tunnelCountSummary } from '../src/labels.js'
import { withCheckVerdict } from '../src/routes.js'
import { tunnelList, tunnelListSummary } from '../src/tunnelDelete.js'

// v0.56, спека B3: «Роутер»/«Проверки» и вкладка «VPN-туннели» считают
// VPN-туннели одним определением: «свой» -- управляемый awg-manager,
// «работает» -- поднят, проверка не провалена и не «не проверено».
// Общая фикстура: три своих VPN-туннеля (один с проваленной проверкой,
// один остановлен) и один чужой NDMS-интерфейс.
const snapshot = {
  tunnels: [
    { id: 'awg10', name: 'amsterdam', type: 'managed', status: 'running', enabled: true },
    { id: 'awg11', name: 'berlin', type: 'managed', status: 'running', enabled: true },
    { id: 'awg12', name: 'old', type: 'managed', status: 'stopped', enabled: false },
    { id: 'Wireguard0', name: 'provider', type: 'system', status: 'running', enabled: true },
  ],
  counts: {},
  policies: [],
}
const events = {
  tunnels: [
    { tunnel_id: 'awg10', status: 'ok', run_state: 'running', enabled: true, handshake_age_sec: 20 },
    { tunnel_id: 'awg11', status: 'fail', run_state: 'running', enabled: true, handshake_age_sec: 20 },
    { tunnel_id: 'awg12', status: 'ok', run_state: 'stopped', enabled: false, handshake_age_sec: 20 },
  ],
}

describe('B3: счёт VPN-туннелей', () => {
  it('главная и вкладка дают один счёт на общей фикстуре', () => {
    const main = tunnelCountSummary(events.tunnels, [])
    const tab = tunnelListSummary(tunnelList(withCheckVerdict(snapshot, events)), [], events.tunnels)
    expect(main).toEqual({ total: 3, working: 1, unchecked: 0 })
    expect(tab).toEqual(main)
  })

  it('«не проверено» не работающий и не упавший -- на обоих экранах', () => {
    const ev = { tunnels: events.tunnels.map((t) => (t.tunnel_id === 'awg11' ? { ...t, status: 'unknown' } : t)) }
    const main = tunnelCountSummary(ev.tunnels, [])
    const tab = tunnelListSummary(tunnelList(withCheckVerdict(snapshot, ev)), [], ev.tunnels)
    expect(main).toEqual({ total: 3, working: 1, unchecked: 1 })
    expect(tab).toEqual(main)
  })

  it('чужой VPN-туннель (type system) не считается своим, даже если он пришёл строкой', () => {
    const rows = [...events.tunnels, { tunnel_id: 'Wireguard0', status: 'ok', run_state: 'running', enabled: true, type: 'system' }]
    expect(tunnelCountSummary(rows, []).total).toBe(3)
  })

  it('старые функции счёта сходятся с общей', () => {
    expect(workingTunnelCount(events.tunnels, [])).toBe(1)
    expect(uncheckedTunnelCount(events.tunnels)).toBe(0)
  })

  it('тревога по туннелю снимает его из работающих на обоих экранах', () => {
    const inc = [{ check_name: 'tunnel_awg10' }]
    expect(tunnelCountSummary(events.tunnels, inc).working).toBe(0)
    expect(tunnelListSummary(tunnelList(withCheckVerdict(snapshot, events)), inc, events.tunnels).working).toBe(0)
  })
})

// Живые данные песочницы (fixtures/sandbox_counts.json -- снято с настоящего
// бэкенда песочницы: ответ /events, тревоги и снимок route_status). Общая
// фикстура выше проходила, а на этих данных экраны расходились:
// «2 из 3» на «Роутере» против «4 из 4» на «VPN-туннелях».
import sandbox from './fixtures/sandbox_counts.json'

describe.each(['sandbox-broken', 'sandbox-work'])('B3 на данных песочницы: %s', (name) => {
  const s = sandbox[name]
  it('«Роутер»/«Проверки» и вкладка дают один счёт', () => {
    const main = tunnelCountSummary(s.events.tunnels, s.incidents)
    const tab = tunnelListSummary(tunnelList(withCheckVerdict(s.snapshot, s.events)), s.incidents, s.events.tunnels)
    expect(tab).toEqual(main)
    expect(main.total).toBe(s.events.tunnels.length)
  })

  it('без загруженных проверок вкладка не придумывает чужое: чужой NDMS-интерфейс не считается', () => {
    const tab = tunnelListSummary(tunnelList(s.snapshot), [], [])
    expect(tab.total).toBe(s.snapshot.tunnels.filter((t) => t.type === 'managed').length)
  })
})
