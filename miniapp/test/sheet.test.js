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

  // Подпись занятости у разных локальных действий разная: «Ставим…» для
  // обновления агента не то же самое, что «Сохраняем…» для настройки. Экран
  // задаёт её описанием листа, а не Sheet.jsx угадывает по действию.
  it('подпись занятости -- из описания листа, по умолчанию прежняя', () => {
    const withLabel = localSheet({ title: 't', body: 'b', busyLabel: 'Ставим…', perform: () => Promise.resolve() })
    expect(withLabel.busyLabel).toBe('Ставим…')
    const withoutLabel = localSheet({ title: 't', body: 'b', perform: () => Promise.resolve() })
    expect(withoutLabel.busyLabel).toBe('')
  })
})

describe('confirmReady: строгий набор', () => {
  it('confirmStrict -- только пробелы по краям', async () => {
    const { confirmReady } = await import('../src/sheet.js')
    const sheet = { confirmPhrase: 'v0.36.0', confirmStrict: true }
    expect(confirmReady(sheet, ' v0.36.0 ')).toBe(true)
    expect(confirmReady(sheet, 'V0.36.0')).toBe(false)
    expect(confirmReady({ confirmPhrase: 'dacha-1', confirmStrict: true }, 'dacha‑1')).toBe(false)
  })
})
