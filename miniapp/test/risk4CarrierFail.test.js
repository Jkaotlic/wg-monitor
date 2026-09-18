import { describe, it, expect } from 'vitest'
import { pathState } from '../src/trafficPath.js'

// Ревью v0.41 (Opus, 18.09).

describe('RISK 4: проваленная проверка несущего без тревоги -- ветка «молчит»', () => {
  it('status fail, тревоги ещё нет -- down, как и в шапке', () => {
    const tunnels = [{ tunnel_id: 'awg14', name: 'vpn-hip', run_state: 'running', status: 'fail' }]
    const s = pathState({ traffic: { mode: 'vpn', egress_tunnel_id: 'awg14' }, incidents: [], tunnels })
    expect(s.tunnel).toBe('down')
  })
})
