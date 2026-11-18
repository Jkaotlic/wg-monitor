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

describe('непроверенная связь на всех экранах', () => {
  const traffic = { mode: 'split', carrier_basis: 'policy', carrier_tunnel_id: 'awg11', reserve_tunnel_ids: [] }
  const line = { tunnel_id: 'awg11', run_state: 'running', enabled: true, status: 'unknown', handshake_age_sec: 16 }
  const snap = {
    tunnels: [{ id: 'awg11', type: 'managed', status: 'running' }],
    policies: [{ name: 'VPN', active_tunnel_id: 'awg11', interfaces: [{ tunnel_id: 'awg11' }] }],
  }
  it('схема, шапка, цепочка и маршрут не объявляют непроверенную связь рабочей или сломанной', async () => {
    const { pathState } = await import('../src/trafficPath.js')
    const { routerHeadline } = await import('../src/routerHeadline.js')
    const { tunnelsView } = await import('../src/tunnelsView.js')
    const { routingVerdict } = await import('../src/routes.js')
    const { tunnelStateLabel } = await import('../src/labels.js')
    const args = { traffic, tunnels: [line], router: LIVE }
    const shown = withCheckVerdict(snap, { tunnels: [line] })
    expect(pathState(args).tunnel).toBe('unknown')
    expect(routerHeadline(args).tag).toMatch(/не проверен/)
    expect(tunnelsView(shown, traffic).chain[0].role).toBe('activeUnknown')
    expect(routingVerdict(shown, traffic).mode).toBe('unknown')
    expect(tunnelStateLabel(line)).toContain('не проверено')
  })

  it('отсутствие проверки в полном ответе не подтверждает работу поднятого интерфейса', () => {
    const shown = withCheckVerdict(snap, { tunnels: [] }, { requireTimestamp: true })
    expect(tunnelList(shown)[0].stateLabel).toBe('поднят, не проверено')
  })
})

it('старый снимок без status: свежий fail виден, без проверки обе ветки маршрутов неизвестны', async () => {
  const { routingVerdict } = await import('../src/routes.js')
  const snapshot = { tunnels: [{ id: 'awg11', type: 'managed', default_route: true }] }
  const traffic = { mode: 'vpn', carrier_basis: 'policy', carrier_tunnel_id: 'awg11' }
  const failed = withCheckVerdict(snapshot, { tunnels: [{ tunnel_id: 'awg11', status: 'fail' }] })
  expect(tunnelList(failed)[0].stateLabel).toBe('не отвечает')
  for (const tr of [traffic, undefined]) expect(routingVerdict(snapshot, tr).mode).toBe('unknown')
})

it('одного работающего интерфейса или одной ok-проверки недостаточно для вывода о канале', async () => {
  const { pathState } = await import('../src/trafficPath.js')
  const { routerHeadline } = await import('../src/routerHeadline.js')
  const traffic = { mode: 'vpn', carrier_basis: 'policy', carrier_tunnel_id: 'wg0' }
  for (const info of [{ status: 'ok' }, { run_state: 'running' }]) {
    const args = { router: LIVE, traffic, tunnels: [{ tunnel_id: 'wg0', ...info }] }
    expect(pathState(args).tunnel).toBe('unknown')
    expect(routerHeadline(args).tag).toMatch(/не проверен/)
  }
})
