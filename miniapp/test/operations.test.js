import { describe, expect, it } from 'vitest'
import { operationView } from '../src/operations.js'

describe('operationView', () => {
  it.each([
    [{ outcome: '' }, ['Выполняется', 'warn']],
    [{ outcome: 'ok' }, ['Завершено', 'ok']],
    [{ outcome: 'failed', reason_code: 'command_timeout' }, ['Роутер не ответил вовремя', 'danger']],
    [{ outcome: 'failed', reason_code: 'command_locked' }, ['Другая операция ещё не закончилась', 'warn']],
  ])('переводит устойчивый исход %j', (operation, [status, tone]) => {
    expect(operationView({ summary: 'Перезапуск VPN-туннеля', ...operation })).toMatchObject({ status, tone, title: 'Перезапуск VPN-туннеля' })
  })

  it('не показывает технические идентификаторы как основной текст', () => {
    const view = operationView({ action: 'tunnel_restart', operation_id: 'op-secret', command_id: 'cmd-secret', outcome: 'ok' })
    expect(view.title).toBe('Операция на роутере')
    expect(JSON.stringify(view)).not.toContain('cmd-secret')
  })
})
