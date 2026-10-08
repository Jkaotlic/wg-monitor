import { describe, it, expect, vi, afterEach } from 'vitest'
import { fetchPeople, ApiError } from '../src/api.js'

function stubFetch(reply, status = 200) {
  const calls = []
  vi.stubGlobal('fetch', async (url, opts = {}) => {
    calls.push({ url, method: opts.method ?? 'GET' })
    return { ok: status < 400, status, json: async () => reply }
  })
  return calls
}

afterEach(() => vi.unstubAllGlobals())

describe('fetchPeople', () => {
  it('GET /v1/miniapp/people, отдаёт массив людей', async () => {
    const people = [{ telegram_user_id: 5, name: 'Тест Тестов', username: '', last_seen_at: null, is_admin: false, routers: [] }]
    const calls = stubFetch({ people })
    expect(await fetchPeople()).toEqual(people)
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/people', method: 'GET' })
  })
  it('тело без people -- пустой список', async () => {
    stubFetch({})
    expect(await fetchPeople()).toEqual([])
  })
  it.each([
    [404, 'старый бэкенд'],
    [403, 'не админ'],
    [401, 'нет сессии'],
  ])('%i (%s) -- null', async (status) => {
    stubFetch({ code: 'x' }, status)
    expect(await fetchPeople()).toBe(null)
  })
  it('прочие ошибки -- наверх', async () => {
    stubFetch({ code: 'internal' }, 500)
    await expect(fetchPeople()).rejects.toBeInstanceOf(ApiError)
  })
})
