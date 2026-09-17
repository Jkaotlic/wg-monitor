import { describe, it, expect, vi, afterEach } from 'vitest'
import { reinstallRouterAgent, repointRouterAgent } from '../src/api.js'

function stubFetch(reply, { ok = true, status = 202 } = {}) {
  const calls = []
  vi.stubGlobal('fetch', async (url, opts) => {
    calls.push({ url, method: opts.method, body: JSON.parse(opts.body), type: opts.headers['Content-Type'] })
    return { ok, status, json: async () => reply }
  })
  return calls
}

afterEach(() => vi.unstubAllGlobals())

describe('запуск переустановки и перенаправления', () => {
  it('переустановка -- POST …/agent/reinstall, ответ с job_id', async () => {
    const calls = stubFetch({ job_id: 'job-1' })
    const body = { root_password: 'pw', awgm_login: '', awgm_password: '', awgm_api_key: '', version: '', confirm: 'home' }
    expect(await reinstallRouterAgent(22, body)).toEqual({ job_id: 'job-1' })
    expect(calls).toEqual([{ url: '/v1/miniapp/routers/22/agent/reinstall', method: 'POST', body, type: 'application/json' }])
  })

  it('перенаправление -- POST …/agent/repoint', async () => {
    const calls = stubFetch({ job_id: 'job-2' })
    const body = { root_password: 'pw', new_backend_url: '', awgm_login: '', awgm_password: '', awgm_api_key: '', confirm: 'home' }
    expect(await repointRouterAgent(22, body)).toEqual({ job_id: 'job-2' })
    expect(calls[0].url).toBe('/v1/miniapp/routers/22/agent/repoint')
    expect(calls[0].body).toEqual(body)
  })
})
