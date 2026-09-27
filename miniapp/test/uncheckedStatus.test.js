import { describe, it, expect } from 'vitest'
import { checkState, workingTunnelCount, uncheckedTunnelCount, workingTunnelNote } from '../src/labels.js'
import { checkRows } from '../src/diag.js'
import { withCheckVerdict } from '../src/routes.js'
import { tunnelList } from '../src/tunnelDelete.js'

// Бэкенд v0.46 отдаёт статус проверки «unknown»: агент не смог ничего
// проверить (например, не прочитал адреса DNS). Это не «работает» и не
// «не работает» -- «не проверено», серым, и ни в какой счёт не идёт.
// Формы -- miniappCheckStatus (check_name/status/ts) и miniappTunnel.
const TS = '2026-09-27T12:00:00Z'
const LIVE = { status: 'online', stale: false, reach: 'online', last_seen_age_sec: 20 }

describe('статус unknown -- «не проверено»', () => {
  it('checkState: серое «не проверено», в том числе у сторожа DNS и у молчащего', () => {
    expect(checkState({ check_name: 'dns', status: 'unknown', ts: TS })).toEqual({ label: 'не проверено', tone: 'muted' })
    expect(checkState({ check_name: 'resolver_guard', status: 'unknown', ts: TS })).toEqual({ label: 'не проверено', tone: 'muted' })
    expect(checkState({ check_name: 'dns', status: 'unknown', ts: TS }, { stale: true }).label).toBe('не проверено')
  })

  it('«Проверки»: строка «не проверено», не «нет» и не красная', () => {
    const rows = checkRows({
      checks: [
        { check_name: 'dns', status: 'unknown', ts: TS },
        { check_name: 'resolver_guard', status: 'unknown', ts: TS },
        { check_name: 'custom_probe', status: 'unknown', ts: TS },
      ],
      router: LIVE,
      clockOffsetMs: 0,
    })
    for (const key of ['dns', 'resolver_guard', 'custom_probe']) {
      const r = rows.find((x) => x.key === key)
      expect(r.answer, key).toBe('не проверено')
      expect(r.tone, key).toBe('muted')
      expect(r.consequence ?? '', key).toBe('')
    }
  })

  const TUNNELS = [
    { tunnel_id: 'awg10', status: 'ok', run_state: 'running', enabled: true, handshake_age_sec: 20 },
    { tunnel_id: 'awg11', status: 'unknown', run_state: 'running', enabled: true, handshake_age_sec: 20 },
  ]
  it('туннель «не проверено» не считается ни работающим, ни упавшим', () => {
    expect(workingTunnelCount(TUNNELS, [])).toBe(1)
    expect(uncheckedTunnelCount(TUNNELS)).toBe(1)
    expect(workingTunnelNote(1, 2, 1)).toBe('работает из 2 настроенных · 1 не проверено')
    const row = checkRows({ checks: [], tunnels: TUNNELS, router: LIVE }).find((r) => r.key === 'tunnels')
    expect(row.answer).toBe('не проверено')
    expect(row.tone).toBe('muted')
    expect(row.value).toBe('1 из 2 работает, 1 не проверено')
  })

  it('вкладка «VPN-туннели»: поднятый с непроверенной проверкой -- «не проверено»', () => {
    const snap = { tunnels: [{ id: 'awg11', name: 'vymysel-de', type: 'managed', status: 'running', enabled: true }], policies: [] }
    const s = withCheckVerdict(snap, { tunnels: [{ tunnel_id: 'awg11', status: 'unknown', run_state: 'running' }] })
    const label = tunnelList(s).find((r) => r.id === 'awg11').stateLabel
    expect(label).toBe('поднят, не проверено')
  })
})
