import { describe, it, expect } from 'vitest'
import { routingVerdict, policyRows, parseRouteSnapshot } from '../src/routes.js'

// Форма снимка -- wire.RouteSnapshot (pkg/wire/routing.go): tunnels[],
// counts{}, policies[], rules[], singbox_router, warnings[].
describe('routingVerdict', () => {
  it('sing-box перебивает всё: раскладка HR-Neo не действует', () => {
    const v = routingVerdict({ singbox_router: { enabled: true }, tunnels: [] })
    expect(v.mode).toBe('unknown')
    // Механизм должен быть назван -- иначе оператор не поймёт, почему
    // раскладка ниже не отвечает на его вопрос.
    expect(`${v.title} ${v.detail}`).toMatch(/sing-box/i)
  })

  it('туннель с default_route -- трафик через VPN', () => {
    const v = routingVerdict({ tunnels: [{ id: 'awg12', name: 'Amsterdam', default_route: true }] })
    expect(v.mode).toBe('vpn')
    expect(v.title).toMatch(/Amsterdam/)
  })

  it('без default_route -- напрямую', () => {
    expect(routingVerdict({ tunnels: [{ id: 'awg12', name: 'Amsterdam' }] }).mode).toBe('direct')
  })

  it('несколько туннелей с default_route -- честное "неизвестно"', () => {
    // Каждый может лишь ЗАЯВЛЯТЬ, что он главный; угадывать нельзя.
    const v = routingVerdict({
      tunnels: [
        { id: 'awg10', name: 'A', default_route: true },
        { id: 'awg11', name: 'B', default_route: true },
      ],
    })
    expect(v.mode).toBe('unknown')
  })

  it('пустой снимок -- неизвестно, а не "напрямую"', () => {
    expect(routingVerdict({}).mode).toBe('unknown')
    expect(routingVerdict(null).mode).toBe('unknown')
  })

  it('снимок с предупреждениями помечается неполным', () => {
    const v = routingVerdict({
      tunnels: [{ id: 'a', default_route: true }],
      warnings: ['/api/routing/tunnels failed'],
    })
    expect(v.partial).toBe(true)
  })
})

describe('policyRows', () => {
  it('разворачивает политику в цепочку интерфейсов по приоритету', () => {
    const rows = policyRows({
      policies: [
        {
          name: 'HydraRoute',
          dns: 32,
          hr_neo: 32,
          interfaces: [{ bind: 'OpkgTun11' }, { bind: 'Wireguard1' }],
        },
      ],
    })
    expect(rows).toHaveLength(1)
    expect(rows[0].chain.map((i) => i.label)).toEqual(['OpkgTun11', 'Wireguard1'])
    expect(rows[0].rules).toBe(32)
  })

  it('политика без интерфейсов даёт пустую цепочку', () => {
    expect(policyRows({ policies: [{ name: 'RU', dns: 3 }] })[0].chain).toEqual([])
  })

  it('снимок без политик даёт пустой список', () => {
    expect(policyRows({})).toEqual([])
    expect(policyRows(null)).toEqual([])
  })
})

const liveSnapshot = {
  policies: [
    {
      name: 'HydraRoute',
      dns: 26,
      hr_neo: 26,
      active_tunnel_id: 'awg11',
      via_vpn: true,
      interfaces: [
        { bind: 'OpkgTun11', name: 'awg3-work-via-ru1', role: 'active', available: true, tunnel_id: 'awg11', via_vpn: true },
        { bind: 'Wireguard0', name: 'NetherlandsKerkradeS24', role: 'unavailable', order: 1, tunnel_id: 'awg20' },
        { bind: 'OpkgTun10', name: 'awg3-main-work', role: 'unavailable', order: 2, tunnel_id: 'awg10' },
      ],
    },
    {
      name: 'RU',
      dns: 2,
      hr_neo: 2,
      interfaces: [{ bind: 'GigabitEthernet1', name: 'Подключение Ethernet', role: 'active', available: true }],
    },
  ],
}

describe('policyRows', () => {
  it('раскладывает цепочку по ролям, а не в одну строку', () => {
    const [hydra] = policyRows(liveSnapshot)
    expect(hydra.chain.map((i) => i.role)).toEqual(['active', 'unavailable', 'unavailable'])
    expect(hydra.chain[0].label).toBe('awg3-work-via-ru1')
    expect(hydra.rules).toBe(26)
  })

  it('помечает политику, которая выходит мимо VPN', () => {
    const [, ru] = policyRows(liveSnapshot)
    expect(ru.viaVPN).toBe(false)
    expect(ru.egress).toBe('мимо VPN')
  })

  it('не помечает политику, идущую через туннель', () => {
    const [hydra] = policyRows(liveSnapshot)
    expect(hydra.viaVPN).toBe(true)
    expect(hydra.egress).toBe('')
  })

  it('говорит прямо, когда в цепочке нет живого звена', () => {
    const [dead] = policyRows({
      policies: [{ name: 'Dead', dns: 1, interfaces: [{ bind: 'OpkgTun10', role: 'unavailable' }] }],
    })
    expect(dead.egress).toBe('нет доступного интерфейса')
  })

  it('снимок старого агента не размечается догадками', () => {
    const [old] = policyRows({
      policies: [{ name: 'HydraRoute', dns: 7, interfaces: [{ bind: 'nwg1', name: 'amst', role: 'active', available: true }] }],
    })
    expect(old.egress).toBe('')
  })
})

describe('parseRouteSnapshot', () => {
  it('разбирает JSON из вывода команды', () => {
    const snap = parseRouteSnapshot(JSON.stringify({ tunnels: [{ id: 'awg1' }] }))
    expect(snap.tunnels).toHaveLength(1)
  })

  it('на мусоре возвращает null, а не половину снимка', () => {
    expect(parseRouteSnapshot('не json')).toBe(null)
    expect(parseRouteSnapshot('')).toBe(null)
    expect(parseRouteSnapshot(null)).toBe(null)
  })
})
