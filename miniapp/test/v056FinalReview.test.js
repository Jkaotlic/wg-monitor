import { describe, it, expect, vi, afterEach } from 'vitest'
import { readFileSync } from 'node:fs'
import { fetchRouterChecksWithIncidents } from '../src/api.js'
import { tunnelCountSummary } from '../src/labels.js'
import { routingVerdict, withCheckVerdict } from '../src/routes.js'
import { tunnelList, tunnelsTabSummary } from '../src/tunnelDelete.js'
import { withSnapshotCarrier } from '../src/trafficPath.js'

// Сквозное ревью v0.56: вкладки «VPN-туннели» и «Маршруты» видят те же
// открытые тревоги, что «Роутер» и «Проверки» (I1); заголовок списка
// считает показанные строки (M2); несущий по снимку -- только у политики,
// которая что-то исполняет (M1).

afterEach(() => vi.unstubAllGlobals())

const snapshot = {
  tunnels: [
    { id: 'awg10', name: 'amsterdam', type: 'managed', status: 'running', enabled: true },
    { id: 'awg11', name: 'berlin', type: 'managed', status: 'running', enabled: true },
  ],
  counts: { awg10: { dns: 5, static: 0, hr_neo: 0 } },
  policies: [],
}
const events = {
  checks: [],
  tunnels: [
    { tunnel_id: 'awg10', name: 'amsterdam', status: 'ok', run_state: 'running', enabled: true, handshake_age_sec: 20 },
    { tunnel_id: 'awg11', name: 'berlin', status: 'ok', run_state: 'running', enabled: true, handshake_age_sec: 20 },
  ],
  traffic: { mode: 'split', carrier_basis: 'policy', carrier_tunnel_id: 'awg10', carrier_alive: true },
}
// Тревога снимается не сразу (2-3 удачных отчёта): проверка уже ok, а
// инцидент по VPN-туннелю ещё открыт.
const incidents = [{ check_name: 'tunnel_awg10', severity: 'HARD' }]

function stubApi() {
  const calls = []
  vi.stubGlobal('fetch', async (url) => {
    calls.push(url)
    const body = String(url).endsWith('/events') ? events : { router: { id: 7 }, incidents }
    return { ok: true, status: 200, json: async () => body }
  })
  return calls
}

describe('I1: вкладки видят открытые тревоги', () => {
  it('загрузка вкладок приносит тревоги из того же ответа, что у «Роутера», без команд роутеру', async () => {
    const calls = stubApi()
    const checks = await fetchRouterChecksWithIncidents(7)
    expect(checks.incidents).toEqual(incidents)
    expect(checks.tunnels).toEqual(events.tunnels)
    expect(calls.sort()).toEqual(['/v1/miniapp/routers/7', '/v1/miniapp/routers/7/events'])
  })

  it('«Роутер» и вкладка «VPN-туннели» дают один счёт, пока тревога открыта', async () => {
    stubApi()
    const checks = await fetchRouterChecksWithIncidents(7)
    // Так считает вкладка (TunnelsTab.jsx).
    const tab = tunnelsTabSummary(tunnelList(withCheckVerdict(snapshot, checks)), checks)
    // Так считает «Роутер» (RouterDetail.jsx).
    const main = tunnelCountSummary(events.tunnels, incidents)
    expect(main.working).toBe(1)
    expect(tab.counts).toEqual(main)
  })

  it('«Маршруты»: живой несущий с открытой тревогой -- не «обход идёт»', async () => {
    stubApi()
    const checks = await fetchRouterChecksWithIncidents(7)
    // Так вызывает экран (RoutesTab.jsx).
    const v = routingVerdict(withCheckVerdict(snapshot, checks), checks?.traffic, checks?.incidents)
    expect(v.title).toMatch(/«amsterdam» не отвечает/)
    // Без тревоги -- прежний ответ.
    const ok = routingVerdict(withCheckVerdict(snapshot, events), events.traffic, [])
    expect(ok.title).toBe('Обход идёт через «amsterdam»')
  })

  it('экраны вызывают функции именно так', () => {
    const tab = readFileSync(new URL('../src/screens/TunnelsTab.jsx', import.meta.url), 'utf8')
    const routes = readFileSync(new URL('../src/screens/RoutesTab.jsx', import.meta.url), 'utf8')
    expect(tab).toMatch(/fetchRouterChecksWithIncidents\(routerID\)/)
    expect(tab).toMatch(/tunnelsTabSummary\(list, checks\)/)
    expect(tab).not.toMatch(/tunnelListSummary\(/)
    expect(routes).toMatch(/fetchRouterChecksWithIncidents\(routerID\)/)
    expect(routes).toMatch(/routingVerdict\(shown, checks\?\.traffic, checks\?\.incidents\)/)
  })
})

describe('M2: заголовок «Все VPN-туннели · N» -- по показанным строкам', () => {
  const three = {
    ...snapshot,
    tunnels: [...snapshot.tunnels, { id: 'awg12', name: 'cologne', type: 'managed', status: 'running', enabled: true }],
  }

  it('строк три, проверок две: в заголовке три, подпись говорит, откуда счёт', () => {
    const list = tunnelList(withCheckVerdict(three, events))
    const s = tunnelsTabSummary(list, { ...events, incidents: [] })
    expect(list.length).toBe(3)
    expect(s.title).toBe('Все VPN-туннели · 3')
    expect(s.counts.total).toBe(2)
    expect(s.note).toMatch(/^\d+ работа\S* из 2 /)
    expect(s.note).toMatch(/в списке 3/)
  })

  it('числа совпадают -- подпись прежняя, без оговорки', () => {
    const list = tunnelList(withCheckVerdict(snapshot, events))
    const s = tunnelsTabSummary(list, { ...events, incidents: [] })
    expect(s.title).toBe('Все VPN-туннели · 2')
    expect(s.note).toBe('2 работают из 2 настроенных')
    expect(s.note).not.toMatch(/в списке/)
  })

  it('проверки не загрузились -- счёт по строкам, заголовок тот же', () => {
    const list = tunnelList(snapshot)
    const s = tunnelsTabSummary(list, null)
    expect(s.title).toBe('Все VPN-туннели · 2')
    expect(s.counts.total).toBe(2)
  })
})

describe('M1: несущий по снимку -- только у политики, которая что-то исполняет', () => {
  const traffic = { mode: 'split', carrier_basis: 'none', carrier_alive: false }
  const tunnels = [{ id: 'awg10' }, { id: 'awg11' }]

  it('у политики ноль правил -- несущего не называем', () => {
    const snap = { tunnels, policies: [{ active_tunnel_id: 'awg10', via_vpn: true, dns: 0, hr_neo: 0 }] }
    expect(withSnapshotCarrier(traffic, snap)).toBe(traffic)
  })

  it('все правила политики -- HydraRoute Neo, а он остановлен: несущего не называем', () => {
    const snap = {
      tunnels,
      hr_neo: { installed: true, running: false },
      policies: [{ active_tunnel_id: 'awg10', via_vpn: true, dns: 5, hr_neo: 5 }],
    }
    expect(withSnapshotCarrier(traffic, snap)).toBe(traffic)
  })

  it('HydraRoute Neo работает -- его правила считаются', () => {
    const snap = {
      tunnels,
      hr_neo: { installed: true, running: true },
      policies: [{ active_tunnel_id: 'awg10', via_vpn: true, dns: 5, hr_neo: 5 }],
    }
    expect(withSnapshotCarrier(traffic, snap).carrier_tunnel_id).toBe('awg10')
  })

  it('больше правил по числу -- но не исполняемых: несущий -- политика с исполняемыми', () => {
    const snap = {
      tunnels,
      hr_neo: { installed: true, running: false },
      policies: [
        { active_tunnel_id: 'awg10', via_vpn: true, dns: 10, hr_neo: 10 },
        { active_tunnel_id: 'awg11', via_vpn: true, dns: 3, hr_neo: 0 },
      ],
    }
    expect(withSnapshotCarrier(traffic, snap).carrier_tunnel_id).toBe('awg11')
  })
})

describe('Fix 2: строка VPN-туннеля с открытой тревогой', () => {
  it('подписана так же, как считается: не «работает»', () => {
    const rows = tunnelList(withCheckVerdict(snapshot, events), incidents)
    expect(rows.find((r) => r.id === 'awg10').stateLabel).toBe('не отвечает — тревога открыта')
    expect(rows.find((r) => r.id === 'awg11').stateLabel).toBe('работает')
    // Без тревог -- как раньше.
    expect(tunnelList(withCheckVerdict(snapshot, events)).find((r) => r.id === 'awg10').stateLabel).toBe('работает')
  })

  it('выключенный настройкой с открытой тревогой остаётся «выключен»', () => {
    const off = { ...snapshot, tunnels: snapshot.tunnels.map((t) => (t.id === 'awg10' ? { ...t, status: 'stopped', enabled: false } : t)) }
    expect(tunnelList(off, incidents).find((r) => r.id === 'awg10').stateLabel).not.toMatch(/тревога/)
  })

  it('экран передаёт тревоги в список', () => {
    const tab = readFileSync(new URL('../src/screens/TunnelsTab.jsx', import.meta.url), 'utf8')
    expect(tab).toMatch(/tunnelList\(shown, checks\?\.incidents\)/)
  })
})
