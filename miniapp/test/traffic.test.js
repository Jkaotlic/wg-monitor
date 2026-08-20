import { describe, it, expect } from 'vitest'
import { formatBytes, trafficSummary } from '../src/traffic.js'

describe('formatBytes', () => {
  // Байты человек не читает: 3 200 000 000 -- это «3 ГБ», а не число с
  // девятью знаками.
  it('переводит байты в человеческие единицы', () => {
    expect(formatBytes(0)).toBe('0 Б')
    expect(formatBytes(900)).toBe('900 Б')
    expect(formatBytes(1536)).toBe('1,5 КБ')
    expect(formatBytes(3_200_000_000)).toBe('3,0 ГБ')
  })

  it('неизвестное остаётся неизвестным', () => {
    expect(formatBytes(null)).toBe('')
  })
})

describe('trafficSummary', () => {
  const OUT = JSON.stringify({
    tunnel_id: 'awg11',
    period: '24h',
    rx_total: 3_221_225_472,
    tx_total: 268_435_456,
    points: [{ t: '2026-08-20T09:00:00Z', rx: 1000, tx: 200 }],
  })

  it('суммы приезжают от агента и печатаются как есть', () => {
    const s = trafficSummary(OUT)
    expect(s.known).toBe(true)
    expect(s.rx).toBe('3,0 ГБ')
    expect(s.tx).toBe('256,0 МБ')
    expect(s.points).toBe(1)
  })

  // Пустой ряд -- это «обмена не было», а не «неизвестно».
  it('пустой ряд отличается от отсутствия ответа', () => {
    const s = trafficSummary(JSON.stringify({ tunnel_id: 'awg11', rx_total: 0, tx_total: 0, points: [] }))
    expect(s.known).toBe(true)
    expect(s.rx).toBe('0 Б')
    expect(s.empty).toBe(true)
  })

  it('чужой ответ не выдаётся за ряд', () => {
    expect(trafficSummary('готово').known).toBe(false)
  })
})
