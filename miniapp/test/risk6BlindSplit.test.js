import { describe, it, expect } from 'vitest'
import { pathState, reserveLine } from '../src/trafficPath.js'

// Ревью v0.41 (Opus, 18.09).

describe('RISK 6: выключенный руками туннель -- не «мёртвый»', () => {
  it('старый агент, живой + остановленный без тревоги -- ветка не «не знаем»', () => {
    const tunnels = [
      { tunnel_id: 'awg10', name: 'nl', run_state: 'running', status: 'ok', matrix_latency_ms: 40 },
      { tunnel_id: 'awg11', name: 'old', run_state: 'stopped', status: 'ok' },
    ]
    const s = pathState({ traffic: { mode: 'split' }, incidents: [], tunnels })
    expect(s.tunnel).toBe('up')
  })

  it('остановленный с тревогой -- мёртвый, не угадываем', () => {
    const tunnels = [
      { tunnel_id: 'awg10', name: 'nl', run_state: 'running', status: 'ok' },
      { tunnel_id: 'awg11', name: 'old', run_state: 'stopped', status: 'fail' },
    ]
    const s = pathState({ traffic: { mode: 'split' }, incidents: [{ check_name: 'tunnel_awg11' }], tunnels })
    expect(s.tunnel).toBe('unknown')
    expect(reserveLine({ traffic: { mode: 'split' }, incidents: [], tunnels, via: '' })).toBeUndefined()
  })
})
