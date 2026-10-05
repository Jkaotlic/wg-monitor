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

function allSheetStrings(resp, name, backup) {
  const t = enableSheetText(resp, name, backup)
  const out = [t.title, t.note, ...t.sections.flatMap((s) => [s.h, s.text])]
  for (const f of enableFields(resp)) {
    out.push(f.label)
    if (typeof f.hint === 'function') out.push(f.hint({ source: '', allow_relocate: false }), f.hint({ source: 'amnezia|nl' }))
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
})

describe('autorepairRow', () => {
  it('выключена', () => expect(autorepairRow(RESP()).value).toBe('выключена'))
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
    expect(backupFor(snap, 'a')).toEqual({ known: true, name: 'reserve' })
  })
  it('недоступное звено не резерв', () => {
    const s = { ...snap, policies: [{ interfaces: [{ tunnel_id: 'a', role: 'active' }, { tunnel_id: 'b', role: 'unavailable' }] }] }
    expect(backupFor(s, 'a')).toEqual({ known: true, name: '' })
  })
  it('available: false не резерв, available: true резерв', () => {
    const s = (av) => ({ ...snap, policies: [{ interfaces: [{ tunnel_id: 'a', role: 'active' }, { tunnel_id: 'b', available: av }] }] })
    expect(backupFor(s(false), 'a').name).toBe('')
    expect(backupFor(s(true), 'a').name).toBe('reserve')
  })
  it('набор без этого туннеля не считается', () => {
    expect(backupFor(snap, 'c')).toEqual({ known: true, name: '' })
  })
  it('нет снимка -- не знаем', () => {
    expect(backupFor(null, 'a')).toEqual({ known: false, name: '' })
    expect(backupFor({ tunnels: [] }, 'a')).toEqual({ known: false, name: '' })
  })
})

describe('enableSheetText', () => {
  const sec = (t, h) => t.sections.find((s) => s.h === h)?.text
  it('заголовок и четыре раздела', () => {
    const t = enableSheetText(RESP(), 'vpn-nl', 'reserve')
    expect(t.title).toBe('Включить автопочинку «vpn-nl»?')
    expect(t.sections.map((s) => s.h)).toEqual(['Что будет делать', 'Чего стоит', 'Кому напишу', 'Как выключить'])
    expect(sec(t, 'Кому напишу')).toBe('Владельцу, операторам и админу — в личку бота: что упало, что делаю и чем кончилось. Если понадобится ваше участие, напишу отдельно, со звуком.')
    expect(sec(t, 'Как выключить')).toBe('Этим же переключателем, в любой момент.')
  })
  it('с резервом', () => {
    const r = RESP({ suggested: { provider: 'amnezia', option: 'nl', why: 'так он был выпущен' } })
    expect(sec(enableSheetText(r, 'vpn-nl', 'reserve'), 'Что будет делать')).toBe(
      'Если VPN-туннель «vpn-nl» упадёт, я уведу трафик на запасной VPN-туннель «reserve», перезапущу «vpn-nl», а если не поможет — выпущу конфиг заново из «Amnezia Premium» и заменю его на роутере.',
    )
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
  it('цена по доступным источникам', () => {
    const cost = sec(enableSheetText(RESP(), 'x', ''), 'Чего стоит')
    expect(cost).toContain('Повторный выпуск того же конфига место в кабинете не тратит. Смена локации меняет страну, через которую видны сайты.')
    expect(cost).toContain('Если старое подключение на сервере не оживёт, заведу новое — оно займёт ещё одно место на сервере.')
    const only = sec(enableSheetText(RESP({ sources: [SOURCES[2]] }), 'x', ''), 'Чего стоит')
    expect(only).not.toContain('Повторный выпуск')
  })
  it('нет рабочих источников -- перевыпуска в тексте нет, note про урезанный режим', () => {
    const t = enableSheetText(RESP({ sources: [SOURCES[1]] }), 'x', 'r')
    expect(sec(t, 'Что будет делать')).not.toContain('выпущу')
    expect(t.note).toBe('Источник не выбран: смогу только перезапускать. Чтобы пересоздавать конфиг, выберите кабинет.')
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
  it('подсказка про урезанный режим следует выбору', () => {
    const [sel] = enableFields(RESP())
    expect(sel.hint({ source: '' })).toContain('Источник не выбран')
    expect(sel.hint({ source: 'amnezia|nl' })).toBe('')
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
    ['настоящие подписи кабинетов', RESP({ suggested: { provider: 'hidemyname', option: 'de', why: 'x' }, sources: SOURCES.map((s) => ({ ...s, ok: true })) }), 'r'],
  ]
  for (const [name, resp, backup] of variants) {
    it(`лист: ${name}`, () => {
      const bad = allSheetStrings(resp, 'vpn-nl', backup).flatMap(latinOutside)
      expect(bad).toEqual([])
    })
  }
  it('метки и строка', () => {
    for (const s of ['on', 'limited', 'blocked']) expect(latinOutside(autorepairBadge(s).text)).toEqual([])
    for (const resp of [
      RESP(),
      RESP({ enabled: true }),
      RESP({ enabled: true, provider: 'amnezia', option: 'nl' }),
      RESP({ enabled: true, provider: 'hidemyname', option: 'de', sources: SOURCES.map((s) => ({ ...s, ok: true })) }),
      RESP({ enabled: true, provider: 'awg3', option: 'main/awg1' }),
      RESP({ enabled: true, provider: 'amnezia', option: 'nl', blocked: 'лимит попыток' }),
    ]) {
      const r = autorepairRow(resp)
      expect(latinOutside(`${r.title} ${r.value} ${r.hint}`)).toEqual([])
    }
  })
})
