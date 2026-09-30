import { describe, it, expect } from 'vitest'
import { agentReplyText, errorText, FALLBACK_ERROR_TEXT } from '../src/errorText.js'
import { ApiError } from '../src/api.js'

const FB = 'Роутер не переспросил — попробуйте ещё раз через минуту.'

// Ответ агента (result.output) -- английский технический текст и голые статусы
// («timeout») на экран не выходят: фраза экрана, а русский вывод агента -- после неё.
describe('agentReplyText', () => {
  it('английский вывод -- только запасная фраза', () => {
    expect(agentReplyText({ status: 'error', output: 'awgmgr GET /api/x: HTTP 500' }, FB)).toBe(FB)
  })
  it('русский вывод -- дописывается после фразы', () => {
    expect(agentReplyText({ status: 'error', output: 'Служба не отвечает.' }, FB)).toBe(`${FB} Служба не отвечает.`)
  })
  it('пусто и голый статус -- запасная фраза', () => {
    expect(agentReplyText({ status: 'timeout', output: '' }, FB)).toBe(FB)
    expect(agentReplyText({ status: 'timeout' }, FB)).toBe(FB)
    expect(agentReplyText(null, FB)).toBe(FB)
  })
})

// Русским считается текст, где кириллицы >= 70% букв и нет путей/протоколов.
describe('agentReplyText: русский только целиком', () => {
  it('чистый русский -- сохраняется', () => {
    expect(agentReplyText({ output: 'Служба не отвечает, попробуйте позже.' }, FB)).toBe(`${FB} Служба не отвечает, попробуйте позже.`)
  })
  it('смесь с путём и HTTP -- только фраза экрана', () => {
    expect(agentReplyText({ output: 'awgmgr GET /api/x: HTTP 500 (роутер недоступен)' }, FB)).toBe(FB)
  })
  it('русское слово в английском -- только фраза', () => {
    expect(agentReplyText({ output: 'connection refused by peer (отказ)' }, FB)).toBe(FB)
  })
  it('русский с протоколом или Error: -- только фраза', () => {
    expect(agentReplyText({ output: 'Не удалось открыть https://example.com/x' }, FB)).toBe(FB)
    expect(agentReplyText({ output: 'Error: не удалось выполнить команду' }, FB)).toBe(FB)
  })
  it('английский и пусто -- фраза', () => {
    expect(agentReplyText({ output: 'timeout' }, FB)).toBe(FB)
    expect(agentReplyText({ output: '  ' }, FB)).toBe(FB)
  })
  it('errorText: та же строгость для serverMessage и готовых строк', () => {
    expect(errorText(new ApiError(500, 'unknown', 'x', 'Ошибка: GET /api/x вернул HTTP 500'))).toBe(FALLBACK_ERROR_TEXT)
    expect(errorText(new ApiError(500, 'unknown', 'x', 'У сервера не настроена база данных.'))).toBe('У сервера не настроена база данных.')
    expect(errorText('awgmgr GET /api/x: HTTP 500 (роутер недоступен)')).toBe(FALLBACK_ERROR_TEXT)
  })
})
