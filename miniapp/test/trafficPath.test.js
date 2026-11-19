import { describe, it, expect } from 'vitest'
import { deadReserveLine, pathState } from '../src/trafficPath.js'

// Форма линии -- как её отдаёт бэкенд (internal/backend/miniapp_tunnels.go):
// run_state -- слово роутера о туннеле, status -- вердикт проверки. Путать их
// значит рисовать зелёную ветку над остановленной линией.

describe('pathState', () => {
  it('живая линия и прямой поток', () => {
    const s = pathState({
      traffic: { mode: 'vpn', egress_tunnel_id: 'awg12', egress_tunnel_name: 'Амстердам' },
      incidents: [],
      tunnels: [{ tunnel_id: 'awg12', run_state: 'running', status: 'ok', matrix_latency_ms: 84 }],
    })
    expect(s.tunnel).toBe('up')
    expect(s.direct).toBe('unknown')
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
    expect(s.direct).toBe('unknown')
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

  it('backend-конфликт запрещает локальной схеме быть зелёной', () => {
    const s = pathState({
      traffic: { mode: 'vpn', egress_tunnel_id: 'awg12' },
      incidents: [],
      tunnels: [{ tunnel_id: 'awg12', run_state: 'running' }],
      evidence: { state: 'degraded', reason_code: 'source_conflict' },
    })
    expect(s.tunnel).toBe('unknown')
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
  // B1 (v0.56): ветка живая, но несущего на sing-box нет -- имя и задержка
  // первой поднятой линии были угадыванием.
  it('ветка живая, но первую поднятую линию несущей не называет', () => {
    const s = pathState({
      traffic: { mode: 'singbox' },
      incidents: [],
      tunnels: [
        { tunnel_id: 'awg10', name: 'Франкфурт', run_state: 'stopped' },
        { tunnel_id: 'awg12', name: 'Амстердам', run_state: 'running', status: 'ok', matrix_latency_ms: 84 },
      ],
    })
    expect(s.tunnel).toBe('up')
    expect(s.via).toBe('')
    expect(s.latencyMs).toBeNull()
  })

  it('без единой поднятой линии честно говорит, что не знает', () => {
    const s = pathState({ traffic: { mode: 'singbox' }, incidents: [], tunnels: [] })
    expect(s.tunnel).toBe('unknown')
    expect(s.via).toBe('')
  })
})

it('после потери связи прежний отказ не выдаётся за состояние прямо сейчас', () => {
  expect(pathState({ stale: true, evidence: { state: 'failed' } }).tunnel).toBe('unknown')
})

it('свежий отчёт с истёкшим замером не показывает прежнюю задержку', () => {
  const nowMs = Date.UTC(2026, 10, 18, 12)
  const row = { tunnel_id: 'a', status: 'ok', run_state: 'running', ts: new Date(nowMs).toISOString(),
    matrix_latency_ms: 40, matrix_updated_at: new Date(nowMs - 360000).toISOString() }
  const state = pathState({ traffic: { egress_tunnel_id: 'a' }, tunnels: [row], freshness: { nowMs, enforceFreshness: true } })
  expect(state.tunnel).toBe('up')
  expect(state.latencyMs).toBeNull()
})

it.each(['ok', 'fail'])('устаревшая проверка %s не задаёт состояние ветки', (status) => {
  const nowMs = Date.UTC(2026, 10, 18, 12)
  const traffic = { carrier_basis: 'policy', carrier_tunnel_id: 'a', carrier_alive: status === 'ok' }
  const tunnels = [{ tunnel_id: 'a', status, run_state: 'running', ts: new Date(nowMs - 360000).toISOString() }]
  expect(pathState({ traffic, tunnels, freshness: { nowMs, enforceFreshness: true } }).tunnel).toBe('unknown')
})

it('отказ туннеля другой политики не означает потерю резерва', () => {
  const traffic = { carrier_tunnel_id: 'main', carrier_basis: 'policy', fallback_tunnel_ids: ['spare'] }
  const tunnels = [{ tunnel_id: 'other', run_state: 'running', status: 'fail' }]
  expect(deadReserveLine({ traffic, tunnels })).toBeUndefined()
})
