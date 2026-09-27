import { describe, it, expect } from 'vitest'
import { tunnelsView } from '../src/tunnelsView.js'
import { policyRows, tunnelRows } from '../src/routes.js'

// MINI-09: в wire.RoutePolicySummary поля static НЕТ (pkg/wire/routing.go):
// статические маршруты агент кладёт в counts[<tunnel_id>].static
// (route_status.go). «Несёт N» у активного туннеля обязан их учитывать.
const SNAP = {
  policy_model: true,
  tunnels: [{ id: 'awg11', name: 'vymysel-work', iface: 'opkgtun11', type: 'managed', status: 'running' }],
  policies: [
    {
      name: 'HydraRoute', dns: 26, hr_neo: 26, via_vpn: true, active_tunnel_id: 'awg11',
      interfaces: [{ bind: 'OpkgTun11', name: 'vymysel-work', role: 'active', tunnel_id: 'awg11', via_vpn: true }],
    },
  ],
  counts: { awg11: { dns: 1, static: 4, hr_neo: 0 } },
}

describe('MINI-09', () => {
  it('активный туннель несёт правила политики, свои DNS и статические маршруты', () => {
    const v = tunnelsView(SNAP)
    expect(v.active.rules).toBe(31)
    // То же число, что в строке туннеля раскладки.
    expect(v.active.rules).toBe(tunnelRows(SNAP).find((r) => r.id === 'awg11').total)
  })
  it('строка политики не читает несуществующее поле static', () => {
    const rows = policyRows({ policies: [{ name: 'HydraRoute', dns: 26, static: 99, interfaces: [] }] })
    expect(rows[0].rules).toBe(26)
  })
})
