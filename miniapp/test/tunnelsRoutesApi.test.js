import { describe, it, expect, vi, afterEach } from 'vitest'
import { deleteTunnel, previewTunnelImport, fetchTunnelImport, confirmTunnelImport, ApiError } from '../src/api.js'

function stubFetch(reply, { ok = true, status = 200 } = {}) {
  const calls = []
  vi.stubGlobal('fetch', async (url, opts = {}) => {
    calls.push({ url, method: opts.method ?? 'GET', body: opts.body ? JSON.parse(opts.body) : undefined })
    return {
      ok,
      status,
      json: async () => {
        if (reply instanceof Error) throw reply
        return reply
      },
    }
  })
  return calls
}

afterEach(() => vi.unstubAllGlobals())

// Цикл 4 «бот без слеш-команд»: удаление VPN-туннеля и загрузка своего .conf.
// Формы ответов -- раздел «Контракт для фронтенда» плана части 1.
describe('VPN-туннели: запросы', () => {
  it('удаление -- POST с набранным именем, id в пути экранирован', async () => {
    const calls = stubFetch({ state: 'queued', cmd_id: 'c1' }, { status: 202 })
    expect(await deleteTunnel(7, 'nwg/1', 'spare')).toEqual({ state: 'queued', cmd_id: 'c1' })
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/routers/7/tunnels/nwg%2F1/delete', method: 'POST', body: { confirm: 'spare' } })
  })

  it('отказ несёт тело ответа целиком: разбивка правил', async () => {
    const body = { code: 'tunnel_has_rules', error: 'tunnel_has_rules', message: 'На VPN-туннеле 3 правила', rules: { total: 3, dns: 2, static: 1, hr_neo: 0, via_policy: 0 } }
    stubFetch(body, { ok: false, status: 409 })
    const err = await deleteTunnel(7, 'nwg1', 'amsterdam').catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(409)
    expect(err.code).toBe('tunnel_has_rules')
    expect(err.serverMessage).toBe('На VPN-туннеле 3 правила')
    expect(err.data).toEqual(body)
  })

  it('ошибка без JSON-тела -- data пустое, код unknown', async () => {
    stubFetch(new Error('not json'), { ok: false, status: 502 })
    const err = await confirmTunnelImport(7, 't1').catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.code).toBe('unknown')
    expect(err.data).toBe(null)
  })

  it('импорт: предпросмотр, опрос и подтверждение, конфиг только в теле', async () => {
    let calls = stubFetch({ token: 't1', state: 'ready', preview: {}, analyzed: true, can_confirm: true })
    await previewTunnelImport(7, { name: 'amsterdam', confB64: 'W0ludGVyZmFjZV0K' })
    expect(calls[0]).toMatchObject({
      url: '/v1/miniapp/routers/7/tunnels/import',
      method: 'POST',
      body: { name: 'amsterdam', conf_b64: 'W0ludGVyZmFjZV0K' },
    })
    expect(calls[0].url).not.toContain('W0lud')

    calls = stubFetch({ token: 't/1', state: 'analyzing' })
    expect(await fetchTunnelImport(7, 't/1')).toEqual({ token: 't/1', state: 'analyzing' })
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/routers/7/tunnels/import/t%2F1', method: 'GET', body: undefined })

    calls = stubFetch({ state: 'queued', cmd_id: 'c2', tunnel_name: 'amsterdam' }, { status: 202 })
    expect(await confirmTunnelImport(7, 't1')).toEqual({ state: 'queued', cmd_id: 'c2', tunnel_name: 'amsterdam' })
    expect(calls[0]).toMatchObject({ url: '/v1/miniapp/routers/7/tunnels/import/confirm', method: 'POST', body: { token: 't1' } })
  })
})
