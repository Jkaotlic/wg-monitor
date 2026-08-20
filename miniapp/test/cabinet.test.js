import { describe, it, expect } from 'vitest'
import { accountSummary, optionRows } from '../src/cabinet.js'

describe('accountSummary', () => {
  it('подключённый кабинет говорит про подписку и устройства', () => {
    const s = accountSummary({
      provider: 'amnezia', label: 'Amnezia Premium', connected: true,
      status: 'active', ends_at: '2026-12-01', devices_used: 2, devices_max: 5,
      options: [{ id: 'nl', label: 'Нидерланды' }],
    })
    expect(s.title).toBe('Amnezia Premium')
    expect(s.lines).toContain('Устройств занято 2 из 5')
    expect(s.lines.join(' ')).toContain('2026-12-01')
    expect(s.canIssue).toBe(true)
  })

  // Свободных мест нет -- выпускать нельзя, и сказать это надо ДО того, как
  // человек выберет страну и получит отказ от кабинета.
  it('без свободных устройств выпуск закрыт и объяснён', () => {
    const s = accountSummary({
      provider: 'amnezia', connected: true, devices_used: 5, devices_max: 5,
      options: [{ id: 'nl', label: 'Нидерланды' }],
    })
    expect(s.canIssue).toBe(false)
    expect(s.reason).toContain('мест')
  })

  // Неподключённый кабинет -- состояние, а не поломка: у него своя фраза, и
  // приложение не спрашивает ключей.
  it('неподключённый кабинет объясняет себя словами кабинета', () => {
    const s = accountSummary({ provider: 'hidemyname', label: 'HideMy.name', connected: false, note: 'Код доступа не сохранён.' })
    expect(s.canIssue).toBe(false)
    expect(s.reason).toBe('Код доступа не сохранён.')
  })
})

describe('optionRows', () => {
  it('уже выпущенное помечено, чтобы не выпускать второй раз вслепую', () => {
    const rows = optionRows({ options: [{ id: 'nl', label: 'Нидерланды' }, { id: 'de', label: 'Германия', issued: true }] })
    expect(rows[0]).toEqual({ id: 'nl', label: 'Нидерланды', note: '' })
    expect(rows[1].note).toBe('уже выпущен')
  })

  it('пустой список остаётся пустым, а не выдумывает строки', () => {
    expect(optionRows(null)).toEqual([])
  })
})
