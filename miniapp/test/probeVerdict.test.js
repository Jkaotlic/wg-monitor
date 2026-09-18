import { describe, it, expect } from 'vitest'
import { withCheckVerdict, tunnelLive } from '../src/routes.js'
import { tunnelList } from '../src/tunnelDelete.js'

// workrouter 18.09: интерфейс nl21 поднят (route_status: running), а проверка
// tunnel_awg10 провалена -- awg-manager пишет «нет связи». Вкладка «VPN-туннели»
// подписывала его «работает».
const snap = {
  tunnels: [
    { id: 'awg10', name: 'vpn-nl', type: 'managed', status: 'running', enabled: true, has_handshake: true, handshake_age_sec: 44 },
    { id: 'awg11', name: 'vpn-hip', type: 'managed', status: 'running', enabled: true, has_handshake: true, handshake_age_sec: 10 },
  ],
  policies: [],
}
const events = { tunnels: [{ tunnel_id: 'awg10', status: 'fail', run_state: 'running' }, { tunnel_id: 'awg11', status: 'ok', run_state: 'running' }] }

describe('вердикт проверок на вкладке «VPN-туннели»', () => {
  it('проваленная проверка -- «не отвечает», даже при поднятом интерфейсе', () => {
    const s = withCheckVerdict(snap, events)
    expect(tunnelLive(s.tunnels[0])).toBe('down')
    expect(tunnelLive(s.tunnels[1])).toBe('up')
    const rows = tunnelList(s)
    expect(rows.find((r) => r.id === 'awg10').stateLabel).toBe('не отвечает')
  })
  it('исходный снимок не меняется, без проверок -- как есть', () => {
    withCheckVerdict(snap, events)
    expect(snap.tunnels[0].status).toBe('running')
    expect(withCheckVerdict(snap, null)).toBe(snap)
  })
})
