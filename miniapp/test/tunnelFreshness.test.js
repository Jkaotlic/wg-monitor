import { describe, expect, it } from 'vitest'
import { snapshotState, withCheckVerdict } from '../src/routes.js'
import { tunnelList } from '../src/tunnelDelete.js'

const NOW = Date.parse('2026-11-06T10:00:00Z')
const SNAPSHOT = {
  tunnels: [{ id: 'awg10', name: 'vpn-main', type: 'managed', status: 'broken', enabled: true }],
  policies: [],
}

describe('свежесть вердикта VPN-туннеля', () => {
  it('старая ok-проверка не перекрашивает свежий broken-снимок в running', () => {
    const shown = withCheckVerdict(
      SNAPSHOT,
      { tunnels: [{ tunnel_id: 'awg10', status: 'ok', run_state: 'running', ts: '2026-11-06T09:40:00Z' }] },
      { nowMs: NOW, enforceFreshness: true, requireTimestamp: true },
    )

    expect(shown.tunnels[0]).toMatchObject({ status: 'broken', verdict_stale: true })
    expect(shown.tunnels[0].check_ok).not.toBe(true)
    expect(tunnelList(shown)[0].stateLabel).toBe('не отвечает, проверка устарела')
  })

  it('свежая ok-проверка по-прежнему исправляет разошедшийся снимок', () => {
    const shown = withCheckVerdict(
      SNAPSHOT,
      { tunnels: [{ tunnel_id: 'awg10', status: 'ok', run_state: 'running', ts: '2026-11-06T09:58:00Z' }] },
      { nowMs: NOW, enforceFreshness: true, requireTimestamp: true },
    )

    expect(shown.tunnels[0]).toMatchObject({ status: 'running', check_ok: true })
  })

  it('проверка без времени считается непроверяемой, а не свежей', () => {
    const shown = withCheckVerdict(
      { ...SNAPSHOT, tunnels: [{ ...SNAPSHOT.tunnels[0], status: 'running' }] },
      { tunnels: [{ tunnel_id: 'awg10', status: 'fail', run_state: 'running' }] },
      { nowMs: NOW, enforceFreshness: true, requireTimestamp: true },
    )

    expect(shown.tunnels[0]).toMatchObject({ status: 'running', verdict_stale: true })
    expect(shown.tunnels[0].check_failed).not.toBe(true)
    expect(tunnelList(shown)[0].stateLabel).toBe('поднят, проверка устарела')
  })
})

describe('повторное чтение снимка', () => {
  const snapshot = { tunnels: [] }

  it('показывает обновление поверх предыдущего снимка', () => {
    expect(snapshotState({ snapshot, busy: true })).toBe('refreshing')
  })

  it('не скрывает ошибку повторного чтения предыдущим снимком', () => {
    expect(snapshotState({ snapshot, error: 'дедлайн вышел' })).toBe('stale-error')
  })

  it('не скрывает отказ роутера предыдущим снимком', () => {
    expect(snapshotState({ snapshot, result: { status: 'error' } })).toBe('stale-refused')
  })
})
