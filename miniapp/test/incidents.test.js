import { describe, it, expect } from 'vitest'
import { incidentLine, groupIncidentsByDay } from '../src/incidents.js'

const inc = (check, from, to, extra = {}) => ({
  check_name: check, from, to, down_sec: 240, flaps: 1, ongoing: false, ...extra,
})

describe('incidentLine', () => {
  it('закрытая поломка говорит, что было и сколько длилось', () => {
    const line = incidentLine(inc('external_reach', '2026-09-09T14:20:00Z', '2026-09-09T14:24:00Z'))
    expect(line.title).toBe('Нет доступа в интернет')
    expect(line.detail).toContain('4 мин')
    expect(line.tone).toBe('warn')
  })

  // Моргание -- одна новость «линия неустойчива», и число морганий в ней
  // главное: оно отличает разовый сбой от того, что пора менять конфиг.
  it('моргание называет число раз и общий срок', () => {
    const line = incidentLine(inc('hydraroute', '2026-09-09T09:05:00Z', '2026-09-09T10:40:00Z', {
      flaps: 12, down_sec: 720,
    }))
    expect(line.title).toBe('Не работает обход блокировок')
    expect(line.detail).toContain('12 раз')
    expect(line.detail).toContain('12 мин')
  })

  it('идущая поломка не притворяется прошедшей', () => {
    const line = incidentLine(inc('dns', '2026-09-09T14:20:00Z', undefined, { ongoing: true, down_sec: 600 }))
    expect(line.ongoing).toBe(true)
    expect(line.detail).toContain('идёт')
    expect(line.tone).toBe('bad')
  })

  // Машинного имени в основной строке быть не должно: оно живёт в режиме
  // «как есть», где его и ищут.
  it('машинное имя проверки в строку не попадает', () => {
    const line = incidentLine(inc('external_reach', '2026-09-09T14:20:00Z', '2026-09-09T14:24:00Z'))
    expect(`${line.title} ${line.detail}`).not.toContain('external_reach')
  })

  // Туннель приезжает как tunnel_<id>: у него своя фраза, но и она обязана
  // быть человеческой.
  it('линия называется линией, а не строкой tunnel_awg12', () => {
    const line = incidentLine(inc('tunnel_awg12', '2026-09-09T14:20:00Z', '2026-09-09T14:24:00Z'))
    expect(line.title).not.toContain('tunnel_')
  })
})

describe('groupIncidentsByDay', () => {
  const now = Date.parse('2026-09-09T12:00:00Z')

  it('день без происшествий не исчезает, а говорит, что всё было хорошо', () => {
    const groups = groupIncidentsByDay(
      [inc('dns', '2026-09-09T09:00:00Z', '2026-09-09T09:04:00Z')], 3, now,
    )
    expect(groups.map((g) => g.day)).toEqual(['2026-09-09', '2026-09-08', '2026-09-07'])
    expect(groups[0].quiet).toBe(false)
    expect(groups[1].quiet).toBe(true)
    expect(groups[1].incidents).toEqual([])
  })

  it('свежий день первым', () => {
    const groups = groupIncidentsByDay([
      inc('dns', '2026-09-07T09:00:00Z', '2026-09-07T09:04:00Z'),
      inc('dns', '2026-09-09T09:00:00Z', '2026-09-09T09:04:00Z'),
    ], 3, now)
    expect(groups[0].incidents).toHaveLength(1)
    expect(groups[0].day).toBe('2026-09-09')
  })

  it('пустая неделя -- это семь тихих дней, а не пустой экран', () => {
    const groups = groupIncidentsByDay([], 7, now)
    expect(groups).toHaveLength(7)
    expect(groups.every((g) => g.quiet)).toBe(true)
  })
})

// Лента живёт снаружи роутера: снимка маршрутов рядом нет, и идентификатор
// линии человеку не говорит ничего.
describe('incidentLine -- линия без идентификатора', () => {
  it('упавшая линия называется линией, а не tunnel_awg12', () => {
    const line = incidentLine({
      check_name: 'tunnel_awg12', from: '2026-09-09T14:20:00Z', to: '2026-09-09T14:24:00Z',
      down_sec: 240, flaps: 1, ongoing: false,
    })
    expect(line.title).toBe('Одна из линий не отвечает')
    expect(line.title).not.toContain('awg12')
  })
})
