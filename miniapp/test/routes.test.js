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
    expect(rows[0].chain).toBe('OpkgTun11 → Wireguard1')
    expect(rows[0].rules).toBe(32)
  })

  it('политика без интерфейсов честно говорит, что привязки нет', () => {
    expect(policyRows({ policies: [{ name: 'RU', dns: 3 }] })[0].chain).toBe('привязки нет')
  })

  it('снимок без политик даёт пустой список', () => {
    expect(policyRows({})).toEqual([])
    expect(policyRows(null)).toEqual([])
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
