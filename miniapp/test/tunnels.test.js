import { describe, it, expect } from 'vitest'
import { tunnelHealth } from '../src/screens/tunnelHealth.js'

// Форма -- как у miniappTunnel (miniapp_tunnels.go): enabled и run_state
// приходят с роутера, и именно по ним считается "работает".
const alive = { tunnel_id: 'awg12', name: 'Amsterdam', status: 'ok', enabled: true, run_state: 'running', handshake_age_sec: 45, ping_check_status: 'ok' }
const dead = { tunnel_id: 'awg7', name: 'Reserve', status: 'fail', enabled: false, run_state: 'stopped' }

describe('tunnelHealth', () => {
  it('считает живые из общего числа', () => {
    expect(tunnelHealth([alive, alive, dead])).toMatchObject({ alive: 2, total: 3 })
  })
  it('даёт подпись для заголовка секции', () => {
    expect(tunnelHealth([alive, dead]).label).toBe('1 из 2')
  })
  it('пустой список не притворяется здоровым', () => {
    expect(tunnelHealth([])).toMatchObject({ alive: 0, total: 0, label: 'нет данных' })
  })
  it('без аргумента не падает', () => {
    expect(tunnelHealth().label).toBe('нет данных')
  })
})
