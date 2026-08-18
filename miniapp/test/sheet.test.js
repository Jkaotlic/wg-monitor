import { describe, it, expect } from 'vitest'
import { confirmSheet, sheetPhase } from '../src/sheet.js'

describe('confirmSheet', () => {
  it('несёт команду, которая уйдёт на роутер', () => {
    const s = confirmSheet({
      routerID: 1,
      title: 'Перезапустить туннель',
      body: 'Связь через туннель прервётся секунд на пятнадцать.',
      action: 'tunnel_restart',
      args: { tunnel_id: 'awg12' },
      buttonLabel: 'Перезапустить',
    })
    expect(s).toMatchObject({
      routerID: 1,
      action: 'tunnel_restart',
      args: { tunnel_id: 'awg12' },
      buttonLabel: 'Перезапустить',
    })
  })

  it('без args подставляет пустой объект, а не undefined', () => {
    expect(confirmSheet({ routerID: 1, title: 't', body: 'b', action: 'a' }).args).toEqual({})
  })

  it('запоминает, что роутер спит -- шит обещает отложенное выполнение', () => {
    expect(confirmSheet({ routerID: 1, title: 't', body: 'b', action: 'a', asleep: true }).asleep).toBe(true)
  })

  it('помечает разрушающее действие, чтобы кнопка стала красной', () => {
    expect(confirmSheet({ routerID: 1, title: 't', body: 'b', action: 'a', danger: true }).danger).toBe(true)
  })
})

describe('sheetPhase', () => {
  it('до запуска -- подтверждение', () => {
    expect(sheetPhase({})).toBe('confirm')
  })
  it('во время выполнения -- ожидание', () => {
    expect(sheetPhase({ busy: true })).toBe('running')
  })
  it('после успеха -- результат', () => {
    expect(sheetPhase({ result: { status: 'ok' } })).toBe('done')
  })
  it('ошибка важнее результата', () => {
    expect(sheetPhase({ result: { status: 'ok' }, error: 'таймаут' })).toBe('error')
  })
  it('пока идёт выполнение, старый результат не показывается', () => {
    expect(sheetPhase({ busy: true, result: { status: 'ok' } })).toBe('running')
  })
})
