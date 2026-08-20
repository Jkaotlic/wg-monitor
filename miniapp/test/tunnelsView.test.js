import { describe, it, expect } from 'vitest'
import { tunnelsView } from '../src/tunnelsView.js'

// Форма снимка -- wire.RouteSnapshot. Данные сняты с домашнего роутера:
// политика HydraRoute ведёт в awg11, за ним два звена резерва, ещё
// несколько туннелей в цепочке не состоят вовсе.
const SNAP = {
  policy_model: true,
  tunnels: [
    { id: 'awg11', name: 'awg3-work-via-ru1', iface: 'opkgtun11', type: 'managed', status: 'running', handshake_age_sec: 18, has_handshake: true },
    { id: 'awg20', name: 'NetherlandsKerkradeS24', iface: 'nwg0', type: 'managed', status: 'running' },
    { id: 'awg10', name: 'awg3-main-work', iface: 'opkgtun10', type: 'managed', status: 'disabled' },
    { id: 'awg21', name: 'Запасной', iface: 'opkgtun21', type: 'managed', status: 'disabled' },
    { id: 'eth3', name: 'Подключение Ethernet', iface: 'eth3', type: 'wan' },
  ],
  policies: [
    {
      name: 'HydraRoute', dns: 26, hr_neo: 26, via_vpn: true, active_tunnel_id: 'awg11',
      interfaces: [
        { bind: 'OpkgTun11', name: 'awg3-work-via-ru1', role: 'active', tunnel_id: 'awg11', via_vpn: true },
        { bind: 'Wireguard0', name: 'NetherlandsKerkradeS24', role: 'fallback', tunnel_id: 'awg20', via_vpn: true },
        { bind: 'OpkgTun10', name: 'awg3-main-work', role: 'unavailable', tunnel_id: 'awg10', via_vpn: true },
      ],
    },
    { name: 'RU', dns: 2, interfaces: [{ bind: 'GigabitEthernet1', name: 'Ethernet', role: 'active' }] },
  ],
  counts: {},
}

describe('tunnelsView -- активная линия', () => {
  it('называет линию, которая несёт трафик, и сколько назначений на ней', () => {
    const v = tunnelsView(SNAP)
    expect(v.active.id).toBe('awg11')
    expect(v.active.name).toBe('awg3-work-via-ru1')
    expect(v.active.iface).toBe('opkgtun11')
    expect(v.active.rules).toBe(26)
    expect(v.active.handshakeAgeSec).toBe(18)
  })

  // Политика "мимо VPN" активной линией не считается: её звено -- провайдер,
  // а не туннель, и рисовать его карточкой живой линии значило бы соврать.
  it('политика мимо VPN активной линией не становится', () => {
    const v = tunnelsView({
      ...SNAP,
      policies: [{ name: 'RU', dns: 2, interfaces: [{ bind: 'GigabitEthernet1', role: 'active' }] }],
    })
    expect(v.active).toBe(null)
  })

  it('пустой снимок не падает', () => {
    expect(tunnelsView(null)).toEqual({ active: null, policyName: '', chain: [], unused: [] })
  })
})

describe('tunnelsView -- порядок подхвата', () => {
  it('раскладывает цепочку по ролям: работает, готов подхватить, выключен', () => {
    const v = tunnelsView(SNAP)
    expect(v.policyName).toBe('HydraRoute')
    expect(v.chain.map((c) => [c.tunnelID, c.role])).toEqual([
      ['awg11', 'active'],
      ['awg20', 'ready'],
      ['awg10', 'off'],
    ])
  })

  // "Готов подхватить" -- это про туннель, который поднят и ждёт. Выключенный
  // вручную подхватить не может, и обещать это цветом нельзя.
  it('готовым считается только поднятое звено', () => {
    const v = tunnelsView(SNAP)
    expect(v.chain[1].note).toBe('отвечает')
    expect(v.chain[2].note).toBe('выключен')
  })

  // Заголовок строки уже говорит "Работает сейчас". Значение справа обязано
  // добавлять факт, а не повторять его другими словами.
  it('активное звено не повторяет свой заголовок значением', () => {
    const v = tunnelsView(SNAP)
    expect(v.chain[0].note).toBe('')
    expect(v.chain[0].handshakeAgeSec).toBe(18)
  })
})

describe('tunnelsView -- не используются', () => {
  it('свои туннели вне цепочки собираются отдельно', () => {
    const v = tunnelsView(SNAP)
    expect(v.unused.map((t) => t.id)).toEqual(['awg21'])
  })

  // WAN и системные записи каталога NDMS туннелями не являются: показать их
  // в разделе "не используются" значило бы предложить их поднять.
  it('WAN-записи в список не попадают', () => {
    const v = tunnelsView(SNAP)
    expect(v.unused.some((t) => t.id === 'eth3')).toBe(false)
  })
})

// Включить и выключить туннель можно только через NDMS-интерфейс: агент
// делает это ndmc'ом. Раскладка обязана нести его имя, иначе экран нарисует
// кнопку, которая ответит отказом с сервера.
describe('tunnelsView: чем можно управлять', () => {
  const SNAP = {
    tunnels: [
      { id: 'awg11', name: 'work', iface: 'opkgtun11', type: 'managed', status: 'running', has_handshake: true, handshake_age_sec: 30 },
      { id: 'awg20', name: 'reserve', iface: 'nwg0', ndms_name: 'Wireguard0', type: 'managed', status: 'disabled' },
      { id: 'awg7', name: 'spare', iface: 'nwg1', ndms_name: 'Wireguard1', type: 'managed', status: 'disabled' },
    ],
    policies: [{
      name: 'HydraRoute',
      active_tunnel_id: 'awg11',
      dns: 26,
      interfaces: [
        { bind: 'OpkgTun11', name: 'work', role: 'active', tunnel_id: 'awg11' },
        { bind: 'Wireguard0', name: 'reserve', role: 'unavailable', tunnel_id: 'awg20' },
      ],
    }],
  }

  it('звено цепочки несёт имя NDMS-интерфейса и своё состояние', () => {
    const link = tunnelsView(SNAP).chain.find((c) => c.tunnelID === 'awg20')
    expect(link.ndmsName).toBe('Wireguard0')
    expect(link.live).toBe('down')
  })

  // Opkg-туннель в NDMS не заведён вовсе: имени нет, и кнопки под ним не
  // будет -- это не пробел, а отсутствие способа.
  it('у opkg-туннеля имени интерфейса нет', () => {
    expect(tunnelsView(SNAP).chain.find((c) => c.tunnelID === 'awg11').ndmsName).toBe('')
  })

  it('неиспользуемый туннель тоже несёт имя интерфейса', () => {
    const row = tunnelsView(SNAP).unused.find((t) => t.id === 'awg7')
    expect(row.ndmsName).toBe('Wireguard1')
  })
})
