import { describe, it, expect, vi, afterEach } from 'vitest'
import {
  fetchSession,
  dashboardLogin,
  redeemWebLink,
  dashboardLogout,
  setUnauthorizedHandler,
  fetchRouters,
  cancelRouterAgentRevive,
  ApiError,
} from '../src/api.js'

function stubFetch(reply, { ok = true, status = 200 } = {}) {
  const calls = []
  vi.stubGlobal('fetch', async (url, opts = {}) => {
    calls.push({
      url,
      method: opts.method ?? 'GET',
      headers: opts.headers ?? {},
      credentials: opts.credentials,
      body: opts.body ? JSON.parse(opts.body) : undefined,
    })
    return {
      ok,
      status,
      json: async () => {
        if (status === 204) throw new Error('у 204 нет тела')
        return reply
      },
    }
  })
  return calls
}

afterEach(() => vi.unstubAllGlobals())

describe('веб-вход: запросы', () => {
  it('кто я -- GET /v1/miniapp/session с кукой', async () => {
    const calls = stubFetch({ ok: true, telegram_user_id: 42, is_admin: true, via: 'web' })
    expect(await fetchSession()).toEqual({ ok: true, telegram_user_id: 42, is_admin: true, via: 'web' })
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/session', method: 'GET', credentials: 'same-origin' })
  })

  it('вход токеном -- POST /v1/dashboard/login, JSON', async () => {
    const calls = stubFetch({ ok: true })
    await dashboardLogin('secret')
    expect(calls[0]).toMatchObject({ url: '/v1/dashboard/login', method: 'POST', body: { token: 'secret' } })
    expect(calls[0].headers['Content-Type']).toBe('application/json')
  })

  it('обмен ссылки -- POST /v1/dashboard/web-link/redeem', async () => {
    const calls = stubFetch({ ok: true })
    await redeemWebLink('raw')
    expect(calls[0]).toMatchObject({ url: '/v1/dashboard/web-link/redeem', method: 'POST', body: { token: 'raw' } })
  })

  it('выход -- 204 без тела не ломает разбор', async () => {
    const calls = stubFetch(null, { status: 204 })
    expect(await dashboardLogout()).toBe(null)
    expect(calls[0]).toMatchObject({ url: '/v1/dashboard/logout', method: 'POST' })
  })

  it('отказ входа -- ApiError с кодом и фразой сервера', async () => {
    stubFetch({ code: 'rate_limited', message: 'Слишком много попыток. Попробуйте через 30 с.' }, { ok: false, status: 429 })
    const err = await dashboardLogin('x').catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(429)
    expect(err.serverMessage).toBe('Слишком много попыток. Попробуйте через 30 с.')
  })

  it('DELETE без тела тоже несёт Content-Type: application/json (иначе 415 в web)', async () => {
    const calls = stubFetch({ cleared: true })
    await cancelRouterAgentRevive(7)
    expect(calls[0].method).toBe('DELETE')
    expect(calls[0].headers['Content-Type']).toBe('application/json')
  })
})

describe('401 посреди работы', () => {
  it('обработчик зовётся на 401 запросов мини-аппа', async () => {
    stubFetch({ code: 'unauthorized' }, { ok: false, status: 401 })
    const seen = vi.fn()
    const off = setUnauthorizedHandler(seen)
    await fetchRouters().catch(() => {})
    expect(seen).toHaveBeenCalledTimes(1)
    off()
    await fetchRouters().catch(() => {})
    expect(seen).toHaveBeenCalledTimes(1)
  })

  it('не зовётся на /session и на запросы входа -- там 401 и есть ответ', async () => {
    stubFetch({ code: 'unauthorized' }, { ok: false, status: 401 })
    const seen = vi.fn()
    const off = setUnauthorizedHandler(seen)
    await fetchSession().catch(() => {})
    await dashboardLogin('x').catch(() => {})
    await redeemWebLink('x').catch(() => {})
    expect(seen).not.toHaveBeenCalled()
    off()
  })
})
