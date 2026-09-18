import { describe, it, expect } from 'vitest'
import { navReducer, backButtonVisible, escapeAction, initialNav } from '../src/nav.js'

// 18.09: оператор открывал мини-апп и видел пустое «Выберите роутер в списке».
// Список роутеров без выбранного роутера -- главный экран, а не крышка: «назад»
// (кнопка Telegram, свайп, Esc) закрывал его в пустоту.
describe('список роутеров -- главный экран, пока роутер не выбран', () => {
  const home = initialNav({ routerIDs: [1, 2, 3] })
  it('открыт сразу', () => {
    expect(home.routerID).toBe(null)
    expect(home.overlay).toBe('fleet')
  })
  it('назад и Esc его не закрывают, кнопки назад нет', () => {
    expect(navReducer(home, { type: 'back' }).overlay).toBe('fleet')
    expect(backButtonVisible(home, { wide: false })).toBe(false)
    expect(escapeAction(home, { wide: false })).toBe(null)
  })
  it('роутер из списка открывается, слой парка -- тоже', () => {
    const picked = navReducer(home, { type: 'router', id: 2 })
    expect(picked.routerID).toBe(2)
    expect(picked.overlay).toBe(null)
    expect(navReducer(home, { type: 'overlay', overlay: 'provision' }).overlay).toBe('provision')
  })
  it('с выбранным роутером список закрывается как прежде', () => {
    const opened = { ...home, routerID: 2 }
    expect(backButtonVisible(opened, { wide: false })).toBe(true)
    expect(navReducer(opened, { type: 'back' }).overlay).toBe(null)
  })
})
