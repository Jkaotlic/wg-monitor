import { describe, it, expect } from 'vitest'
import { autorepairBadge, autorepairRow, enableSheetText, enableFields, enableReady, backupFor, enableBody, sourceLabel } from '../src/autorepair.js'

const SOURCES = [
  { provider: 'amnezia', label: 'Amnezia Premium', ok: true, options: [{ id: 'nl', label: 'Нидерланды' }] },
  { provider: 'hidemyname', label: 'HideMy.name', ok: false, note: 'кабинет не подключён', options: [{ id: 'de', label: 'Germany' }] },
  { provider: 'awg3', label: 'Свой сервер', ok: true, options: [{ id: 'main/awg1', label: '«Main» · «Frankfurt»' }] },
]
const RESP = (over = {}) => ({ enabled: false, provider: '', option: '', allow_relocate: false, sources: SOURCES, can_edit: true, ...over })

// Латиница вне «ёлочек» и вне «VPN» -- тот же смысл, что у сторожа бэкенда.
function latinOutside(s) {
  return String(s).replace(/«[^«»]*»/g, '').replace(/VPN/g, '').match(/[A-Za-z]+/g) ?? []
}

function allSheetStrings(resp, name, backup, carrier, reserve, self) {
  const t = enableSheetText(resp, name, backup, carrier, reserve, self)
  const out = [t.title, t.note, ...t.sections.flatMap((s) => [s.h, s.text])]
  for (const f of enableFields(resp)) {
    out.push(f.label)
    if (typeof f.hint === 'function') {
      for (const source of ['', 'amnezia|nl', 'hidemyname|de', 'awg3|main/awg1']) out.push(f.hint({ source, allow_relocate: false }))
    }
    for (const o of f.options ?? []) out.push(o.label)
  }
  return out
}

describe('autorepairBadge', () => {
  it('четыре состояния', () => {
    expect(autorepairBadge('on')).toEqual({ tone: 'ok', text: 'Автопочинка' })
    expect(autorepairBadge('limited')).toEqual({ tone: 'neutral', text: 'Автопочинка: только перезапуск' })
    expect(autorepairBadge('blocked')).toEqual({ tone: 'warn', text: 'Автопочинка стоит — нужен человек' })
    expect(autorepairBadge('off')).toBeNull()
    expect(autorepairBadge(undefined)).toBeNull()
  })
  it('стоит из-за переименования -- просит подтвердить', () => {
    expect(autorepairBadge('blocked', 'VPN-туннель переименован — подтвердите автопочинку на его экране')).toEqual({ tone: 'warn', text: 'Автопочинка ждёт подтверждения' })
  })
})

describe('autorepairRow', () => {
  it('выключена', () => expect(autorepairRow(RESP()).value).toBe('выключена'))
  it('включена: выключают кнопкой, а не переключателем', () => {
    const h = autorepairRow(RESP({ enabled: true, provider: 'amnezia', option: 'nl' })).hint
    expect(h).toBe('Выключить можно кнопкой «Выключить автопочинку» ниже, в любой момент.')
  })
  it('переименован -- ждёт подтверждения, называет прежнее имя', () => {
    const r = autorepairRow(RESP({ enabled: true, provider: 'amnezia', option: 'nl', rename_pending: 'Старый' }))
    expect(r.value).toBe('ждёт подтверждения')
    expect(r.hint).toBe('VPN-туннель переименован (был «Старый»): пока вы не подтвердите автопочинку, я только перезапускаю его, конфиг не выпускаю.')
  })
  it('включена с источником -- подпись кабинета в ёлочках', () => {
    const r = autorepairRow(RESP({ enabled: true, provider: 'amnezia', option: 'nl' }))
    expect(r.title).toBe('Автопочинка')
    expect(r.value).toBe('включена · из «Amnezia Premium»')
  })
  it('свой сервер -- подпись варианта уже в ёлочках', () => {
    expect(autorepairRow(RESP({ enabled: true, provider: 'awg3', option: 'main/awg1' })).value).toBe('включена · из «Main» · «Frankfurt»')
  })
  it('без источника -- только перезапуск', () => {
    expect(autorepairRow(RESP({ enabled: true })).value).toBe('только перезапуск')
  })
  it('стоит -- с причиной', () => {
    expect(autorepairRow(RESP({ enabled: true, provider: 'amnezia', option: 'nl', blocked: 'лимит попыток' })).value).toBe('стоит: лимит попыток')
  })
  it('источник пропал из списка -- имя кабинета по провайдеру', () => {
    expect(autorepairRow(RESP({ enabled: true, provider: 'hidemyname', option: 'zz', sources: [] })).value).toBe('включена · из «HideMy.name»')
  })
})

describe('backupFor', () => {
  const snap = {
    tunnels: [{ id: 'a', name: 'main' }, { id: 'b', name: 'reserve' }, { id: 'c', name: 'other' }],
    policies: [
      { name: 'P', active_tunnel_id: 'a', interfaces: [{ tunnel_id: 'a', role: 'active' }, { tunnel_id: 'b', role: 'fallback' }] },
      { name: 'Q', interfaces: [{ tunnel_id: 'c', role: 'active' }] },
    ],
  }
  it('первый доступный другой туннель того же набора', () => {
    expect(backupFor(snap, 'a')).toEqual({ known: true, name: 'reserve', carrier: '', reserve: false, self: false })
  })
  it('недоступное звено не резерв', () => {
    const s = { ...snap, policies: [{ interfaces: [{ tunnel_id: 'a', role: 'active' }, { tunnel_id: 'b', role: 'unavailable' }] }] }
    expect(backupFor(s, 'a')).toEqual({ known: true, name: '', carrier: '', reserve: false, self: false })
  })
  it('звено в роли down не резерв', () => {
    const s = { ...snap, policies: [{ interfaces: [{ tunnel_id: 'a', role: 'active' }, { tunnel_id: 'b', role: 'down' }] }] }
    expect(backupFor(s, 'a').name).toBe('')
  })
  it('не первое звено цепочки -- резерв сам: трафик и так идёт через активное', () => {
    const s = { ...snap, policies: [{ interfaces: [{ tunnel_id: 'b', role: 'active', available: true }, { tunnel_id: 'a', role: 'unavailable' }, { tunnel_id: 'c', role: 'fallback', available: true }] }] }
    expect(backupFor(s, 'a')).toEqual({ known: true, name: '', carrier: 'reserve', reserve: true, self: false })
  })
  it('резерв, а первое звено лежит -- через него трафик не идёт: следующее живое или никто', () => {
    const down = { tunnel_id: 'b', role: 'unavailable', available: false }
    const s1 = { ...snap, policies: [{ interfaces: [down, { tunnel_id: 'a', role: 'unavailable' }, { tunnel_id: 'c', role: 'fallback', available: true }] }] }
    expect(backupFor(s1, 'a')).toEqual({ known: true, name: '', carrier: 'other', reserve: true, self: false })
    const s2 = { ...snap, policies: [{ interfaces: [down, { tunnel_id: 'a', role: 'unavailable' }] }] }
    expect(backupFor(s2, 'a')).toEqual({ known: true, name: '', carrier: '', reserve: true, self: false })
  })
  it('available: false не резерв, available: true резерв', () => {
    const s = (av) => ({ ...snap, policies: [{ interfaces: [{ tunnel_id: 'a', role: 'active' }, { tunnel_id: 'b', available: av }] }] })
    expect(backupFor(s(false), 'a').name).toBe('')
    expect(backupFor(s(true), 'a').name).toBe('reserve')
  })
  it('набор без этого туннеля не считается', () => {
    expect(backupFor(snap, 'c')).toEqual({ known: true, name: '', carrier: '', reserve: false, self: false })
  })
  it('звено с проваленной проверкой (status dead) -- не живое, как на главном экране', () => {
    const tun = [{ id: 'a', name: 'vpn-de' }, { id: 'b', name: 'vpn-nl', status: 'dead' }, { id: 'c', name: 'other' }]
    // a -- первое звено, его резерв b «поднят», но проверка говорит «не отвечает».
    const s1 = { tunnels: tun, policies: [{ interfaces: [{ tunnel_id: 'a', role: 'active' }, { tunnel_id: 'b', role: 'fallback', available: true }] }] }
    expect(backupFor(s1, 'a').name).toBe('')
    // c -- резерв; первое звено b «доступно» по набору, но мертво по проверке.
    const s2 = { tunnels: tun, policies: [{ interfaces: [{ tunnel_id: 'b', role: 'active', available: true }, { tunnel_id: 'c', role: 'fallback' }] }] }
    expect(backupFor(s2, 'c')).toEqual({ known: true, name: '', carrier: '', reserve: true, self: false })
  })
  it('резерв, через который трафик идёт сейчас сам (первое звено не отвечает)', () => {
    const tun = [{ id: 'a', name: 'vpn-nl', status: 'dead' }, { id: 'b', name: 'vpn-de' }]
    const s = { tunnels: tun, policies: [{ active_tunnel_id: 'b', interfaces: [{ tunnel_id: 'a', role: 'fallback', available: true }, { tunnel_id: 'b', role: 'active', available: true }] }] }
    expect(backupFor(s, 'b')).toEqual({ known: true, name: '', carrier: '', reserve: true, self: true })
  })
  it('нет снимка -- не знаем', () => {
    expect(backupFor(null, 'a')).toEqual({ known: false, name: '', carrier: '', reserve: false, self: false })
    expect(backupFor({ tunnels: [] }, 'a')).toEqual({ known: false, name: '', carrier: '', reserve: false, self: false })
  })
})

describe('enableSheetText', () => {
  const sec = (t, h) => t.sections.find((s) => s.h === h)?.text
  it('заголовок и четыре раздела', () => {
    const t = enableSheetText(RESP(), 'vpn-nl', 'reserve')
    expect(t.title).toBe('Включить автопочинку «vpn-nl»?')
    expect(t.sections.map((s) => s.h)).toEqual(['Что будет делать', 'Чего стоит', 'Кому напишу', 'Как выключить'])
    expect(sec(t, 'Кому напишу')).toBe('Владельцу, операторам и админу — в личку бота: что упало, что делаю и чем кончилось. Если понадобится ваше участие, напишу отдельно, со звуком.')
    expect(sec(t, 'Как выключить')).toBe('Кнопкой «Выключить автопочинку» на этом экране, в любой момент.')
  })
  it('с резервом', () => {
    const r = RESP({ suggested: { provider: 'amnezia', option: 'nl', why: 'так он был выпущен' } })
    expect(sec(enableSheetText(r, 'vpn-nl', 'reserve'), 'Что будет делать')).toBe(
      'Если VPN-туннель «vpn-nl» упадёт, я уведу трафик на запасной VPN-туннель «reserve», перезапущу «vpn-nl», а если не поможет и ниже выбран источник — выпущу конфиг заново из него и заменю конфиг на роутере.',
    )
  })
  it('источник можно сменить в поле -- текст его не называет', () => {
    const r = RESP({ suggested: { provider: 'amnezia', option: 'nl', why: 'x' } })
    const w = sec(enableSheetText(r, 'vpn-nl', 'reserve'), 'Что будет делать')
    expect(w).not.toContain('Amnezia')
    expect(w).not.toContain('Свой сервер')
    expect(w).toContain('ниже выбран источник')
  })
  it('VPN-туннель -- резерв: трафик не уводится, обещания увести нет', () => {
    const w = sec(enableSheetText(RESP(), 'vpn-nl', '', 'main'), 'Что будет делать')
    expect(w).not.toContain('уведу')
    expect(w).not.toContain('Запасного VPN-туннеля нет')
    expect(w).toContain('трафик и так идёт через «main»')
    expect(w).toContain('порядок VPN-туннелей я не меняю')
    expect(w).not.toContain('его я не трогаю')
  })
  it('VPN-туннель -- резерв, но трафик сейчас идёт через него самого', () => {
    const w = sec(enableSheetText(RESP(), 'vpn-de', '', '', true, true), 'Что будет делать')
    expect(w).toContain('сейчас трафик идёт через него самого')
    expect(w).not.toContain('нет рабочего')
    expect(w).toContain('порядок VPN-туннелей я не меняю')
  })
  it('VPN-туннель -- резерв, а живого звена нет: лежащее не называется', () => {
    const w = sec(enableSheetText(RESP(), 'vpn-nl', '', '', true), 'Что будет делать')
    expect(w).not.toContain('уведу')
    expect(w).not.toContain('и так идёт')
    expect(w).toContain('у трафика сейчас нет рабочего VPN-туннеля')
    expect(w).toContain('порядок VPN-туннелей я не меняю')
  })
  it('без резерва', () => {
    const w = sec(enableSheetText(RESP(), 'vpn-nl', ''), 'Что будет делать')
    expect(w).toContain('Запасного VPN-туннеля нет: пока чиню, заблокированное открываться не будет')
    expect(w).not.toContain('уведу трафик')
  })
  it('резерв неизвестен -- без фразы о резерве', () => {
    const w = sec(enableSheetText(RESP(), 'vpn-nl', null), 'Что будет делать')
    expect(w).not.toContain('запасн')
    expect(w).toContain('я перезапущу его')
  })
  it('«Чего стоит» не зависит от поля: цена -- под полем источника', () => {
    const cost = sec(enableSheetText(RESP(), 'x', ''), 'Чего стоит')
    expect(cost).toBe('Перезапуск ничего не тратит. Цена выпуска зависит от источника — она написана под полем «Откуда выпускать конфиг заново».')
  })
  it('нет рабочих источников -- перевыпуска в тексте нет; о урезанном режиме говорит одна подсказка поля', () => {
    const t = enableSheetText(RESP({ sources: [SOURCES[1]] }), 'x', 'r')
    expect(sec(t, 'Что будет делать')).not.toContain('выпущу')
    expect(t.note).toBe('')
    expect(enableFields(RESP({ sources: [SOURCES[1]] }))[0].hint({ source: '' })).toBe('Источник не выбран: смогу только перезапускать. Чтобы пересоздавать конфиг, выберите кабинет.')
  })
  it('переименован -- лист подтверждения', () => {
    expect(enableSheetText(RESP({ enabled: true, rename_pending: 'Старый' }), 'vpn-nl', '').title).toBe('Подтвердить автопочинку «vpn-nl»?')
  })
})

describe('enableFields / enableReady', () => {
  it('в списке только источники с ok, плюс «без источника»', () => {
    const [sel] = enableFields(RESP())
    expect(sel.type).toBe('select')
    const vals = sel.options.map((o) => o.value)
    expect(vals).toEqual(['', 'amnezia|nl', 'awg3|main/awg1'])
    expect(sel.options.map((o) => o.label).join('|')).not.toContain('Germany')
  })
  it('кабинет без выпущенных стран (ok:false с объяснением) в выборе не участвует', () => {
    const resp = RESP({ sources: [{ provider: 'amnezia', label: 'Amnezia Premium', ok: false, note: 'в кабинете нет выпущенных стран — выпустите конфиг во вкладке «Управление»', options: [] }, SOURCES[2]] })
    expect(enableFields(resp)[0].options.map((o) => o.value)).toEqual(['', 'awg3|main/awg1'])
    expect(enableFields(resp)[0].hint({ source: 'awg3|main/awg1' })).not.toContain('Amnezia')
  })
  it('подсказка поля -- цена выбранного источника, и только его', () => {
    const all = RESP({ sources: SOURCES.map((s) => ({ ...s, ok: true })) })
    const [sel] = enableFields(all)
    expect(sel.hint({ source: '' })).toContain('Источник не выбран')
    const am = sel.hint({ source: 'amnezia|nl' })
    expect(am).toContain('«Amnezia Premium»: повторный выпуск того же конфига места в подписке не тратит.')
    expect(am).toContain('Смена страны может один раз занять ещё одно место в подписке')
    expect(am).toContain('Уже выпущенные страны не беру')
    expect(am).toContain('меняет страну, через которую видны сайты')
    expect(am).not.toContain('HideMy')
    expect(am).not.toContain('на сервере')
    const hm = sel.hint({ source: 'hidemyname|de' })
    expect(hm).toBe('«HideMy.name»: повторный выпуск и смена сервера места не тратят — код открывает все серверы. Смена локации меняет страну, через которую видны сайты.')
    const own = sel.hint({ source: 'awg3|main/awg1' })
    expect(own).toBe('Если старое подключение на сервере не оживёт, заведу новое — оно займёт ещё одно место на сервере.')
  })
  it('смена страны уже израсходована -- цена так и говорит', () => {
    const [sel] = enableFields(RESP({ relocate_spent: 'nl' }))
    const am = sel.hint({ source: 'amnezia|nl' })
    expect(am).toContain('Смену страны автопочинка этого VPN-туннеля уже использовала («Нидерланды»)')
    expect(am).not.toContain('может один раз занять')
  })
  it('начальное значение -- подсказанный источник или прежний выбор', () => {
    expect(enableFields(RESP({ suggested: { provider: 'amnezia', option: 'nl', why: 'x' } }))[0].initial).toBe('amnezia|nl')
    expect(enableFields(RESP({ provider: 'awg3', option: 'main/awg1' }))[0].initial).toBe('awg3|main/awg1')
    expect(enableFields(RESP({ suggested: { provider: 'hidemyname', option: 'de', why: 'x' } }))[0].initial).toBe('')
    expect(enableFields(RESP())[0].initial).toBe('')
  })
  it('allow_relocate -- только у кабинетов amnezia/hidemyname', () => {
    const [, tog] = enableFields(RESP())
    expect(tog.type).toBe('toggle')
    expect(tog.showIf({ source: 'amnezia|nl' })).toBe(true)
    expect(tog.showIf({ source: 'hidemyname|de' })).toBe(true)
    expect(tog.showIf({ source: 'awg3|main/awg1' })).toBe(false)
    expect(tog.showIf({ source: '' })).toBe(false)
  })
  it('enableReady всегда true', () => {
    expect(enableReady({})).toBe(true)
    expect(enableReady({ source: '' })).toBe(true)
  })
  it('тело PUT', () => {
    expect(enableBody({ source: 'amnezia|nl', allow_relocate: true })).toEqual({ enabled: true, provider: 'amnezia', option: 'nl', allow_relocate: true })
    expect(enableBody({ source: 'awg3|main/awg1', allow_relocate: true })).toEqual({ enabled: true, provider: 'awg3', option: 'main/awg1', allow_relocate: false })
    expect(enableBody({ source: '', allow_relocate: true })).toEqual({ enabled: true, provider: '', option: '', allow_relocate: false })
  })
  it('подпись источника: бренд кабинета в ёлочках', () => {
    expect(sourceLabel(SOURCES, 'hidemyname', '')).toBe('«HideMy.name»')
  })
})

describe('латиница только в ёлочках', () => {
  const variants = [
    ['с резервом, подсказка', RESP({ suggested: { provider: 'amnezia', option: 'nl', why: 'x' } }), 'reserve'],
    ['без резерва', RESP(), ''],
    ['неизвестно', RESP(), null],
    ['без источников', RESP({ sources: [SOURCES[1]] }), 'r'],
    ['резерв сам', RESP(), '', 'main'],
    ['резерв сам, живого звена нет', RESP(), '', '', true],
    ['резерв сам, трафик через него', RESP(), '', '', true, true],
    ['смена страны израсходована', RESP({ relocate_spent: 'nl' }), ''],
    ['переименован', RESP({ enabled: true, rename_pending: 'Old' }), ''],
    ['настоящие подписи кабинетов', RESP({ suggested: { provider: 'hidemyname', option: 'de', why: 'x' }, sources: SOURCES.map((s) => ({ ...s, ok: true })) }), 'r'],
  ]
  for (const [name, resp, backup, carrier, reserve, self] of variants) {
    it(`лист: ${name}`, () => {
      const bad = allSheetStrings(resp, 'vpn-nl', backup, carrier, reserve, self).flatMap(latinOutside)
      expect(bad).toEqual([])
    })
  }
  it('метки и строка', () => {
    for (const s of ['on', 'limited', 'blocked']) expect(latinOutside(autorepairBadge(s).text)).toEqual([])
    expect(latinOutside(autorepairBadge('blocked', 'x').text)).toEqual([])
    for (const resp of [
      RESP(),
      RESP({ enabled: true }),
      RESP({ enabled: true, provider: 'amnezia', option: 'nl' }),
      RESP({ enabled: true, provider: 'hidemyname', option: 'de', sources: SOURCES.map((s) => ({ ...s, ok: true })) }),
      RESP({ enabled: true, provider: 'awg3', option: 'main/awg1' }),
      RESP({ enabled: true, provider: 'amnezia', option: 'nl', blocked: 'лимит попыток' }),
      RESP({ enabled: true, provider: 'amnezia', option: 'nl', rename_pending: 'Old' }),
    ]) {
      const r = autorepairRow(resp)
      expect(latinOutside(`${r.title} ${r.value} ${r.hint}`)).toEqual([])
    }
  })
})
