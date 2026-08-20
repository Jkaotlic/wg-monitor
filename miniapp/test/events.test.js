import { describe, it, expect } from 'vitest'
import { filterEvents, groupByDay, EVENT_FILTERS, annotateEvents } from '../src/events.js'

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

// --- Журнал: фраза о последствии, под ней код мелким -----------------------
describe('annotateEvents', () => {
  // Лента приходит свежими вперёд: восстановление лежит ВЫШЕ падения, и
  // длительность считается по паре, а не по одному событию.
  const EVENTS = [
    { check_name: 'tunnel_awg20', status: 'ok', ts: '2026-08-20T14:05:00Z' },
    { check_name: 'tunnel_awg20', status: 'fail', ts: '2026-08-20T14:02:00Z' },
    { check_name: 'dns', status: 'fail', ts: '2026-08-20T13:00:00Z' },
  ]
  const NOW = Date.parse('2026-08-20T14:30:00Z')

  it('падение с восстановлением знает, сколько лежало', () => {
    const rows = annotateEvents(EVENTS, NOW)
    const down = rows.find((r) => r.check_name === 'tunnel_awg20' && r.status === 'fail')
    expect(down.durationSec).toBe(180)
    expect(down.ongoing).toBe(false)
    expect(down.code).toBe('tunnel_awg20 · лежал 3 мин')
  })

  // Незакрытое падение -- это «идёт до сих пор», а не «неизвестно сколько».
  it('падение без восстановления считается от текущего момента', () => {
    const rows = annotateEvents(EVENTS, NOW)
    const dns = rows.find((r) => r.check_name === 'dns')
    expect(dns.ongoing).toBe(true)
    expect(dns.code).toBe('dns · идёт 1 ч')
  })

  it('восстановление называет, сколько длилась поломка', () => {
    const rows = annotateEvents(EVENTS, NOW)
    const up = rows.find((r) => r.check_name === 'tunnel_awg20' && r.status === 'ok')
    expect(up.code).toBe('tunnel_awg20 · после 3 мин')
    expect(up.tone).toBe('sig')
  })

  // Фраза -- о последствии для человека, код -- под ней мелким.
  it('фраза говорит о последствии, а не об имени проверки', () => {
    const rows = annotateEvents(EVENTS, NOW)
    expect(rows.find((r) => r.check_name === 'dns').title).toBe('Не определяются адреса сайтов')
    expect(rows.find((r) => r.status === 'ok').title).toContain('снова')
  })

  // Одиночное восстановление без предшествующего падения в окне: длительности
  // нет, и выдумывать её нечем -- в коде остаётся одно имя проверки.
  it('восстановление без пары не выдумывает длительность', () => {
    const rows = annotateEvents([{ check_name: 'dns', status: 'ok', ts: '2026-08-20T14:00:00Z' }], NOW)
    expect(rows[0].code).toBe('dns')
    expect(rows[0].durationSec).toBe(null)
  })
})
