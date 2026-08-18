import { describe, it, expect } from 'vitest'
import { routingVerdict, policyRows, tunnelRows, parseRouteSnapshot } from '../src/routes.js'

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

  // Экран решает, рисовать ли "привязки нет" вместо списка звеньев, ровно по
  // chain.length -- отсутствие интерфейсов у политики должно оставаться
  // отличимым от одного недоступного интерфейса, иначе оба случая молча
  // схлопнутся в одну и ту же (неверную для одного из них) картинку.
  it('пустая цепочка отличима от цепочки с одним недоступным звеном', () => {
    const noInterfaces = policyRows({ policies: [{ name: 'RU', dns: 3 }] })[0]
    const oneUnavailable = policyRows({
      policies: [{ name: 'RU', dns: 3, interfaces: [{ bind: 'eth0', role: 'unavailable' }] }],
    })[0]
    expect(noInterfaces.chain).toHaveLength(0)
    expect(oneUnavailable.chain).toHaveLength(1)
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

  // policy_model обязателен и здесь: без него снимок мог собрать старый
  // агент, который роли расставляет по своей (другой) логике, и "нет
  // живого звена" стало бы догадкой. Бот гейтит эту пометку тем же флагом.
  it('говорит прямо, когда в цепочке нет живого звена', () => {
    const [dead] = policyRows({
      policy_model: true,
      policies: [{ name: 'Dead', dns: 1, interfaces: [{ bind: 'OpkgTun10', role: 'unavailable' }] }],
    })
    expect(dead.egress).toBe('нет доступного интерфейса')
  })

  // Старый агент присылает role на КАЖДОМ звене (старая ветка снимка
  // размечает роли по доступности туннелей), поэтому "нет активного звена"
  // на его данных -- такая же догадка, как и "мимо VPN". Прежняя версия
  // теста этого не показывала: её фикстура имела активное звено и проходила
  // бы даже с неверной проверкой.
  it('снимок старого агента не размечается догадками', () => {
    const withActive = policyRows({
      policies: [{ name: 'HydraRoute', dns: 7, interfaces: [{ bind: 'nwg1', name: 'amst', role: 'active', available: true }] }],
    })[0]
    expect(withActive.egress).toBe('')

    const noActive = policyRows({
      policies: [
        {
          name: 'HydraRoute',
          dns: 7,
          interfaces: [
            { bind: 'nwg1', name: 'amst', role: 'unavailable' },
            { bind: 'nwg0', name: 'fra', role: 'unavailable' },
          ],
        },
      ],
    })[0]
    // Бот на таких данных молчит (routePolicyEgressNote), мини-апп обязан
    // молчать так же -- иначе экраны расходятся на одном и том же снимке.
    expect(noActive.egress).toBe('')
  })

  // policy_model -- это факт, который агент знает о себе: он и решает,
  // читались ли политики. Вывести это из данных нельзя: у политики, чья
  // цепочка целиком уходит мимо VPN, нет ни одного tunnel_id, и роутер с
  // одной такой политикой прежняя эвристика принимала за старого агента,
  // теряя метку "мимо VPN".
  it('верит флагу policy_model, когда туннелей в политике нет вовсе', () => {
    const [ru] = policyRows({
      policy_model: true,
      policies: [
        {
          name: 'RU',
          dns: 2,
          interfaces: [
            { bind: 'GigabitEthernet1', name: 'Подключение Ethernet', role: 'active', available: true },
          ],
        },
      ],
    })
    expect(ru.egress).toBe('мимо VPN')
  })
})

// Правила, привязанные политикой, живут только в policies[] и в counts не
// попадают (иначе панель бота, складывающая оба источника, удвоила бы итог).
// Экран обязан свести их обратно -- к активному туннелю политики и ни к
// какому другому, ровно как routePolicyDNSByTunnelID в панели бота.
describe('tunnelRows', () => {
  it('приписывает правила политики её активному туннелю', () => {
    const rows = tunnelRows({
      policy_model: true,
      tunnels: [
        { id: 'awg11', name: 'work', iface: 'opkgtun11' },
        { id: 'awg10', name: 'main', iface: 'opkgtun10' },
      ],
      counts: {},
      policies: [
        {
          name: 'HydraRoute',
          dns: 26,
          hr_neo: 26,
          active_tunnel_id: 'awg11',
          via_vpn: true,
          interfaces: [
            { bind: 'OpkgTun11', role: 'active', available: true, tunnel_id: 'awg11', via_vpn: true },
            { bind: 'OpkgTun10', role: 'unavailable', tunnel_id: 'awg10' },
          ],
        },
      ],
    })
    const byID = Object.fromEntries(rows.map((r) => [r.id, r]))
    expect(byID.awg11.total).toBe(26)
    expect(byID.awg11.policyRules).toBe(26)
    // Резерв цепочки трафика не несёт: приписать ему те же 26 значило бы
    // посчитать их дважды -- ровно тот дефект, который убрал active_tunnel_id.
    expect(byID.awg10.total).toBe(0)
  })

  it('складывает собственные правила туннеля с правилами его политики', () => {
    const [row] = tunnelRows({
      policy_model: true,
      tunnels: [{ id: 'awg11', name: 'work', iface: 'opkgtun11' }],
      counts: { awg11: { dns: 3, static: 2, hr_neo: 1 } },
      policies: [{ name: 'HydraRoute', dns: 26, hr_neo: 26, active_tunnel_id: 'awg11', via_vpn: true }],
    })
    expect(row.total).toBe(31)
    expect(row.policyRules).toBe(26)
    expect(row.hrNeo).toBe(27)
  })

  it('политика мимо VPN не приписывается ни одному туннелю', () => {
    const rows = tunnelRows({
      policy_model: true,
      tunnels: [{ id: 'awg11', name: 'work', iface: 'opkgtun11' }],
      counts: {},
      policies: [
        {
          name: 'RU',
          dns: 2,
          interfaces: [{ bind: 'GigabitEthernet1', role: 'active', available: true }],
        },
      ],
    })
    expect(rows[0].total).toBe(0)
  })

  // На снимке старого агента active_tunnel_id нет. Бот в этом случае
  // сопоставляет звенья цепочки с туннелями по iface/имени -- мини-апп
  // повторяет ту же запасную раскладку, иначе на одних данных экраны
  // покажут разное.
  it('на снимке старого агента сопоставляет цепочку по iface и имени', () => {
    const rows = tunnelRows({
      tunnels: [
        { id: 'awg11', name: 'amst', iface: 'nwg1' },
        { id: 'awg12', name: 'fra', iface: 'nwg0' },
      ],
      counts: {},
      policies: [
        {
          name: 'HydraRoute',
          dns: 7,
          interfaces: [{ bind: 'nwg1' }, { bind: 'nwg0' }],
        },
      ],
    })
    const byID = Object.fromEntries(rows.map((r) => [r.id, r]))
    expect(byID.awg11.total).toBe(7)
    expect(byID.awg12.total).toBe(7)
  })

  it('без политик остаются только собственные счётчики туннеля', () => {
    const [row] = tunnelRows({
      tunnels: [{ id: 'awg11', name: 'work', iface: 'opkgtun11' }],
      counts: { awg11: { dns: 4, static: 1, hr_neo: 2 } },
    })
    expect(row.total).toBe(5)
    expect(row.policyRules).toBe(0)
    expect(row.hrNeo).toBe(2)
    expect(row.name).toBe('work')
  })

  it('пустой снимок даёт пустой список', () => {
    expect(tunnelRows(null)).toEqual([])
    expect(tunnelRows({})).toEqual([])
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
