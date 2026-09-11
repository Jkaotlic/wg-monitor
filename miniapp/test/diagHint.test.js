import { describe, it, expect } from 'vitest'
import { reportStamp, reportHint } from '../src/diag.js'

// Отметка под «Собрать отчёт»: когда снят и сколько собирался. Десятичный
// разделитель у владельца -- запятая: «16.4 с» читается как дата или версия.
const WHEN = /^\d{2}\.\d{2}\.\d{4}, \d{2}:\d{2}/

describe('reportStamp', () => {
  it('дробные секунды пишет через запятую', () => {
    const s = reportStamp('2026-08-18T09:00:00Z', 16416)
    expect(s).toMatch(WHEN)
    expect(s.endsWith(' · сбор занял 16,4 с')).toBe(true)
  })

  it('целые секунды -- без «,0»', () => {
    expect(reportStamp('2026-08-18T09:00:00Z', 16000).endsWith(' · сбор занял 16 с')).toBe(true)
  })

  it('доли секунды -- тоже через запятую', () => {
    expect(reportStamp('2026-08-18T09:00:00Z', 400).endsWith(' · сбор занял 0,4 с')).toBe(true)
  })

  it('меньше десятой секунды не выдаётся за ноль', () => {
    expect(reportStamp('2026-08-18T09:00:00Z', 30).endsWith(' · сбор занял меньше 0,1 с')).toBe(true)
  })

  it('минуту и дольше считает минутами', () => {
    expect(reportStamp('2026-08-18T09:00:00Z', 95_400).endsWith(' · сбор занял 1 мин 35 с')).toBe(true)
    expect(reportStamp('2026-08-18T09:00:00Z', 120_000).endsWith(' · сбор занял 2 мин')).toBe(true)
  })

  it('без длительности -- только когда', () => {
    expect(reportStamp('2026-08-18T09:00:00Z', null)).toMatch(new RegExp(WHEN.source + '$'))
    expect(reportStamp('2026-08-18T09:00:00Z', 0)).toMatch(new RegExp(WHEN.source + '$'))
  })

  it('без времени снятия -- пусто', () => {
    expect(reportStamp(null, 16416)).toBe('')
  })
})

// Подпись под кнопкой. До отчёта она обещает, что туннели не тронут; после --
// обещание не пропадает: второе нажатие должно быть таким же спокойным, как
// первое.
describe('reportHint', () => {
  const CALM = 'VPN-туннели при сборе не перезапускаются.'

  it('до отчёта -- полная подсказка', () => {
    expect(reportHint(null)).toEqual([
      'Роутер проверит себя заново — это займёт до минуты. VPN-туннели при этом не перезапускаются.',
    ])
  })

  it('после отчёта -- отметка и короткое обещание', () => {
    const lines = reportHint({ generatedAt: '2026-08-18T09:00:00Z', durationMs: 16416 })
    expect(lines).toHaveLength(2)
    expect(lines[0].endsWith('сбор занял 16,4 с')).toBe(true)
    expect(lines[1]).toBe(CALM)
  })

  it('отчёт без времени снятия -- одно обещание, без пустой строки', () => {
    expect(reportHint({ generatedAt: null, durationMs: null })).toEqual([CALM])
  })
})
