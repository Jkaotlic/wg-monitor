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
