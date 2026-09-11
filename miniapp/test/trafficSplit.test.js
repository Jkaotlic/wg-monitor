import { describe, it, expect } from 'vitest'
import { routerHeadline } from '../src/routerHeadline.js'
import { trafficLabel } from '../src/labels.js'
import { pathState } from '../src/trafficPath.js'

// Главный выход «напрямую» при правилах обхода -- обычная раздельная
// маршрутизация, а не поломка. Так настроены рабочий роутер и testkeen, и
// жёлтое «обход блокировок не работает» на них было неправдой.

const ONLINE = { status: 'online', nickname: 'workrouter', last_seen_age_sec: 20 }
const TWO_LIVE = [
  { tunnel_id: 'awg10', name: 'vpn-nl', run_state: 'running', matrix_latency_ms: 40 },
  { tunnel_id: 'awg14', name: 'vpn-de', run_state: 'running', matrix_latency_ms: 70 },
]

describe('раздельная маршрутизация на главном экране', () => {
  it('правила ведут обход -- всё работает, а не предупреждение', () => {
    const h = routerHeadline({ router: ONLINE, traffic: { mode: 'split' }, tunnels: TWO_LIVE })
    expect(h.tone).toBe('sig')
    expect(h.cold).toBe(false)
    expect(h.tag).toBe('всё работает')
    expect(h.verdict).toContain('напрямую')
    expect(h.verdict).not.toMatch(/не работает|не открыва/)
  })

  it('не называет VPN-туннель, который роутер не назвал', () => {
    const h = routerHeadline({ router: ONLINE, traffic: { mode: 'split' }, tunnels: TWO_LIVE })
    for (const t of TWO_LIVE) expect(h.verdict).not.toContain(t.name)
  })

  it('называет VPN-туннель, когда он единственный живой', () => {
    const h = routerHeadline({
      router: ONLINE,
      traffic: { mode: 'split', egress_tunnel_id: 'awg14', egress_tunnel_name: 'vpn-de' },
      tunnels: TWO_LIVE,
    })
    expect(h.verdict).toContain('«vpn-de»')
  })

  it('«напрямую» без правил говорит словами владельца', () => {
    const h = routerHeadline({ router: ONLINE, traffic: { mode: 'direct' }, tunnels: TWO_LIVE })
    expect(h.tone).toBe('warn')
    expect(h.verdict).not.toContain('основной маршрут')
    expect(h.verdict).toContain('напрямую')
  })
})

describe('подпись режима трафика', () => {
  it('раздельная маршрутизация не обещает «весь трафик через»', () => {
    const l = trafficLabel({ mode: 'split' })
    expect(l.title).not.toMatch(/неизвестно/i)
    expect(l.detail).not.toMatch(/Весь исходящий/)
    expect(l.detail).toContain('напрямую')
  })

  it('«напрямую» без жаргона про основной маршрут', () => {
    expect(trafficLabel({ mode: 'direct' }).detail).not.toContain('основной маршрут')
  })
})

describe('схема пути при раздельной маршрутизации', () => {
  it('ветка обхода живая, но имя и задержку не угадывает', () => {
    const s = pathState({ traffic: { mode: 'split' }, tunnels: TWO_LIVE })
    expect(s.tunnel).toBe('up')
    expect(s.via).toBe('')
    expect(s.latencyMs).toBeNull()
  })

  it('названный VPN-туннель рисуется с его задержкой', () => {
    const s = pathState({
      traffic: { mode: 'split', egress_tunnel_id: 'awg14', egress_tunnel_name: 'vpn-de' },
      tunnels: TWO_LIVE,
    })
    expect(s.via).toBe('vpn-de')
    expect(s.latencyMs).toBe(70)
  })
})
