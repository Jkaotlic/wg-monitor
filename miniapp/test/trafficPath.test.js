import { describe, it, expect } from 'vitest'
import { pathState } from '../src/trafficPath.js'

// Форма линии -- как её отдаёт бэкенд (internal/backend/miniapp_tunnels.go):
// run_state -- слово роутера о туннеле, status -- вердикт проверки. Путать их
// значит рисовать зелёную ветку над остановленной линией.

describe('pathState', () => {
  it('живая линия и прямой поток', () => {
    const s = pathState({
      traffic: { mode: 'vpn', egress_tunnel_id: 'awg12', egress_tunnel_name: 'Амстердам' },
      incidents: [],
      tunnels: [{ tunnel_id: 'awg12', run_state: 'running', matrix_latency_ms: 84 }],
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
      tunnels: [{ tunnel_id: 'awg12', run_state: 'stopped' }],
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
      tunnels: [{ tunnel_id: 'awg12', run_state: 'running' }],
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

// На sing-box-роутере единой «линии выхода» нет: маршрут выбирается для
// каждого адреса. Но линия, через которую идёт обход, всё равно существует --
// и написать «роутер не сказал» там, где она поднята, значит соврать.
describe('pathState на sing-box', () => {
  it('берёт поднятую линию, когда единого выхода нет', () => {
    const s = pathState({
      traffic: { mode: 'singbox' },
      incidents: [],
      tunnels: [
        { tunnel_id: 'awg10', name: 'Франкфурт', run_state: 'stopped' },
        { tunnel_id: 'awg12', name: 'Амстердам', run_state: 'running', matrix_latency_ms: 84 },
      ],
    })
    expect(s.tunnel).toBe('up')
    expect(s.via).toBe('Амстердам')
    expect(s.latencyMs).toBe(84)
  })

  it('без единой поднятой линии честно говорит, что не знает', () => {
    const s = pathState({ traffic: { mode: 'singbox' }, incidents: [], tunnels: [] })
    expect(s.tunnel).toBe('unknown')
    expect(s.via).toBe('')
  })
})
