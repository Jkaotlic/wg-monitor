import { describe, it, expect } from 'vitest'
import SNAP from './fixtures/split_reserve_dead.json'
import { pathState, reserveLine } from '../src/trafficPath.js'
import { routerHeadline } from '../src/routerHeadline.js'
import { fleetRow } from '../src/fleet.js'

// v0.41, спека A3–A5. Прод 18.09 07:58, workrouter: обход идёт через awg14
// (hipvps, жив, 117 мс), запасное звено awg10 мертво. Экран рисовал красную
// ветку «VPN-туннель молчит», «Запасной готов» и «работает на запасном» --
// схема и заголовок угадывали РАЗНЫЕ туннели. Фикстура -- обезличенный снимок
// ответов прода (до новых полей egress/reserve).

function screen(traffic) {
  const { router, incidents } = SNAP
  const { tunnels } = SNAP.events
  const headline = routerHeadline({ router, traffic, incidents, tunnels })
  const path = pathState({ traffic, incidents, tunnels, stale: headline.stale })
  const reserve = reserveLine({ traffic, incidents, tunnels, via: path.via })
  return { headline, path, reserve }
}

describe('реплей workrouter 18.09: старый агент (несущий не назван)', () => {
  const s = screen(SNAP.events.traffic)

  it('живой несущий не красится красным: ветка «не знаем», а не «молчит»', () => {
    expect(s.path.tunnel).not.toBe('down')
    expect(s.path.tunnel).toBe('unknown')
    expect(s.path.via).toBe('')
    expect(s.path.latencyMs).toBeNull()
  })

  it('«Запасной готов» не выдумывается: живое звено одно', () => {
    expect(s.reserve).toBeUndefined()
  })

  it('заголовок не называет ни одного туннеля', () => {
    expect(s.headline.verdict).not.toContain('vpn-nl')
    expect(s.headline.verdict).not.toContain('vpn-hip')
  })
})

describe('реплей workrouter 18.09: агент назвал несущего', () => {
  const traffic = {
    ...SNAP.events.traffic,
    egress_tunnel_id: 'awg14',
    egress_tunnel_name: 'vpn-hip',
    reserve_tunnel_ids: [],
  }
  const s = screen(traffic)

  it('ветка VPN зелёная, через несущего, с его задержкой', () => {
    expect(s.path.tunnel).toBe('up')
    expect(s.path.via).toBe('vpn-hip')
    expect(s.path.latencyMs).toBe(117)
  })

  it('заголовок: всё работает, резерва нет -- и называет тот же туннель, что схема', () => {
    expect(s.headline.tone).toBe('warn')
    expect(s.headline.tag).toBe('всё работает, резерва нет')
    expect(s.headline.verdict).toContain('«vpn-hip»')
    expect(s.headline.verdict).toContain('«vpn-nl»')
    expect(s.headline.verdict).toMatch(/подхватить некому/)
  })

  it('резерв -- по reserve_tunnel_ids, пустой значит «нет»', () => {
    expect(s.reserve).toBeUndefined()
  })
})

describe('несущий известен', () => {
  const ONLINE = { status: 'alert', last_seen_age_sec: 10 }
  const tunnels = [
    { tunnel_id: 'awg10', name: 'nl2', run_state: 'running', status: 'ok', matrix_latency_ms: 50 },
    { tunnel_id: 'awg14', name: 'hipvps', run_state: 'running', status: 'fail', matrix_latency_ms: 90 },
  ]

  it('несущий мёртв -- тревога как раньше, ветка красная', () => {
    const traffic = { mode: 'split', egress_tunnel_id: 'awg14', egress_tunnel_name: 'hipvps', reserve_tunnel_ids: ['awg10'] }
    const incidents = [{ check_name: 'tunnel_awg14' }]
    const h = routerHeadline({ router: ONLINE, traffic, incidents, tunnels })
    const p = pathState({ traffic, incidents, tunnels })
    expect(h.tone).toBe('danger')
    expect(p.tunnel).toBe('down')
    expect(p.via).toBe('hipvps')
  })

  it('запасной берётся из reserve_tunnel_ids, а не из числа поднятых', () => {
    const traffic = { mode: 'split', egress_tunnel_id: 'awg10', egress_tunnel_name: 'nl2', reserve_tunnel_ids: ['awg14'] }
    expect(reserveLine({ traffic, tunnels, incidents: [], via: 'nl2' })?.tunnel_id).toBe('awg14')
    const none = { ...traffic, reserve_tunnel_ids: [] }
    expect(reserveLine({ traffic: none, tunnels, incidents: [], via: 'nl2' })).toBeUndefined()
  })

  it('упал один из двух запасных -- тег не говорит «резерва нет»', () => {
    const three = [...tunnels, { tunnel_id: 'awg15', name: 'fi', run_state: 'running', status: 'ok' }]
    const traffic = { mode: 'split', egress_tunnel_id: 'awg10', egress_tunnel_name: 'nl2', reserve_tunnel_ids: ['awg15'] }
    const h = routerHeadline({ router: ONLINE, traffic, incidents: [{ check_name: 'tunnel_awg14' }], tunnels: three })
    expect(h.tone).toBe('warn')
    expect(h.tag).not.toMatch(/резерва нет/)
    expect(h.verdict).toContain('«nl2»')
  })
})

describe('строка списка роутеров', () => {
  it('тревога только по запасному -- янтарное «резерв не работает»', () => {
    const r = fleetRow({ id: 1, nickname: 'w', status: 'alert', last_seen_age_sec: 5, reserve_only_alert: true, active_incidents: [{ check_name: 'tunnel_awg10' }] })
    expect(r.pill).toEqual({ tone: 'warn', text: 'резерв не работает' })
  })

  it('обычная тревога -- по-прежнему красная', () => {
    const r = fleetRow({ id: 1, nickname: 'w', status: 'alert', last_seen_age_sec: 5 })
    expect(r.pill).toEqual({ tone: 'danger', text: 'тревога' })
  })
})
