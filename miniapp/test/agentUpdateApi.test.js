import { describe, it, expect, vi, afterEach } from 'vitest'
import { updateRouterAgent, cancelRouterAgentUpdate, updateFleetAgents, ApiError } from '../src/api.js'

function stubFetch(reply, { ok = true, status = 202 } = {}) {
  const calls = []
  vi.stubGlobal('fetch', async (url, opts) => {
    calls.push({ url, method: opts.method, body: opts.body ? JSON.parse(opts.body) : undefined })
    return { ok, status, json: async () => reply }
  })
  return calls
}

afterEach(() => vi.unstubAllGlobals())

describe('обновление агента: запросы', () => {
  it('набранное имя уходит полем confirm, без цели версии поля нет', async () => {
    const calls = stubFetch({ queued: true, deferred: true, target_version: 'v0.33.0' })
    const resp = await updateRouterAgent(7, 'bronya')
    expect(calls[0]).toEqual({ url: '/v1/miniapp/routers/7/agent/update', method: 'POST', body: { confirm: 'bronya' } })
    expect(resp).toEqual({ queued: true, deferred: true, target_version: 'v0.33.0' })
  })

  it('цель версии уходит полем target_version', async () => {
    const calls = stubFetch({ queued: true, deferred: false, target_version: 'v0.32.0' })
    await updateRouterAgent(7, 'bronya', 'v0.32.0')
    expect(calls[0].body).toEqual({ confirm: 'bronya', target_version: 'v0.32.0' })
  })

  it('разрешение отката уходит полем allow_downgrade, только когда оно есть', async () => {
    const calls = stubFetch({ queued: true, deferred: false, target_version: 'v0.34.0' })
    await updateRouterAgent(7, 'bronya', 'v0.34.0', true)
    await updateRouterAgent(7, 'bronya', 'v0.36.0', false)
    expect(calls[0].body).toEqual({ confirm: 'bronya', target_version: 'v0.34.0', allow_downgrade: true })
    expect(calls[1].body).toEqual({ confirm: 'bronya', target_version: 'v0.36.0' })
  })

  it('отмена -- POST без тела на .../agent/update/cancel', async () => {
    const calls = stubFetch({ cleared: true }, { status: 200 })
    expect(await cancelRouterAgentUpdate(7)).toEqual({ cleared: true })
    expect(calls[0]).toEqual({ url: '/v1/miniapp/routers/7/agent/update/cancel', method: 'POST', body: undefined })
  })

  it('всем отставшим -- слово «обновить» полем confirm', async () => {
    const results = [{ router_id: 7, nickname: 'bronya', outcome: 'deferred', reason_code: '', reason_text: '' }]
    const calls = stubFetch({ results }, { status: 200 })
    expect(await updateFleetAgents('обновить')).toEqual({ results })
    expect(calls[0]).toEqual({ url: '/v1/miniapp/fleet/agent/update', method: 'POST', body: { confirm: 'обновить' } })
  })

  it('отказ приходит кодом в ApiError.code', async () => {
    stubFetch({ code: 'deploy_pending', message: 'deploy pending' }, { ok: false, status: 409 })
    const err = await updateRouterAgent(7, 'bronya').catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(409)
    expect(err.code).toBe('deploy_pending')
  })
})
