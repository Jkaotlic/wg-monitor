import { describe, it, expect } from 'vitest'
import { ApiError, COMMAND_GONE_TEXT } from '../src/api.js'
import { errorText, FALLBACK_ERROR_TEXT, OFFLINE_ERROR_TEXT } from '../src/errorText.js'

// v0.50, спека п. 1.4: одна функция «ошибка словами». err.message у ApiError --
// «/routers/7/commands failed: 400», путь и число; на экран он не выходит.
describe('errorText', () => {
  it('пусто -- пусто', () => {
    expect(errorText(null)).toBe('')
    expect(errorText(undefined)).toBe('')
  })

  it('код с общей фразой -- фраза', () => {
    const err = new ApiError(409, 'agent_too_old', '/routers/7/commands failed: 409')
    expect(errorText(err)).toBe('Эта кнопка заработает после обновления агента на роутере.')
  })

  it('своя карта экрана перекрывает общую', () => {
    const err = new ApiError(409, 'agent_too_old', 'x')
    expect(errorText(err, { agent_too_old: 'Своя фраза экрана.' })).toBe('Своя фраза экрана.')
  })

  it('русская фраза сервера -- как есть', () => {
    const err = new ApiError(503, 'not_configured', 'x failed: 503', 'У сервера не настроена база данных.')
    expect(errorText(err)).toBe('У сервера не настроена база данных.')
  })

  it('английская фраза сервера на экран не выходит', () => {
    const err = new ApiError(403, 'forbidden', '/routers/7 failed: 403', 'sign in required')
    expect(errorText(err)).toBe(FALLBACK_ERROR_TEXT)
  })

  it('401 -- сессия истекла', () => {
    const err = new ApiError(401, 'unauthorized', '/routers failed: 401', 'sign in required')
    expect(errorText(err)).toBe('Сессия истекла — откройте приложение заново.')
  })

  it('ни кода, ни фразы (502 с HTML-телом) -- запасная фраза без пути', () => {
    const text = errorText(new ApiError(502, 'unknown', '/routers/7/commands failed: 502'))
    expect(text).toBe(FALLBACK_ERROR_TEXT)
    expect(text).not.toMatch(/failed|\/routers|502/)
  })

  it('обрыв сети (TypeError без статуса) -- про связь', () => {
    expect(errorText(new TypeError('Failed to fetch'))).toBe(OFFLINE_ERROR_TEXT)
  })

  it('«итог команды недоступен» из api.js сохраняется', () => {
    expect(errorText(new ApiError(404, 'not_found', COMMAND_GONE_TEXT))).toBe(COMMAND_GONE_TEXT)
  })

  it('готовая русская строка проходит, английская -- нет', () => {
    expect(errorText('Не дождались ответа за отведённое время.')).toBe('Не дождались ответа за отведённое время.')
    expect(errorText('/routers/7/commands failed: 400')).toBe(FALLBACK_ERROR_TEXT)
  })
})
