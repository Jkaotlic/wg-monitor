import { describe, it, expect } from 'vitest'
import { sortByUrgency } from '../src/fleet.js'

const r = (id, status, nickname = 'r' + id) => ({ id, status, nickname })

describe('sortByUrgency', () => {
  it('тревога впереди офлайна, офлайн впереди спящего, спящий впереди живого', () => {
    const out = sortByUrgency([r(1, 'online'), r(2, 'sleeping'), r(3, 'alert'), r(4, 'offline')])
    expect(out.map((x) => x.id)).toEqual([3, 4, 2, 1])
  })

  it('внутри одного статуса -- по имени, чтобы порядок не прыгал между обновлениями', () => {
    const out = sortByUrgency([r(1, 'online', 'Дача'), r(2, 'online', 'Ватикан'), r(3, 'online', 'Дом')])
    expect(out.map((x) => x.nickname)).toEqual(['Ватикан', 'Дача', 'Дом'])
  })

  it('неизвестный статус уходит в конец, а не роняет сортировку', () => {
    const out = sortByUrgency([r(1, 'нечто'), r(2, 'alert')])
    expect(out.map((x) => x.id)).toEqual([2, 1])
  })

  it('не мутирует входной массив', () => {
    const input = [r(1, 'online'), r(2, 'alert')]
    sortByUrgency(input)
    expect(input.map((x) => x.id)).toEqual([1, 2])
  })

  it('пустой список остаётся пустым', () => {
    expect(sortByUrgency([])).toEqual([])
    expect(sortByUrgency()).toEqual([])
  })
})
