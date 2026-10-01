import { describe, it, expect } from 'vitest'
import { findProblems, rowMismatches, netProblems, isKnownNoise, SMALL_OK } from '../layout/checks.js'

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
})
