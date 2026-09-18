import { describe, it, expect, vi, afterEach } from 'vitest'
import { reviveRouterAgent, cancelRouterAgentRevive, forgetRouterCredentials, ApiError } from '../src/api.js'

function stubFetch(reply, { ok = true, status = 202 } = {}) {
  const calls = []
  vi.stubGlobal('fetch', async (url, opts) => {
    calls.push({ url, method: opts.method, body: opts.body ? JSON.parse(opts.body) : undefined })
    return { ok, status, json: async () => reply }
  })
  return calls
}

afterEach(() => vi.unstubAllGlobals())

describe('оживление агента: запросы', () => {
  it('постановка -- POST на .../agent/revive с телом как есть', async () => {
    const calls = stubFetch({ status: 'waiting', expires_at: '2026-10-15T12:00:00Z' })
    const body = { confirm: 'bronya', expires_days: 30, root_password: 'p' }
    expect(await reviveRouterAgent(7, body)).toEqual({ status: 'waiting', expires_at: '2026-10-15T12:00:00Z' })
    expect(calls[0]).toEqual({ url: '/v1/miniapp/routers/7/agent/revive', method: 'POST', body })
  })

  it('отмена -- DELETE без тела', async () => {
    const calls = stubFetch({ cleared: true }, { status: 200 })
    expect(await cancelRouterAgentRevive(7)).toEqual({ cleared: true })
    expect(calls[0]).toEqual({ url: '/v1/miniapp/routers/7/agent/revive', method: 'DELETE', body: undefined })
  })

  it('«Забыть пароль» -- DELETE на .../credentials без тела', async () => {
    const calls = stubFetch({ cleared: true, revive_cancelled: false }, { status: 200 })
    expect(await forgetRouterCredentials(7)).toEqual({ cleared: true, revive_cancelled: false })
    expect(calls[0]).toEqual({ url: '/v1/miniapp/routers/7/credentials', method: 'DELETE', body: undefined })
  })

  it('отказ -- код в ApiError.code, русская фраза в serverMessage', async () => {
    stubFetch({ code: 'agent_alive', error: 'agent_alive', message: 'Агент на роутере отвечает — оживлять нечего.' }, { ok: false, status: 409 })
    const err = await reviveRouterAgent(7, { confirm: 'bronya' }).catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.code).toBe('agent_alive')
    expect(err.serverMessage).toBe('Агент на роутере отвечает — оживлять нечего.')
  })
})
