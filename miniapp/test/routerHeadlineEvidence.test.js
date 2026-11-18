import { describe, expect, it } from 'vitest'
import { routerHeadline } from '../src/routerHeadline.js'

const withReserveIncident = {
  router: { reach: 'online', last_seen_age_sec: 10 },
  traffic: { mode: 'split', carrier_basis: 'policy', carrier_tunnel_id: 'wg0', carrier_alive: true },
  tunnels: [
    { tunnel_id: 'wg0', run_state: 'running', status: 'ok', enabled: true },
    { tunnel_id: 'wg1', run_state: 'running', status: 'fail', enabled: true },
  ],
  incidents: [{ check_name: 'tunnel_wg1' }],
  reserveOnlyAlert: true,
}

describe('тревога запасного при проблеме с маршрутизацией', () => {
  it.each([
    ['failed', 'route_control_failed', 'danger', 'Правила маршрутизации не работают'],
    ['stale', 'routing_evidence_stale', 'warn', 'Проверка маршрутизации устарела'],
  ])('%s: заголовок учитывает проверку маршрута', (state, reason_code, tone, tag) => {
    const headline = routerHeadline({ ...withReserveIncident, evidence: { state, reason_code } })
    expect(headline).toMatchObject({ tone, tag })
  })

  it('не скрывает тревогу DNS за устаревшей проверкой маршрутизации', () => {
    const headline = routerHeadline({
      ...withReserveIncident,
      incidents: [...withReserveIncident.incidents, { check_name: 'dns' }],
      evidence: { state: 'stale', reason_code: 'routing_evidence_stale' },
    })
    expect(headline).toMatchObject({ tone: 'danger', check: 'dns' })
  })
})
