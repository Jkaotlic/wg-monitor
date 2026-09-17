import { describe, it, expect, vi, afterEach } from 'vitest'
import {
  deployBackend,
  fetchHealth,
  startProvision,
  fetchJob,
  fetchAgentConnection,
  saveAgentConnection,
  setUnauthorizedHandler,
  ApiError,
} from '../src/api.js'

function stubFetch(reply, { ok = true, status = 200 } = {}) {
  const calls = []
  vi.stubGlobal('fetch', async (url, opts = {}) => {
    calls.push({
      url,
      method: opts.method ?? 'GET',
      headers: opts.headers ?? {},
      cache: opts.cache,
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

describe('цикл 2: запросы', () => {
  it('раскатка бэкенда -- POST /v1/miniapp/backend/deploy с версией и набранным', async () => {
    const calls = stubFetch({ accepted: true, target_version: 'v0.36.0' })
    expect(await deployBackend('v0.36.0', 'v0.36.0')).toEqual({ accepted: true, target_version: 'v0.36.0' })
    expect(calls[0]).toMatchObject({
      url: '/v1/miniapp/backend/deploy',
      method: 'POST',
      body: { target_version: 'v0.36.0', confirm: 'v0.36.0' },
    })
    expect(calls[0].headers['Content-Type']).toBe('application/json')
  })

  it('здоровье -- GET /healthz вне /v1/miniapp, без кэша', async () => {
    const calls = stubFetch({ status: 'ok', version: 'v0.36.0' })
    expect(await fetchHealth()).toEqual({ status: 'ok', version: 'v0.36.0' })
    expect(calls[0]).toMatchObject({ url: '/healthz', method: 'GET', cache: 'no-store' })
  })

  it('401 от /healthz не выкидывает на экран входа', async () => {
    const seen = []
    const off = setUnauthorizedHandler(() => seen.push('401'))
    stubFetch({ code: 'unauthorized' }, { ok: false, status: 401 })
    await expect(fetchHealth()).rejects.toBeInstanceOf(ApiError)
    expect(seen).toEqual([])
    off()
  })

  it('добавление роутера -- POST /v1/miniapp/provision с телом как есть', async () => {
    const calls = stubFetch({ job_id: 'j1', nickname: 'dacha-1' })
    const body = { kind: 'register', nickname: 'dacha-1', agent_kind: 'static', confirm: 'dacha-1' }
    expect(await startProvision(body)).toEqual({ job_id: 'j1', nickname: 'dacha-1' })
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/provision', method: 'POST', body })
  })

  it('ход задания -- GET /v1/miniapp/jobs/{id}, id экранируется', async () => {
    const calls = stubFetch({ id: 'a/b', state: 'running', steps: [] })
    await fetchJob('a/b')
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/jobs/a%2Fb', method: 'GET' })
  })

  it('404 задания -- ApiError с кодом и русской фразой', async () => {
    stubFetch({ code: 'job_not_found', message: 'Задание не найдено или истекло' }, { ok: false, status: 404 })
    const err = await fetchJob('x').catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(404)
    expect(err.code).toBe('job_not_found')
    expect(err.serverMessage).toBe('Задание не найдено или истекло')
  })

  it('подключение агента -- GET и PUT (204 без тела)', async () => {
    let calls = stubFetch({ awgm_url: 'https://router.example.com' })
    expect(await fetchAgentConnection(7)).toEqual({ awgm_url: 'https://router.example.com' })
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/routers/7/agent/connection', method: 'GET' })

    calls = stubFetch(null, { status: 204 })
    expect(await saveAgentConnection(7, { ssh_port: 222 })).toBe(null)
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/routers/7/agent/connection', method: 'PUT', body: { ssh_port: 222 } })
  })
})
