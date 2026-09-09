import { describe, it, expect } from 'vitest'
import { pathState } from '../src/trafficPath.js'

describe('pathState', () => {
  it('живая линия и прямой поток', () => {
    const s = pathState({
      traffic: { mode: 'vpn', egress_tunnel_id: 'awg12', egress_tunnel_name: 'Амстердам' },
      incidents: [],
      tunnels: [{ tunnel_id: 'awg12', status: 'running', matrix_latency_ms: 84 }],
    })
    expect(s.tunnel).toBe('up')
    expect(s.direct).toBe('up')
    expect(s.via).toBe('Амстердам')
    expect(s.latencyMs).toBe(84)
  })

  // Упавший туннель гасит ТОЛЬКО свою ветку: прямой поток жив, и человек
  // обязан это видеть -- иначе решит, что интернета нет вовсе.
  it('упавшая линия не гасит прямой поток', () => {
    const s = pathState({
      traffic: { mode: 'vpn', egress_tunnel_id: 'awg12' },
      incidents: [{ check_name: 'tunnel_awg12' }],
      tunnels: [{ tunnel_id: 'awg12', status: 'down' }],
    })
    expect(s.tunnel).toBe('down')
    expect(s.direct).toBe('up')
  })

  // Без матрицы задержки нет -- и рисовать ноль нельзя: ноль читается как
  // «мгновенно», то есть как лучшая линия из возможных.
  it('без задержки отдаёт null, а не ноль', () => {
    const s = pathState({
      traffic: { mode: 'vpn', egress_tunnel_id: 'awg12' },
      incidents: [],
      tunnels: [{ tunnel_id: 'awg12', status: 'running' }],
    })
    expect(s.latencyMs).toBeNull()
  })

  // Молчащий роутер: показания -- на момент последнего отчёта, и рисовать по
  // ним живую схему значит выдавать прошлое за настоящее.
  it('на молчащем роутере обе ветки неизвестны', () => {
    const s = pathState({ traffic: null, incidents: [], tunnels: [], stale: true })
    expect(s.tunnel).toBe('unknown')
    expect(s.direct).toBe('unknown')
  })
})
