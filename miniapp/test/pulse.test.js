import { describe, it, expect } from 'vitest'
import { shouldPulse, freshnessLabel, PULSE_MS } from '../src/pulse.js'

describe('pulse', () => {
  // Пульс -- про открытый экран. Опрашивать спрятанную вкладку значит будить
  // бэкенд ради данных, которых никто не видит.
  it('молчит, когда экран не виден', () => {
    expect(shouldPulse({ visible: true, routerID: 5 })).toBe(true)
    expect(shouldPulse({ visible: false, routerID: 5 })).toBe(false)
    expect(shouldPulse({ visible: true, routerID: null })).toBe(false)
  })

  it('опрашивает раз в десять секунд', () => {
    expect(PULSE_MS).toBe(10000)
  })

  // Свежесть -- то, ради чего пульс затевался: цифра обязана тикать сама.
  it('называет возраст данных словами и склоняет его', () => {
    expect(freshnessLabel(6)).toBe('проверено 6 секунд назад')
    expect(freshnessLabel(1)).toBe('проверено 1 секунду назад')
    expect(freshnessLabel(3)).toBe('проверено 3 секунды назад')
    expect(freshnessLabel(75)).toContain('минуту')
  })

  // Молчащий роутер: показания -- на момент последнего отчёта, и выдавать их
  // за текущие нельзя.
  it('говорит про устаревшие данные прямо', () => {
    expect(freshnessLabel(4000)).toContain('устарел')
  })

  it('роутер, который ещё не отчитывался, не притворяется свежим', () => {
    expect(freshnessLabel(null)).toContain('ещё не отчитывался')
  })
})
