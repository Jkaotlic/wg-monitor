// MINI-08: «Что было» делит дни по местному времени человека, а не по UTC.
// Часовой пояс задан явно: во Владивостоке (UTC+10) происшествие в 06:00
// утра 9-го по UTC случилось ещё 8-го в 20:00 -- и было записано во «вчера».
process.env.TZ = 'Asia/Vladivostok'
import { describe, it, expect } from 'vitest'
import { groupIncidentsByDay, dayTitle, localDay } from '../src/incidents.js'
import { groupByDay } from '../src/events.js'

// Форма происшествия -- timeline.Incident (check_name/from/to/down_sec/ongoing).
const inc = (from) => ({ check_name: 'dns', from, to: from, down_sec: 60, flaps: 1, ongoing: false })
const NOW = Date.parse('2026-09-09T02:00:00Z') // 12:00 9 сентября по Владивостоку

describe('MINI-08', () => {
  it('localDay -- дата по местным часам', () => {
    expect(localDay('2026-09-08T20:00:00Z')).toBe('2026-09-09')
  })
  it('происшествие попадает в местный день', () => {
    const groups = groupIncidentsByDay([inc('2026-09-08T20:00:00Z')], 2, NOW)
    expect(groups[0].day).toBe('2026-09-09')
    expect(groups[0].incidents).toHaveLength(1)
    expect(groups[1].quiet).toBe(true)
  })
  it('«Сегодня»/«Вчера» -- по местной дате', () => {
    expect(dayTitle('2026-09-09', NOW)).toBe('Сегодня')
    expect(dayTitle('2026-09-08', NOW)).toBe('Вчера')
  })
  it('сырая лента тоже по местному дню', () => {
    const groups = groupByDay([{ check_name: 'dns', status: 'fail', ts: '2026-09-08T20:00:00Z' }])
    expect(groups[0].day).toBe('2026-09-09')
  })
})
