import { describe, it, expect } from 'vitest'
import { groupByDay } from '../src/events.js'

const ev = (check, status, ts) => ({ check_name: check, status, ts })

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
