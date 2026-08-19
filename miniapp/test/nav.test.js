import { describe, it, expect } from 'vitest'
import { initialNav, navReducer, backButtonVisible } from '../src/nav.js'

describe('initialNav', () => {
  it('открывает роутер из deep-link', () => {
    const s = initialNav({ routerIDs: [7, 9], deepLinkID: 9 })
    expect(s.routerID).toBe(9)
    expect(s.overlay).toBe(null)
  })

  it('единственный доступный роутер открывается без промежуточного списка', () => {
    expect(initialNav({ routerIDs: [42], deepLinkID: null }).routerID).toBe(42)
  })

  it('при нескольких роутерах и без deep-link открывает список', () => {
    const s = initialNav({ routerIDs: [1, 2], deepLinkID: null })
    expect(s.routerID).toBe(null)
    expect(s.overlay).toBe('fleet')
  })

  it('deep-link на недоступный роутер не выбирает его молча', () => {
    // Доступ проверяет сервер, но и клиент не должен делать вид, что
    // чужой роутер открыт: список честнее, чем экран с 404 внутри.
    const s = initialNav({ routerIDs: [1, 2], deepLinkID: 99 })
    expect(s.routerID).toBe(null)
    expect(s.overlay).toBe('fleet')
  })

  it('без доступа к роутерам не открывает список', () => {
    const s = initialNav({ routerIDs: [], deepLinkID: null })
    expect(s.routerID).toBe(null)
    expect(s.overlay).toBe(null)
  })

  it('всегда стартует с таба роутера', () => {
    expect(initialNav({ routerIDs: [1], deepLinkID: null }).tab).toBe('router')
  })
})

describe('navReducer', () => {
  const base = initialNav({ routerIDs: [1], deepLinkID: null })

  it('смена роутера возвращает на таб роутера и закрывает оверлей', () => {
    const s = navReducer({ ...base, tab: 'events', overlay: 'fleet' }, { type: 'router', id: 5 })
    expect(s).toMatchObject({ routerID: 5, tab: 'router', overlay: null })
  })

  it('back закрывает шит раньше оверлея', () => {
    const open = { ...base, overlay: 'admin', sheet: { title: 'Перезапустить туннель' } }
    const afterFirst = navReducer(open, { type: 'back' })
    expect(afterFirst.sheet).toBe(null)
    expect(afterFirst.overlay).toBe('admin')
    expect(navReducer(afterFirst, { type: 'back' }).overlay).toBe(null)
  })

  it('back на корневом экране ничего не меняет', () => {
    expect(navReducer(base, { type: 'back' })).toEqual(base)
  })

  it('неизвестный таб игнорируется', () => {
    expect(navReducer(base, { type: 'tab', tab: 'нечто' }).tab).toBe('router')
  })

  it('смена таба не трогает выбранный роутер', () => {
    const s = navReducer({ ...base, routerID: 3 }, { type: 'tab', tab: 'events' })
    expect(s).toMatchObject({ routerID: 3, tab: 'events' })
  })

  it('шит обновляется на месте -- ход выполнения виден в том же слое', () => {
    const running = navReducer({ ...base, sheet: { title: 'x', busy: false } }, {
      type: 'sheet',
      sheet: { title: 'x', busy: true },
    })
    expect(running.sheet.busy).toBe(true)
  })
})

describe('navReducer: init', () => {
  it('подставляет состояние, посчитанное после загрузки списка роутеров', () => {
    // Список приходит с сервера уже после первого рендера, поэтому стартовое
    // состояние считается дважды: пустым при монтировании и настоящим здесь.
    const s = navReducer(initialNav({ routerIDs: [], deepLinkID: null }), {
      type: 'init',
      state: initialNav({ routerIDs: [4], deepLinkID: 4 }),
    })
    expect(s).toMatchObject({ routerID: 4, tab: 'router', overlay: null })
  })

  it('init без состояния ничего не ломает', () => {
    const base = initialNav({ routerIDs: [1], deepLinkID: null })
    expect(navReducer(base, { type: 'init' })).toEqual(base)
  })
})

describe('backButtonVisible', () => {
  const base = initialNav({ routerIDs: [1], deepLinkID: null })
  it('скрыта на корневом экране', () => {
    expect(backButtonVisible(base)).toBe(false)
  })
  it('видна при открытом оверлее', () => {
    expect(backButtonVisible({ ...base, overlay: 'fleet' })).toBe(true)
  })
  it('видна при открытом шите', () => {
    expect(backButtonVisible({ ...base, sheet: { title: 'x' } })).toBe(true)
  })
})
