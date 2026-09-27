import { describe, it, expect } from 'vitest'
import { withCheckVerdict, tunnelLive } from '../src/routes.js'
import { tunnelList } from '../src/tunnelDelete.js'

// MINI-06: проверки не загрузились -- поднятый интерфейс не называется
// «работает»: его удалённая сторона могла быть мертва (workrouter 18.09).
// Форма снимка -- route_status (id/status/enabled), вымышленные имена.
const snap = {
  tunnels: [
    { id: 'awg10', name: 'vymysel-nl', type: 'managed', status: 'running', enabled: true },
    { id: 'awg12', name: 'vymysel-off', type: 'managed', status: 'disabled', enabled: false },
  ],
  policies: [],
}

describe('MINI-06: сбой загрузки проверок', () => {
  it('поднятый туннель -- состояние неизвестно, а не «работает»', () => {
    const s = withCheckVerdict(snap, null, { failed: true })
    expect(tunnelLive(s.tunnels[0])).toBe('unknown')
    expect(tunnelList(s).find((r) => r.id === 'awg10').stateLabel).not.toBe('работает')
  })
  it('выключенный настройкой остаётся выключенным', () => {
    const s = withCheckVerdict(snap, null, { failed: true })
    expect(s.tunnels[1].status).toBe('disabled')
  })
  it('без сбоя и без проверок (ещё грузятся) -- снимок как есть', () => {
    expect(withCheckVerdict(snap, null)).toBe(snap)
  })
})

// review v0.46, п. 5: слова говорят, что неизвестна ПРОВЕРКА (сервер не
// ответил), а не что «роутер не сказал» -- роутер-то как раз сказал «running».
describe('review п.5: подписи при сбое загрузки проверок', () => {
  const POLICY_SNAP = {
    ...snap,
    tunnels: [...snap.tunnels, { id: 'awg11', name: 'vymysel-de', type: 'managed', status: 'running', enabled: true }],
    policies: [{
      name: 'HydraRoute', dns: 3, active_tunnel_id: 'awg10', via_vpn: true,
      interfaces: [
        { bind: 'OpkgTun10', name: 'vymysel-nl', role: 'active', tunnel_id: 'awg10', via_vpn: true },
        { bind: 'OpkgTun11', name: 'vymysel-de', role: 'fallback', tunnel_id: 'awg11', via_vpn: true },
      ],
    }],
    counts: {},
  }
  it('строка списка -- «проверка не пришла»', () => {
    const s = withCheckVerdict(snap, null, { failed: true })
    const label = tunnelList(s).find((r) => r.id === 'awg10').stateLabel
    expect(label).toContain('сервер не ответил')
    expect(label).not.toContain('роутер не сказал')
  })
  it('цепочка и активная карточка не говорят «работает» и «роутер не сказал»', async () => {
    const { tunnelsView } = await import('../src/tunnelsView.js')
    const v = tunnelsView(withCheckVerdict(POLICY_SNAP, null, { failed: true }))
    expect(v.active.checkUnknown).toBe(true)
    expect(v.chain[0].role).toBe('activeUnknown')
    expect(v.chain[1].note).toContain('сервер не ответил')
    expect(v.chain[1].note).not.toContain('роутер не сказал')
  })
})
