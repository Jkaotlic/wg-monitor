import { describe, it, expect } from 'vitest'
import { stampText, secondsBetween } from '../src/stamp.js'

describe('stampText', () => {
  it('день.месяц часы:минуты в заданном поясе', () => {
    expect(stampText('2026-09-12T14:20:00Z', { timeZone: 'UTC' })).toBe('12.09 14:20')
    expect(stampText('2026-09-12T14:20:00Z', { timeZone: 'Europe/Moscow' })).toBe('12.09 17:20')
    expect(stampText('2026-01-02T03:04:00Z', { timeZone: 'UTC' })).toBe('02.01 03:04')
  })

  it('пусто и мусор -- пустая строка, а не «Invalid Date»', () => {
    expect(stampText('', { timeZone: 'UTC' })).toBe('')
    expect(stampText(null)).toBe('')
    expect(stampText('вчера')).toBe('')
  })
})

describe('secondsBetween', () => {
  it('секунды от первого времени до второго, не меньше нуля', () => {
    expect(secondsBetween('2026-09-17T10:00:00Z', '2026-09-17T10:00:40Z')).toBe(40)
    expect(secondsBetween('2026-09-17T10:00:40Z', '2026-09-17T10:00:00Z')).toBe(0)
    expect(secondsBetween('', '2026-09-17T10:00:00Z')).toBe(null)
  })
})
