import { describe, it, expect } from 'vitest'
import { parseDiag } from '../src/diag.js'

// Форма ответа -- /api/diagnostics/result awg-manager, та же, что разбирает
// бэкенд в internal/backend/alerts/diag_report.go (version 1.0).
const REPORT = JSON.stringify({
  version: '1.0',
  generatedAt: '2026-08-18T09:00:00Z',
  durationMs: 2559,
  system: {
    appVersion: '2.16.4',
    keeneticOS: '4.1.7',
    uptime: '5 дней',
    totalMemoryMB: 256,
    kernelModule: { exists: true, loaded: true },
  },
  wan: {
    anyUp: true,
    interfaces: { ISP: { up: true, label: 'Провайдер' } },
  },
  tunnels: {
    awg12: { handshake: { status: 'ok' }, dns: { status: 'fail', reason: 'таймаут' } },
    awg10: { handshake: { status: 'ok' }, dns: { status: 'ok' } },
  },
})

describe('parseDiag', () => {
  it('вытаскивает время сбора и длительность', () => {
    const d = parseDiag(REPORT)
    expect(d.generatedAt).toBe('2026-08-18T09:00:00Z')
    expect(d.durationMs).toBe(2559)
  })

  it('система и WAN становятся карточками', () => {
    const cards = parseDiag(REPORT).cards
    expect(cards.find((c) => c.key === 'system').detail).toContain('2.16.4')
    expect(cards.find((c) => c.key === 'wan').tone).toBe('ok')
  })

  it('проверка, провалившаяся хоть на одном туннеле, красится тревожно', () => {
    const dns = parseDiag(REPORT).cards.find((c) => c.key === 'test:dns')
    expect(dns.tone).toBe('danger')
    expect(dns.detail).toContain('awg12')
  })

  it('проверка, прошедшая везде, зелёная', () => {
    expect(parseDiag(REPORT).cards.find((c) => c.key === 'test:handshake').tone).toBe('ok')
  })

  it('порядок карточек стабилен между разборами', () => {
    const a = parseDiag(REPORT).cards.map((c) => c.key)
    const b = parseDiag(REPORT).cards.map((c) => c.key)
    expect(a).toEqual(b)
  })

  it('нераспознанный ответ не выдумывает карточек, но сохраняет сырой текст', () => {
    const d = parseDiag('не json')
    expect(d.cards).toEqual([])
    expect(d.raw).toBe('не json')
    expect(d.parsed).toBe(false)
  })

  it('пустой ответ -- пустой разбор, а не падение', () => {
    expect(parseDiag('').cards).toEqual([])
    expect(parseDiag(null).cards).toEqual([])
  })
})
