import { describe, it, expect } from 'vitest'
import { confirmSheet, localSheet, confirmReady } from '../src/sheet.js'

describe('localSheet', () => {
  // Выключение уведомлений человек делает СЕБЕ: это вызов бэкенда, а не
  // команда роутеру. Шит обязан отличать одно от другого, иначе он покажет
  // строку «команда: undefined» и уйдёт ждать ответа, которого не будет.
  it('несёт локальное действие, а не команду роутеру', () => {
    const sheet = localSheet({
      title: 'Выключить уведомления?',
      body: 'Бот замолчит.',
      buttonLabel: 'Выключить',
      danger: true,
      perform: () => Promise.resolve(),
    })
    expect(sheet.action).toBeUndefined()
    expect(typeof sheet.perform).toBe('function')
    expect(sheet.danger).toBe(true)
  })

  it('без набора подтверждения готов сразу', () => {
    const sheet = localSheet({ title: 't', body: 'b', perform: () => {} })
    expect(confirmReady(sheet, '')).toBe(true)
  })

  // Команда роутеру, наоборот, обязана нести action -- иначе шит запустит
  // пустую команду.
  it('командный шит остаётся с action и без perform', () => {
    const sheet = confirmSheet({ routerID: 1, title: 't', body: 'b', action: 'diag_now' })
    expect(sheet.action).toBe('diag_now')
    expect(sheet.perform).toBeUndefined()
  })

  it('локальный лист может требовать набор имени', () => {
    const sheet = localSheet({ title: 't', body: 'b', confirmPhrase: 'bronya', perform: () => Promise.resolve() })
    expect(sheet.confirmPhrase).toBe('bronya')
    expect(confirmReady(sheet, '')).toBe(false)
    expect(confirmReady(sheet, ' BRONYA ')).toBe(true)
  })

  it('текст отказа по коду экран передаёт функцией', () => {
    const errorText = (err) => (err?.code === 'deploy_pending' ? 'уже ждёт' : '')
    const sheet = localSheet({ title: 't', body: 'b', errorText, perform: () => Promise.resolve() })
    expect(sheet.errorText({ code: 'deploy_pending' })).toBe('уже ждёт')
  })
})
