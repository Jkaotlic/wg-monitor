import { describe, it, expect } from 'vitest'
import { maintenanceNotice } from '../src/maintenanceNotice.js'

// Оператор 18.09: на главном экране роутера всем подсвечивать, что нужна
// перезагрузка или обновление.
describe('подсветка обслуживания на главном экране', () => {
  it('нечего сказать -- ничего не рисуем', () => {
    expect(maintenanceNotice(null)).toBe(null)
    expect(maintenanceNotice({ rows: [], unknown: [] })).toBe(null)
  })
  it('перезагрузка -- главная строка и тревожный тон', () => {
    const n = maintenanceNotice({ rows: [{ component: 'awgmgr', name: 'awg-manager', installed: '2.19.1', available: '2.19.3' }], reboot_hint: 'x' })
    expect(n.tone).toBe('warn')
    expect(n.title).toBe('Нужна перезагрузка роутера')
    expect(n.lines).toContain('awg-manager: 2.19.3 (сейчас 2.19.1)')
  })
  it('только обновления -- спокойный тон, агент тоже назван', () => {
    const n = maintenanceNotice({ rows: [{ component: 'firmware', name: 'Прошивка', installed: '5.02.A.8', available: '5.02.A.9' }], agent: { installed: 'v0.41.0', available: 'v0.43.0' } })
    expect(n.tone).toBe('info')
    expect(n.title).toBe('Есть обновления')
    expect(n.lines).toEqual(['Прошивка: 5.02.A.9 (сейчас 5.02.A.8)', 'Агент wg-monitor: v0.43.0 (сейчас v0.41.0)'])
  })
})
