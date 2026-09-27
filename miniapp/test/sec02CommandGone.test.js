import { describe, it, expect, vi, afterEach } from 'vitest'
import { fetchCommandResult, COMMAND_GONE_TEXT } from '../src/api.js'

// SEC-02 (бэкенд v0.46): опрос неизвестного cmd_id без прав -- сразу 404
// {code:"not_found"}, а не result_not_ready. Опрос обязан остановиться и
// сказать это словами, а не «/routers/1/commands/x failed: 404».
function stubFetch(status, body) {
  globalThis.fetch = vi.fn(() => Promise.resolve({ ok: status < 400, status, json: () => Promise.resolve(body) }))
}
const saved = globalThis.fetch
afterEach(() => { globalThis.fetch = saved })

describe('SEC-02: результат команды недоступен', () => {
  it('404 result_not_ready -- «ещё не готово», опрос продолжается (null)', async () => {
    stubFetch(404, { code: 'result_not_ready', message: 'not ready' })
    expect(await fetchCommandResult(1, 'c-1', 1)).toBe(null)
  })
  it('404 not_found -- ошибка с честной фразой, опрос останавливается', async () => {
    stubFetch(404, { code: 'not_found', message: 'router not found' })
    const err = await fetchCommandResult(1, 'c-1', 1).catch((e) => e)
    expect(err).toBeInstanceOf(Error)
    expect(err.code).toBe('not_found')
    expect(err.message).toBe(COMMAND_GONE_TEXT)
    expect(err.message).not.toMatch(/failed: 404/)
  })
  it('старое поле error вместо code -- то же самое', async () => {
    stubFetch(404, { error: 'not_found' })
    const err = await fetchCommandResult(1, 'c-1', 1).catch((e) => e)
    expect(err.message).toBe(COMMAND_GONE_TEXT)
  })
})
