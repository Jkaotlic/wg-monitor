import { describe, it, expect } from 'vitest'
import { stampText, secondsBetween } from '../src/stamp.js'

describe('stampText', () => {
  const NOW = Date.parse('2026-09-29T12:00:00Z')
  it('день.месяц часы:минуты в заданном поясе', () => {
    expect(stampText('2026-09-12T14:20:00Z', { timeZone: 'UTC', now: NOW })).toBe('12 сен, 14:20')
    expect(stampText('2026-09-12T14:20:00Z', { timeZone: 'Europe/Moscow', now: NOW })).toBe('12 сен, 17:20')
    expect(stampText('2026-01-02T03:04:00Z', { timeZone: 'UTC', now: NOW })).toBe('2 янв, 03:04')
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
