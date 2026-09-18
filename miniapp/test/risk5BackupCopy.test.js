import { describe, it, expect } from 'vitest'
import { backupCopy } from '../src/trafficPath.js'

// Ревью v0.41 (Opus, 18.09).

describe('RISK 5: несущий молчит, запасной жив -- трафик сам не перейдёт', () => {
  it('подпись не обещает «подхватит»', () => {
    const c = backupCopy({ backupLine: { tunnel_id: 'awg10', name: 'vpn-nl' }, carrierDown: true })
    expect(c.title).toBe('Запасной «vpn-nl» жив')
    expect(c.note).toMatch(/не перейдёт/)
    expect(c.note).toMatch(/Починить/)
    expect(c.title + c.note).not.toMatch(/подхватит|готов/)
  })

  it('несущий жив -- прежние слова', () => {
    expect(backupCopy({ backupLine: { tunnel_id: 'awg10', name: 'vpn-nl' }, carrierDown: false }).title).toBe('Запасной VPN-туннель готов')
    expect(backupCopy({ backupLine: undefined, carrierDown: true }).title).toBe('Запасного VPN-туннеля нет')
  })
})
