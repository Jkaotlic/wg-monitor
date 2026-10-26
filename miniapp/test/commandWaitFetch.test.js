import { describe, it, expect, vi, afterEach } from 'vitest'
import { waitCommand } from '../src/commandWait.js'

// Настоящий сбой сети -- TypeError из самого fetch (а не подставленная ошибка
// с status): isTransient обязан его узнать, и опрос продолжается.
afterEach(() => vi.unstubAllGlobals())

describe('waitCommand: настоящий TypeError от fetch', () => {
  it('временный сбой не обрывает ожидание; onPoll видит сбой, потом успех', async () => {
    const answers = [new TypeError('Failed to fetch'), { status: 'ok', output: '' }]
    const fetchMock = vi.fn(() => {
      const a = answers.shift()
      return a instanceof Error
        ? Promise.reject(a)
        : Promise.resolve(new Response(JSON.stringify(a), { status: 200, headers: { 'content-type': 'application/json' } }))
    })
    vi.stubGlobal('fetch', fetchMock)
    const polls = []
    const res = await waitCommand(7, 'c1', { sleep: () => Promise.resolve(), onPoll: (failed) => polls.push(failed) })
    expect(res).toMatchObject({ status: 'ok' })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(polls).toEqual([true, false])
  })

  it('до дедлайна одни сбои -- null, последний опрос помечен сбоем', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))))
    let t = 0
    const polls = []
    const res = await waitCommand(7, 'c1', { deadlineMs: 5_000, now: () => (t += 2_000), sleep: () => Promise.resolve(), onPoll: (f) => polls.push(f) })
    expect(res).toBe(null)
    expect(polls.length).toBeGreaterThan(0)
    expect(polls.at(-1)).toBe(true)
  })
})
