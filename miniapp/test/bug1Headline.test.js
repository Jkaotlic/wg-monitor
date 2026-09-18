import { describe, it, expect } from 'vitest'
import SNAP from './fixtures/split_reserve_dead.json'
import { routerHeadline } from '../src/routerHeadline.js'

// Ревью v0.41 (Opus, 18.09).
const ONLINE = { status: 'alert', last_seen_age_sec: 10 }
const WORK_TRAFFIC = { mode: 'split', egress_tunnel_id: 'awg14', egress_tunnel_name: 'vpn-hip', reserve_tunnel_ids: [] }

describe('BUG 1: «всё работает» -- только по слову сервера', () => {
  const tunnels = SNAP.events.tunnels
  const incidents = SNAP.incidents

  it('reserve_only_alert=true -- «всё работает, резерва нет»', () => {
    const h = routerHeadline({ router: ONLINE, traffic: WORK_TRAFFIC, incidents, tunnels, reserveOnlyAlert: true })
    expect(h.tag).toBe('всё работает, резерва нет')
  })

  // Две политики: несущий одной жив, туннель ДРУГОЙ политики мёртв. Сервер
  // знает, что это не запасной, и reserve_only_alert не ставит.
  it('две политики: несущий жив, туннель другой политики мёртв -- не «всё работает»', () => {
    const h = routerHeadline({ router: ONLINE, traffic: WORK_TRAFFIC, incidents, tunnels, reserveOnlyAlert: false })
    expect(h.tag).not.toMatch(/всё работает/)
    expect(h.tone).toBe('danger')
  })

  it('без признака (старый бэкенд) -- тоже не «всё работает»', () => {
    const h = routerHeadline({ router: ONLINE, traffic: WORK_TRAFFIC, incidents, tunnels })
    expect(h.tag).not.toMatch(/всё работает/)
  })

  it('[запасной туннель, dns] -- тревога dns не прячется за «всё работает»', () => {
    const both = [{ check_name: 'tunnel_awg10' }, { check_name: 'dns' }]
    const h = routerHeadline({ router: ONLINE, traffic: WORK_TRAFFIC, incidents: both, tunnels, reserveOnlyAlert: true })
    expect(h.tone).toBe('danger')
    expect(h.tag).not.toMatch(/всё работает/)
    expect(h.check).toBe('dns')
  })
})
