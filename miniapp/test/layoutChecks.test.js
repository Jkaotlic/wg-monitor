import { describe, it, expect } from 'vitest'
import { findProblems, rowMismatches, netProblems, isKnownNoise, SMALL_OK, unstyledControls, stripProblems, gridRowProblems, headingProblems, neighbourProblems, pairCoverageProblem } from '../layout/checks.js'
import { SCREENS, ROLES, expectPattern } from '../layout/screens.js'

const clean = { scrollWidth: 360, innerWidth: 360, targets: [{ text: 'Роутер', sel: 'button.tabbar-item', w: 72, h: 56 }], limes: ['Починить'], smallText: [], clipped: [], rows: [] }

describe('скрипт раскладки: оценщики', () => {
  it('чистый экран -- без находок', () => {
    expect(findProblems(clean)).toEqual([])
  })
  it('1: страница шире окна', () => {
    expect(findProblems({ ...clean, scrollWidth: 371 }).map((p) => p.check)).toEqual([1])
  })
  it('2: цель меньше 40×40, по каждой стороне', () => {
    const p = findProblems({ ...clean, targets: [{ text: 'История за 24ч', sel: 'button.link-quiet', w: 120, h: 22 }, { text: '✖', sel: 'button.btn-icon', w: 32, h: 40 }] })
    expect(p.map((x) => x.check)).toEqual([2, 2])
    expect(p[0].what).toContain('120×22')
  })
  it('3: больше одной лаймовой', () => {
    expect(findProblems({ ...clean, limes: ['Новый VPN-туннель', 'Выпустить и положить на роутер'] }).map((p) => p.check)).toEqual([3])
  })
  it('4 и 5: мелкий и обрезанный текст', () => {
    const p = findProblems({ ...clean, smallText: [{ text: 'VPN-туннели', size: 11, sel: 'button.tabbar-item' }], clipped: [{ text: 'дача-северная', sel: 'span.strip-name', sw: 140, cw: 90 }] })
    expect(p.map((x) => x.check)).toEqual([4, 5])
  })
  it('6: кнопки одного ряда разной высоты', () => {
    const rows = [
      { sel: 'div.action-row', items: [{ text: 'Перезапустить VPN-туннель', h: 64 }, { text: 'Не беспокоить…', h: 44 }] },
      { sel: 'div.action-row', items: [{ text: 'A', h: 44 }, { text: 'B', h: 45 }] },
    ]
    expect(rowMismatches(rows)).toHaveLength(1)
    expect(findProblems({ ...clean, rows }).map((p) => p.check)).toEqual([6])
  })
  it('7: консоль и ответы ≥400, кроме известного шума', () => {
    const events = [
      { kind: 'console', text: 'TypeError: x is undefined' },
      { kind: 'http', status: 502, method: 'GET', url: '/v1/miniapp/routers/3/vpn/awg3' },
    ]
    expect(netProblems(events).map((p) => p.check)).toEqual([7, 7])
    expect(isKnownNoise({ kind: 'console', text: 'x' })).toBe(false)
  })
  it('7: известный шум -- только по записи с причиной', () => {
    expect(netProblems([{ kind: 'http', status: 409, method: 'GET', url: '/v1/miniapp/awg3panels/old/peers' }])).toEqual([])
    expect(netProblems([{ kind: 'http', status: 409, method: 'GET', url: '/v1/miniapp/awg3panels/main/peers' }])).toHaveLength(1)
  })
  it('7: 400 на командах роутера -- находка, исключения нет', () => {
    const e = { kind: 'http', status: 400, method: 'POST', url: '/v1/miniapp/routers/7/commands', detail: 'tunnel_traffic awg12: unknown_tunnel' }
    expect(isKnownNoise(e)).toBe(false)
    expect(netProblems([e]).map((p) => p.check)).toEqual([7])
  })
  it('7: дубль в консоли гасится только при http с тем же кодом', () => {
    const dup = { kind: 'console', text: 'Failed to load resource: the server responded with a status of 400 (Bad Request)' }
    const http400 = { kind: 'http', status: 400, method: 'POST', url: '/v1/miniapp/routers/7/commands' }
    expect(netProblems([http400, dup])).toHaveLength(1)
    expect(netProblems([dup])).toHaveLength(1)
    expect(netProblems([{ ...http400, status: 500 }, dup])).toHaveLength(2)
  })
  it('исключения мелкого текста -- машинные коды', () => {
    expect(SMALL_OK).toEqual(['.data-row-code', '.tunnel-id', '.ev-code', '.raw-dump'])
  })
  // ---- проверка 8: элемент управления в оформлении браузера ----
  const fonts = ['"IBM Plex Sans", system-ui, sans-serif', '"IBM Plex Mono", ui-monospace, monospace']
  const ua = { button: { bg: 'rgb(239, 239, 239)', font: 'Arial' }, input: { bg: 'rgb(255, 255, 255)', font: 'Arial' }, a: { color: 'rgb(0, 0, 238)' } }
  const styled = { tag: 'button', text: 'Роутер', sel: 'button.side-link', bg: 'rgba(0, 0, 0, 0)', border: 'none', font: fonts[0], color: 'rgb(238, 243, 245)' }
  it('8: оформленная кнопка -- без находок', () => {
    expect(unstyledControls({ controls: [styled, { ...styled, font: fonts[1] }], fonts, ua })).toEqual([])
    expect(findProblems({ ...clean, controls: [styled], fonts, ua })).toEqual([])
  })
  it('8: фон кнопки браузера -- находка (серый 239/240, тёмный и измеренный на странице)', () => {
    for (const bg of ['rgb(239, 239, 239)', 'rgb(240, 240, 240)', 'buttonface']) {
      expect(unstyledControls({ controls: [{ ...styled, bg }], fonts, ua: {} }), bg).toHaveLength(1)
    }
    const dark = { button: { bg: 'rgb(107, 107, 107)', font: 'Arial' } }
    expect(unstyledControls({ controls: [{ ...styled, bg: 'rgb(107, 107, 107)' }], fonts, ua: dark })).toHaveLength(1)
    // тот же цвет у оформленной кнопки на странице, где браузер красит иначе, -- не находка
    expect(unstyledControls({ controls: [{ ...styled, bg: 'rgb(107, 107, 107)' }], fonts, ua })).toEqual([])
  })
  it('8: рамка outset/inset и чужой шрифт -- находки, причина названа', () => {
    const p = findProblems({ ...clean, controls: [{ ...styled, border: 'outset' }, { ...styled, tag: 'input', sel: 'input', border: 'inset' }, { ...styled, sel: 'button.strip-chip', text: 'дача-северная', font: 'Arial' }], fonts, ua })
    expect(p.map((x) => x.check)).toEqual([8, 8, 8])
    expect(p[0].what).toContain('рамка outset')
    expect(p[2].what).toContain('шрифт')
    expect(p[2].what).toContain('button.strip-chip')
  })
  it('8: поле ввода с фоном браузера и ссылка-кнопка цвета ссылки браузера', () => {
    const input = { ...styled, tag: 'input', sel: 'input.field', bg: 'rgb(255, 255, 255)' }
    const link = { ...styled, tag: 'a', sel: 'a.btn', color: 'rgb(0, 0, 238)' }
    expect(unstyledControls({ controls: [input, link], fonts, ua })).toHaveLength(2)
    // белая КНОПКА -- оформление, а не браузер: фон сверяется по своему тегу
    expect(unstyledControls({ controls: [{ ...styled, bg: 'rgb(255, 255, 255)' }], fonts, ua })).toEqual([])
  })
  it('8: без данных о страницах (старый сбор) -- не падает', () => {
    expect(findProblems(clean)).toEqual([])
  })

  // ---- шаг 0a: «expect» по роли ----
  it('expect: строка -- всем ролям, объект -- по роли, роли без фразы нет', () => {
    expect(expectPattern({ expect: 'Кто может' }, 'admin')).toBe('Кто может')
    expect(expectPattern({ expect: { admin: 'А', issuer: 'Б' } }, 'issuer')).toBe('Б')
    expect(() => expectPattern({ expect: { admin: 'А' } }, 'issuer')).toThrow(/issuer/)
  })
  it('cabinet-awg3: у каждой роли своя фраза недоступной панели', () => {
    const step = SCREENS.find((s) => s.id === 'cabinet-awg3').steps.find((s) => s.expect)
    expect(expectPattern(step, 'issuer')).toBe('Панель VPN-сервера сейчас недоступна, сообщите администратору')
    expect(expectPattern(step, 'admin')).toBe('Панель сейчас не отвечает')
    expect(new RegExp(expectPattern(step, 'admin')).test(expectPattern(step, 'issuer'))).toBe(false)
    expect(new RegExp(expectPattern(step, 'issuer')).test('Панель сейчас не отвечает — попробуйте позже.')).toBe(false)
  })

  // ---- пропусков нет: ни одного optional-экрана, у каждой роли есть экраны ----
  it('в обходе нет необязательных экранов; у каждой роли есть свой экран', () => {
    expect(SCREENS.filter((sc) => 'optional' in sc).map((sc) => sc.id)).toEqual([])
    for (const role of ROLES) expect(SCREENS.some((sc) => sc.roles.includes(role)), role).toBe(true)
    for (const id of ['noaccess', 'job', 'backenddeploy', 'hrneo-start']) expect(SCREENS.some((sc) => sc.id === id), id).toBe(true)
    // «Запустить» снимается у каждого, кто может запускать HydraRoute Neo.
    expect(SCREENS.filter((sc) => sc.id === 'hrneo-start').flatMap((sc) => sc.roles).sort()).toEqual(['admin', 'owner1', 'owner3'])
  })
})

// Проверки 9 и 10 (доводка v0.52): полоса роутеров и ряд карточек Парка.
describe('скрипт раскладки: полоса роутеров (9)', () => {
  const chipAt = (text, left, right, extra = {}) => ({ text, left, right, top: 70, bottom: 110, alert: false, hit: true, ...extra })
  it('чипы рядом, касание попадает в свой чип -- находок нет', () => {
    expect(stripProblems([chipAt('sandbox-broken', 16, 166, { alert: true }), chipAt('дача-северная', 174, 320), chipAt('router4car4new', 328, 360)])).toEqual([])
    expect(findProblems({ ...clean, strip: [chipAt('a', 16, 106), chipAt('b', 114, 204)] })).toEqual([])
  })
  it('красный закрывает соседа -- находка с числами', () => {
    const p = findProblems({ ...clean, strip: [chipAt('sandbox-broken', 16, 165.5, { alert: true }), chipAt('дача-северная', 41.5, 186.9, { hit: false })] })
    expect(p.map((x) => x.check)).toEqual([9, 9])
    expect(p[0].what).toContain('«sandbox-broken»')
    expect(p[0].what).toContain('«дача-северная»')
    expect(p[0].what).toContain('124')
    expect(p[1].what).toContain('касание')
  })
  it('соприкосновение краями и полпикселя -- не наложение', () => {
    expect(stripProblems([chipAt('a', 16, 106), chipAt('b', 106.4, 200)])).toEqual([])
  })
  it('чипы на разных строках не пересекаются', () => {
    expect(stripProblems([chipAt('a', 16, 106), { ...chipAt('b', 16, 106), top: 120, bottom: 160 }])).toEqual([])
  })
})

describe('скрипт раскладки: кнопки карточек одного ряда сетки (10)', () => {
  const card = (name, top, btnTop) => ({ sel: 'div.park-cards', name, top, btnTop })
  it('в одном ряду кнопки на одной высоте -- находок нет', () => {
    expect(gridRowProblems([card('a', 805, 917), card('b', 805, 917), card('c', 981, 1094), card('d', 981, 1094.4)])).toEqual([])
  })
  it('в одном ряду кнопки на разной высоте -- находка', () => {
    const p = findProblems({ ...clean, gridCards: [card('sandbox-broken', 805, 917), card('sandbox-bronya', 805, 896)] })
    expect(p.map((x) => x.check)).toEqual([10])
    expect(p[0].what).toContain('«sandbox-broken» 917')
    expect(p[0].what).toContain('«sandbox-bronya» 896')
  })
  it('карточки разных рядов и разных сеток не сравниваются', () => {
    expect(gridRowProblems([card('a', 805, 917), card('b', 981, 1094), { ...card('c', 805, 870), sel: 'div.other' }])).toEqual([])
  })
})

describe('скрипт раскладки: сосед раскрытой карточки ряда', () => {
  const m = (height, btnTop) => ({ name: 'sandbox-bronya', height, btnTop })
  it('высота и кнопки соседа на месте -- находок нет', () => {
    expect(neighbourProblems('sandbox-broken', m(133, 917), m(133.4, 917))).toEqual([])
  })
  it('сосед вытянулся -- находка', () => {
    const p = neighbourProblems('sandbox-broken', m(133, 917), m(420, 917))
    expect(p).toHaveLength(1)
    expect(p[0]).toContain('сменил высоту: 133 → 420')
  })
  it('кнопки соседа уехали -- находка', () => {
    expect(neighbourProblems('sandbox-broken', m(133, 917), m(133, 896))[0]).toContain('кнопки соседа «sandbox-bronya» уехали: 917 → 896')
  })
  it('соседа не стало -- находка', () => {
    expect(neighbourProblems('sandbox-broken', m(133, 917), undefined)).toHaveLength(1)
  })
})

describe('скрипт раскладки: сверка соседа не проходит вхолостую', () => {
  it('от 1100 px две карточки и ноль сверенных пар -- находка', () => {
    expect(pairCoverageProblem(1440, 8, 0)).toContain('сосед не сверен')
    expect(pairCoverageProblem(1100, 2, 0)).toContain('1100')
  })
  it('пары сверены -- находки нет', () => {
    expect(pairCoverageProblem(1440, 8, 8)).toBe(null)
  })
  it('уже 1100 px (столбик) и одна карточка -- ноль пар допустим', () => {
    expect(pairCoverageProblem(1024, 8, 0)).toBe(null)
    expect(pairCoverageProblem(390, 8, 0)).toBe(null)
    expect(pairCoverageProblem(1440, 1, 0)).toBe(null)
  })
})

describe('скрипт раскладки: уровни заголовков (11)', () => {
  it('h3 в группе с h2 -- находок нет', () => {
    expect(headingProblems([{ level: 3, text: 'Раздельный DNS', owner: { level: 2, text: 'Интернет и DNS' } }, { level: 2, text: 'Интернет и DNS', owner: null }])).toEqual([])
  })
  it('h2 в группе с h2 -- находка', () => {
    const p = findProblems({ ...clean, headings: [{ level: 2, text: 'Раздельный DNS', owner: { level: 2, text: 'Интернет и DNS' } }] })
    expect(p.map((x) => x.check)).toEqual([11])
    expect(p[0].what).toContain('h2 «Раздельный DNS»')
    expect(p[0].what).toContain('h2 «Интернет и DNS»')
  })
})
