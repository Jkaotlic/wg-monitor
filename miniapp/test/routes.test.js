import { describe, it, expect } from 'vitest'
import {
  routingVerdict,
  policyRows,
  tunnelRows,
  parseRouteSnapshot,
  tunnelLive,
  defaultRouteBadge,
  tunnelRuleSummary,
  policyRuleSummary,
  rulesByBind,
  ruleBackendLabel,
  visibleTunnelRows,
} from '../src/routes.js'

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


// Три придирки к тексту таба «Маршруты», снятые с живого снимка домашнего
// роутера (26 правил через политику HydraRoute, три туннеля с
// default_route=true, из них работает один): одно и то же число не должно
// повторяться в строке трижды, движок HR Neo не должен зваться так же, как
// соседняя политика «HydraRoute», и зелёный бейдж «основной маршрут» не
// должен висеть на выключенном туннеле.
describe('tunnelLive', () => {
  // Словарь тот же, что у агента (routingStatusEnabled,
  // internal/agent/actions/route_targets.go:56). Вторая формула для той же
  // величины разошлась бы с первой.
  it('running/up/started/active -- туннель поднят', () => {
    for (const status of ['running', 'up', 'started', 'active', 'RUNNING']) {
      expect(tunnelLive({ status })).toBe('up')
    }
  })

  it('disabled -- туннель выключен', () => {
    expect(tunnelLive({ status: 'disabled' })).toBe('down')
    expect(tunnelLive({ status: 'stopped' })).toBe('down')
  })

  // enabled в снимке НЕ говорит о том, включён ли туннель: агент дожимает
  // его до true из каталога маршрутизации NDMS
  // (route_status.go:190, `Enabled || ep.Enabled`), чтобы выключенный
  // туннель оставался мишенью для переноса правил. Живой роутер отдаёт
  // awg10 как enabled:false/status:"disabled", а снимок -- enabled:true.
  // Судить о жизни туннеля можно только по status.
  it('не верит полю enabled -- судит по status', () => {
    expect(tunnelLive({ enabled: true, status: 'disabled' })).toBe('down')
  })

  it('пустой status -- неизвестно, а не "выключен"', () => {
    expect(tunnelLive({})).toBe('unknown')
    expect(tunnelLive({ status: '' })).toBe('unknown')
  })
})

describe('defaultRouteBadge', () => {
  it('туннель работает и несёт основной маршрут -- зелёный бейдж', () => {
    expect(defaultRouteBadge({ defaultRoute: true, live: 'up' })).toEqual({
      tone: 'ok',
      text: 'основной маршрут',
    })
  })

  // Главная придирка: выключенный туннель основной маршрут не несёт, чем бы
  // он ни был настроен, и зелёная пилюля на нём -- прямая неправда.
  it('выключенный туннель не получает зелёный бейдж', () => {
    const badge = defaultRouteBadge({ defaultRoute: true, live: 'down' })
    expect(badge.tone).toBe('muted')
    expect(badge.text).toBe('назначен основным, но выключен')
  })

  it('когда состояние неизвестно -- бейдж без утверждения о выключенности', () => {
    expect(defaultRouteBadge({ defaultRoute: true, live: 'unknown' })).toEqual({
      tone: 'muted',
      text: 'назначен основным',
    })
  })

  it('без default_route бейджа нет вовсе', () => {
    expect(defaultRouteBadge({ defaultRoute: false, live: 'up' })).toBe(null)
  })
})

describe('tunnelRuleSummary', () => {
  // Было: "26 правил ведут сюда · 26 из них через политику · 26 через
  // HydraRoute" -- одно число три раза, и ни одно повторение не добавляет
  // знания.
  it('не повторяет одно и то же число', () => {
    const line = tunnelRuleSummary({ total: 26, policyRules: 26, hrNeo: 26 })
    expect(line).toBe('26 правил — все через политику')
    expect(line.match(/26/g)).toHaveLength(1)
  })

  it('разделяет собственные правила и правила политики, когда числа разные', () => {
    expect(tunnelRuleSummary({ total: 31, policyRules: 26, hrNeo: 27 })).toBe(
      '31 правило, из них 26 через политику · 27 через HR Neo',
    )
  })

  it('без правил политики говорит просто про правила туннеля', () => {
    expect(tunnelRuleSummary({ total: 5, policyRules: 0, hrNeo: 0 })).toBe('5 правил ведут сюда')
    expect(tunnelRuleSummary({ total: 1, policyRules: 0, hrNeo: 0 })).toBe('1 правило ведёт сюда')
    expect(tunnelRuleSummary({ total: 2, policyRules: 0, hrNeo: 0 })).toBe('2 правила ведут сюда')
  })

  it('пустой туннель', () => {
    expect(tunnelRuleSummary({ total: 0, policyRules: 0, hrNeo: 0 })).toBe('правил на него нет')
  })

  // Движок зовётся так же, как одна из политик роутера, поэтому в тексте он
  // называется тем же именем, что и на вкладке роутера -- «HR Neo».
  it('движок называется HR Neo, а не HydraRoute', () => {
    const line = tunnelRuleSummary({ total: 10, policyRules: 0, hrNeo: 4 })
    expect(line).toContain('4 через HR Neo')
    expect(line).not.toContain('HydraRoute')
  })
})

describe('policyRuleSummary', () => {
  // Вторая придирка: под политикой RU стояло "2 правил (2 через
  // HydraRoute)" -- и число повторено, и движок назван именем соседней
  // политики, будто правила RU уходят в политику HydraRoute.
  it('политика, где все правила -- HR Neo, не поминает движок вовсе', () => {
    expect(policyRuleSummary({ rules: 2, hrNeo: 2 })).toBe('2 правила')
    expect(policyRuleSummary({ rules: 26, hrNeo: 26 })).toBe('26 правил')
  })

  it('называет движок, только когда он покрывает часть правил', () => {
    expect(policyRuleSummary({ rules: 10, hrNeo: 4 })).toBe('10 правил · 4 через HR Neo')
  })

  it('политика без правил', () => {
    expect(policyRuleSummary({ rules: 0, hrNeo: 0 })).toBe('правил нет')
  })
})

describe('routingVerdict и выключенные туннели', () => {
  // На домашнем роутере default_route=true стоит у всех трёх туннелей, но
  // работает один. Старый вердикт объявлял главный туннель неизвестным,
  // хотя выключенный туннель претендентом быть не может.
  it('выключенные претенденты не делают главный туннель неизвестным', () => {
    const v = routingVerdict({
      tunnels: [
        { id: 'awg10', name: 'main', default_route: true, status: 'disabled' },
        { id: 'awg11', name: 'work', default_route: true, status: 'running' },
        { id: 'awg20', name: 'nl', default_route: true, status: 'disabled' },
      ],
    })
    expect(v.mode).toBe('vpn')
    expect(v.title).toContain('work')
  })

  it('спорят только работающие туннели', () => {
    const v = routingVerdict({
      tunnels: [
        { id: 'awg10', name: 'main', default_route: true, status: 'running' },
        { id: 'awg11', name: 'work', default_route: true, status: 'running' },
        { id: 'awg20', name: 'nl', default_route: true, status: 'disabled' },
      ],
    })
    expect(v.mode).toBe('unknown')
    expect(v.detail).toContain('main')
    expect(v.detail).toContain('work')
    expect(v.detail).not.toContain('nl')
  })

  it('все претенденты выключены -- трафик идёт напрямую, и экран говорит почему', () => {
    const v = routingVerdict({
      tunnels: [{ id: 'awg10', name: 'main', default_route: true, status: 'disabled' }],
    })
    expect(v.mode).toBe('direct')
    expect(v.detail).toContain('main')
    expect(v.detail).toContain('выключен')
  })

  // Снимок агента, который status не присылал, судить о выключенности не
  // даёт -- и вердикт на нём остаётся прежним.
  it('снимок без status ведёт себя как раньше', () => {
    const v = routingVerdict({
      tunnels: [
        { id: 'awg10', name: 'main', default_route: true },
        { id: 'awg11', name: 'work', default_route: true },
      ],
    })
    expect(v.mode).toBe('unknown')
    expect(v.detail).toContain('main')
    expect(v.detail).toContain('work')
  })
})

describe('tunnelRows -- состояние туннеля', () => {
  it('строка несёт состояние туннеля, а не только его настройку', () => {
    const rows = tunnelRows({
      tunnels: [
        { id: 'awg10', name: 'main', default_route: true, enabled: true, status: 'disabled' },
        { id: 'awg11', name: 'work', default_route: true, enabled: true, status: 'running' },
      ],
      counts: {},
    })
    const byID = Object.fromEntries(rows.map((r) => [r.id, r]))
    expect(byID.awg10.live).toBe('down')
    expect(byID.awg11.live).toBe('up')
  })
})

describe('подписи в списке правил', () => {
  // Заголовок группы приезжает из снимка как "policy:HydraRoute" -- это
  // системный идентификатор, а не текст для человека, и на экране он до сих
  // пор стоял как есть.
  it('группа правил политики подписана словами, а не идентификатором', () => {
    const groups = rulesByBind({
      rules: [
        { id: '1', bind: 'policy:HydraRoute' },
        { id: '2', bind: 'opkgtun11' },
        { id: '3' },
      ],
    })
    const byBind = Object.fromEntries(groups.map((g) => [g.bind, g.label]))
    expect(byBind['policy:HydraRoute']).toBe('Политика «HydraRoute»')
    // Имя интерфейса -- уже имя, его выдумывать не надо.
    expect(byBind['opkgtun11']).toBe('opkgtun11')
    expect(byBind['без привязки']).toBe('без привязки')
  })

  it('движок правила называется HR Neo, а не hydraroute', () => {
    expect(ruleBackendLabel('hydraroute')).toBe('HR Neo')
    // Незнакомый движок эхом, а не проглочен -- то же правило честности,
    // что у checkLabel в labels.js.
    expect(ruleBackendLabel('ndms')).toBe('ndms')
    expect(ruleBackendLabel('')).toBe('')
  })
})

describe('visibleTunnelRows', () => {
  // В снимке живого роутера три своих туннеля и пять записей WAN/system из
  // каталога NDMS. Раздел зовётся "Туннели в маршрутизации", а показывал в
  // том числе "Wi-Fi клиент 2.4 ГГц" и "Подключение Ethernet" -- пустыми
  // строками "правил на него нет".
  it('оставляет свои туннели и прячет пустые WAN/system-записи', () => {
    const rows = visibleTunnelRows([
      { id: 'awg11', type: 'managed', total: 26 },
      { id: 'awg10', type: 'managed', total: 0 },
      { id: 'eth3', type: 'wan', total: 0 },
      { id: 'Wireguard4', type: 'system', total: 0 },
    ])
    expect(rows.map((r) => r.id)).toEqual(['awg11', 'awg10'])
  })

  // Скрыть строку, на которой висят правила, значило бы спрятать часть
  // раскладки -- этого не делаем даже ради опрятного списка.
  it('WAN-запись с правилами остаётся видимой', () => {
    const rows = visibleTunnelRows([{ id: 'eth3', type: 'wan', total: 3 }])
    expect(rows.map((r) => r.id)).toEqual(['eth3'])
  })

  it('пустой список переживает', () => {
    expect(visibleTunnelRows([])).toEqual([])
    expect(visibleTunnelRows(undefined)).toEqual([])
  })
})
