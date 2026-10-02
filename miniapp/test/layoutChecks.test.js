import { describe, it, expect } from 'vitest'
import { findProblems, rowMismatches, netProblems, isKnownNoise, SMALL_OK, unstyledControls, optionalSkip } from '../layout/checks.js'
import { SCREENS, expectPattern } from '../layout/screens.js'

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

  // ---- шаг 0c: пропуск optional -- только когда нет самой цели ----
  it('optional: пропуск -- лишь при отказе ПОСЛЕДНЕГО шага и выбранном роутере', () => {
    expect(optionalSkip({ optional: true, routerOk: true, failedStep: 2, steps: 3 })).toBe(true)
    expect(optionalSkip({ optional: true, routerOk: true, failedStep: 1, steps: 3 })).toBe(false)
    expect(optionalSkip({ optional: true, routerOk: false, failedStep: -1, steps: 3 })).toBe(false)
    expect(optionalSkip({ optional: false, routerOk: true, failedStep: 2, steps: 3 })).toBe(false)
    expect(optionalSkip({ optional: true, routerOk: true, failedStep: -1, steps: 3 })).toBe(false)
  })
})
