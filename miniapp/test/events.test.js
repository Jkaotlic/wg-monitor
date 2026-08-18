import { describe, it, expect } from 'vitest'
import { filterEvents, groupByDay, EVENT_FILTERS } from '../src/events.js'

const ev = (check, status, ts) => ({ check_name: check, status, ts })

describe('filterEvents', () => {
  const list = [
    ev('dns', 'fail', '2026-08-18T10:00:00Z'),
    ev('dns', 'ok', '2026-08-18T10:05:00Z'),
    ev('tunnel_awg12', 'warn', '2026-08-17T22:00:00Z'),
  ]

  it('фильтр "все" ничего не выбрасывает', () => {
    expect(filterEvents(list, 'all')).toHaveLength(3)
  })
  it('фильтр "тревоги" оставляет провалы и предупреждения', () => {
    expect(filterEvents(list, 'problems').map((e) => e.status)).toEqual(['fail', 'warn'])
  })
  it('фильтр "восстановления" оставляет только переходы в ok', () => {
    expect(filterEvents(list, 'recoveries').map((e) => e.status)).toEqual(['ok'])
  })
  it('неизвестный фильтр ведёт себя как "все"', () => {
    expect(filterEvents(list, 'нечто')).toHaveLength(3)
  })
  it('у каждого фильтра есть ключ и подпись', () => {
    expect(EVENT_FILTERS.map((f) => f.key)).toEqual(['all', 'problems', 'recoveries'])
    expect(EVENT_FILTERS.every((f) => f.label.length > 0)).toBe(true)
  })
})

describe('groupByDay', () => {
  it('складывает события в дни, свежий день первым', () => {
    const groups = groupByDay([
      ev('dns', 'ok', '2026-08-18T10:05:00Z'),
      ev('dns', 'fail', '2026-08-17T09:00:00Z'),
      ev('tunnel', 'ok', '2026-08-18T08:00:00Z'),
    ])
    expect(groups).toHaveLength(2)
    expect(groups[0].events).toHaveLength(2)
    expect(groups[0].day > groups[1].day).toBe(true)
  })

  it('внутри дня порядок входа сохраняется -- сервер уже отдал свежие первыми', () => {
    const groups = groupByDay([
      ev('dns', 'ok', '2026-08-18T10:05:00Z'),
      ev('dns', 'fail', '2026-08-18T09:00:00Z'),
    ])
    expect(groups[0].events.map((e) => e.status)).toEqual(['ok', 'fail'])
  })

  it('пустой список даёт пустой результат, а не группу-призрак', () => {
    expect(groupByDay([])).toEqual([])
    expect(groupByDay()).toEqual([])
  })
})
