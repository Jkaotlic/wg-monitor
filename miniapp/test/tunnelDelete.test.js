import { describe, it, expect } from 'vitest'
import {
  mayManageTunnels,
  tunnelList,
  tunnelCard,
  deleteBlock,
  deleteSheetText,
  deleteRefusal,
  deleteErrorText,
  deleteOutcome,
  tunnelAbsence,
  refusalKey,
  TUNNEL_TEXTS,
} from '../src/tunnelDelete.js'
import { ApiError } from '../src/api.js'
import { AGENT_OLDER_THAN_APP } from '../src/labels.js'

// Форма снимка -- wire.RouteSnapshot (pkg/wire/routing.go).
const SNAP = {
  policy_model: true,
  default_egress: 'direct',
  tunnels: [
    { id: 'nwg1', name: 'amsterdam', iface: 'nwg1', type: 'managed', status: 'running' },
    { id: 'nwg2', name: 'spare', iface: 'nwg2', type: 'managed', status: 'disabled' },
    { id: 'nwg3', name: '', iface: 'nwg3', type: 'managed', status: 'running' },
    { id: 'ISP', name: 'Провайдер', iface: 'ISP', type: 'wan', status: 'up' },
  ],
  counts: { nwg1: { dns: 2, static: 1, hr_neo: 2 } },
  other: { dns: 0, static: 0, hr_neo: 0 },
  policies: [
    {
      name: 'VPN',
      dns: 5,
      hr_neo: 5,
      active_tunnel_id: 'nwg1',
      via_vpn: true,
      interfaces: [
        { bind: 'nwg1', name: 'amsterdam', role: 'active', tunnel_id: 'nwg1', via_vpn: true },
        { bind: 'nwg2', name: 'spare', role: 'fallback', tunnel_id: 'nwg2' },
      ],
    },
  ],
}

const RULES_8 = 'На этом VPN-туннеле 8 правил — сначала перенесите их на другой VPN-туннель в «Маршрутах», потом удаляйте.'
const DEFAULT_SPARE = '«spare» — главный выход роутера: через него идёт всё, что не названо правилами. Удалить его нельзя, пока главным выходом не назначен другой.'

describe('права', () => {
  it('удаляют и загружают конфиги владелец и админ', () => {
    expect(mayManageTunnels('admin')).toBe(true)
    expect(mayManageTunnels('owner')).toBe(true)
    expect(mayManageTunnels('operator')).toBe(false)
    expect(mayManageTunnels('')).toBe(false)
    expect(mayManageTunnels(undefined)).toBe(false)
  })
})

describe('список и карточка', () => {
  it('только свои VPN-туннели, безымянный -- по id', () => {
    expect(tunnelList(SNAP).map((r) => [r.id, r.name, r.live, r.total])).toEqual([
      ['nwg1', 'amsterdam', 'up', 8],
      ['nwg2', 'spare', 'down', 0],
      ['nwg3', 'nwg3', 'up', 0],
    ])
    expect(tunnelList(null)).toEqual([])
  })

  it('карточка: интерфейс и главный выход', () => {
    expect(tunnelCard(SNAP, 'nwg1')).toMatchObject({ id: 'nwg1', name: 'amsterdam', iface: 'nwg1', total: 8, policyRules: 5, egressKnown: true, isDefault: false })
    expect(tunnelCard({ ...SNAP, default_egress: 'nwg2' }, 'nwg2')).toMatchObject({ egressKnown: true, isDefault: true })
    expect(tunnelCard({ ...SNAP, default_egress: '' }, 'nwg2')).toMatchObject({ egressKnown: false, isDefault: false })
  })

  it('чужое и несуществующее -- null', () => {
    expect(tunnelCard(SNAP, 'ISP')).toBe(null)
    expect(tunnelCard(SNAP, 'nope')).toBe(null)
    expect(tunnelCard(SNAP, '')).toBe(null)
    expect(tunnelCard(null, 'nwg1')).toBe(null)
  })
})

describe('почему нельзя удалить', () => {
  it('правила -- первым: это можно исправить переносом', () => {
    expect(deleteBlock(tunnelCard(SNAP, 'nwg1'))).toEqual({ kind: 'rules', text: RULES_8 })
    expect(deleteBlock(tunnelCard({ ...SNAP, default_egress: 'nwg1' }, 'nwg1')).kind).toBe('rules')
  })

  it('главный выход', () => {
    expect(deleteBlock(tunnelCard({ ...SNAP, default_egress: 'nwg2' }, 'nwg2'))).toEqual({ kind: 'default', text: DEFAULT_SPARE })
  })

  it('пустой и не главный -- можно', () => {
    expect(deleteBlock(tunnelCard(SNAP, 'nwg2'))).toBe(null)
    expect(deleteBlock(null)).toBe(null)
  })
})

describe('лист и ответы сервера', () => {
  it('лист называет последствие', () => {
    expect(deleteSheetText(tunnelCard(SNAP, 'nwg2'))).toEqual({
      title: 'Удалить VPN-туннель «spare»?',
      body: 'VPN-туннель «spare» исчезнет с роутера вместе с конфигом. Это необратимо: вернуть его можно, только загрузив конфиг заново.',
      note: 'Перед удалением сервер ещё раз проверит по свежему снимку роутера, что правил на VPN-туннеле нет.',
    })
  })

  it('отказ сервера -- те же слова, что до нажатия', () => {
    const card = tunnelCard(SNAP, 'nwg2')
    // Контракт части 1: rules -- разбивка {total, dns, static, hr_neo, via_policy}.
    const rules = { total: 2, dns: 1, static: 1, hr_neo: 0, via_policy: 0 }
    expect(deleteRefusal(new ApiError(409, 'tunnel_has_rules', 'x', 'На VPN-туннеле 2 правила', '', { code: 'tunnel_has_rules', rules }), card)).toEqual({
      kind: 'rules',
      text: 'На этом VPN-туннеле 2 правила — сначала перенесите их на другой VPN-туннель в «Маршрутах», потом удаляйте.',
    })
    expect(deleteRefusal(new ApiError(409, 'tunnel_has_rules', 'x', '', '', { rules: 5 }), card).text).toContain('5 правил')
    expect(deleteRefusal(new ApiError(409, 'tunnel_has_rules', 'x'), card)).toEqual({
      kind: 'rules',
      text: 'На этом VPN-туннеле есть правила — сначала перенесите их на другой VPN-туннель в «Маршрутах», потом удаляйте.',
    })
    expect(deleteRefusal(new ApiError(409, 'tunnel_is_default', 'x'), card)).toEqual({ kind: 'default', text: DEFAULT_SPARE })
    expect(deleteRefusal(new ApiError(400, 'confirm_mismatch', 'x'), card)).toBe(null)
    expect(deleteRefusal(new Error('net'), card)).toBe(null)
  })

  it('прочие ошибки -- фраза сервера, своя по коду или пусто', () => {
    expect(deleteErrorText(new ApiError(400, 'confirm_mismatch', 'x', 'Имя не совпало с «spare»'))).toBe('Имя не совпало с «spare»')
    expect(deleteErrorText(new ApiError(400, 'confirm_mismatch', 'x'))).toBe('Имя VPN-туннеля набрано неверно — ничего не удалено.')
    expect(deleteErrorText(new ApiError(404, 'unknown', 'x'))).toBe('Роутер не знает этот VPN-туннель — обновите список VPN-туннелей.')
    expect(deleteErrorText(new ApiError(404, 'tunnel_not_found', 'x'))).toBe('Роутер не знает этот VPN-туннель — обновите список VPN-туннелей.')
    expect(deleteErrorText(new ApiError(409, 'tunnel_not_managed', 'x'))).toBe('Этот VPN-туннель заведён не через awg-manager — из приложения его не удалить.')
    expect(deleteErrorText(new ApiError(409, 'snapshot_partial', 'x'))).toBe('Роутер прислал неполный снимок маршрутов — проверить правила не вышло, ничего не удалено. Попробуйте через минуту.')
    expect(deleteErrorText(new Error('net'))).toBe('Сервер не ответил — ничего не удалено. Попробуйте ещё раз.')
    expect(deleteErrorText(new ApiError(500, 'unknown', 'x'))).toBe('')
    expect(TUNNEL_TEXTS.checkingTimeout).toBe('Роутер не отвечает — проверьте, что он на связи. Ничего не удалено.')
    expect(deleteErrorText(Object.assign(new Error('checking_timeout'), { code: 'checking_timeout' }))).toBe(TUNNEL_TEXTS.checkingTimeout)
  })

  it('итог команды', () => {
    expect(deleteOutcome({ status: 'ok', output: '' }, 'spare')).toEqual({ tone: 'ok', text: 'VPN-туннель «spare» удалён.', done: true })
    expect(deleteOutcome({ status: 'err', output: 'awg-manager: 500' }, 'spare')).toEqual({ tone: 'error', text: 'Роутер не удалил VPN-туннель: awg-manager: 500', done: false })
    expect(deleteOutcome({ status: 'err', output: 'unknown action: tunnel_delete' }, 'spare').text).toBe(AGENT_OLDER_THAN_APP)
    expect(deleteOutcome(null, 'spare')).toEqual({
      tone: 'warn',
      text: 'Команда ушла на роутер, но он пока не ответил. Обновите список VPN-туннелей через минуту.',
      done: false,
    })
  })

  it('словарь: VPN-туннель полной формой', () => {
    for (const t of Object.values(TUNNEL_TEXTS)) expect(t).not.toMatch(/(^|[^-])туннел/i)
  })
})

// Ревью цикла 4: правки после серверного ревью и ревью фронтенда.
describe('ревью: сверка с сервером', () => {
  it('type и имя -- как на сервере: без учёта регистра и с обрезкой', () => {
    const snap = { tunnels: [{ id: 'nwg7', name: '  spare  ', type: ' Managed ' }, { id: 'nwg8', name: '   ', type: 'MANAGED' }] }
    expect(tunnelList(snap).map((r) => [r.id, r.name])).toEqual([
      ['nwg7', 'spare'],
      ['nwg8', 'nwg8'],
    ])
    expect(tunnelCard(snap, 'nwg7')).toMatchObject({ name: 'spare' })
  })

  it('пустой default_egress -- кнопку не прячем, решает сервер', () => {
    expect(deleteBlock(tunnelCard({ ...SNAP, default_egress: '' }, 'nwg2'))).toBe(null)
  })

  it('упавший VPN-туннель в цепочке общего набора -- фраза сервера и переход к переносу', () => {
    const card = tunnelCard(SNAP, 'nwg2')
    expect(deleteRefusal(new ApiError(409, 'tunnel_in_policy_chain', 'x', 'VPN-туннель стоит в цепочке «VPN» перед работающим.', '', { rules: 5 }), card)).toEqual({
      kind: 'chain',
      text: 'VPN-туннель стоит в цепочке «VPN» перед работающим.',
    })
    expect(deleteRefusal(new ApiError(409, 'tunnel_in_policy_chain', 'x'), card)).toEqual({ kind: 'chain', text: TUNNEL_TEXTS.inChain })
  })

  it('не свой VPN-туннель и пропавший -- разные слова', () => {
    expect(tunnelAbsence(SNAP, 'ISP')).toBe('foreign')
    expect(tunnelAbsence({ tunnels: [{ id: 'Wireguard0', type: 'ndms' }] }, 'Wireguard0')).toBe('foreign')
    expect(tunnelAbsence(SNAP, 'nope')).toBe('gone')
    expect(tunnelAbsence(null, 'nwg1')).toBe('gone')
    expect(TUNNEL_TEXTS.notManaged).toBe('Этот VPN-туннель создан не через awg-manager — удалите его в панели роутера.')
  })

  it('отказ устаревает, когда в снимке поменялись правила, главный выход или цепочка', () => {
    const key = refusalKey(tunnelCard(SNAP, 'nwg2'), SNAP)
    expect(refusalKey(tunnelCard(SNAP, 'nwg2'), structuredClone(SNAP))).toBe(key)
    expect(refusalKey(tunnelCard({ ...SNAP, counts: { nwg2: { dns: 1 } } }, 'nwg2'), SNAP)).not.toBe(key)
    const egress = { ...SNAP, default_egress: 'nwg2' }
    expect(refusalKey(tunnelCard(egress, 'nwg2'), egress)).not.toBe(key)
    const chain = structuredClone(SNAP)
    chain.policies[0].interfaces[1].role = 'unavailable'
    expect(refusalKey(tunnelCard(chain, 'nwg2'), chain)).not.toBe(key)
    expect(refusalKey(null, SNAP)).toBe('')
  })
})

// Приёмка цикла 4: состояние словами -- «выключен» только когда выключили.
describe('приёмка: состояние VPN-туннеля', () => {
  it('включён, но не поднялся -- «не отвечает», а не «выключен»', () => {
    const snap = {
      tunnels: [
        { id: 'a', name: 'up', type: 'managed', status: 'running', enabled: true },
        { id: 'b', name: 'off', type: 'managed', status: 'disabled', enabled: false },
        { id: 'c', name: 'fail', type: 'managed', status: 'down', enabled: true },
        { id: 'd', name: 'odd', type: 'managed', status: '' },
      ],
    }
    expect(tunnelList(snap).map((r) => r.stateLabel)).toEqual(['работает', 'выключен', 'не отвечает', 'состояние неизвестно'])
  })
})
