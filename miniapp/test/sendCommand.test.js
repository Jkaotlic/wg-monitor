import { describe, it, expect, vi, afterEach } from 'vitest'
import { sendCommand } from '../src/api.js'

function stubFetch(reply) {
  const calls = []
  vi.stubGlobal('fetch', async (url, opts) => {
    calls.push({ url, body: JSON.parse(opts.body) })
    return { ok: true, json: async () => reply }
  })
  return calls
}

afterEach(() => vi.unstubAllGlobals())

describe('sendCommand', () => {
  it('без набранного имени поля confirm нет', async () => {
    const calls = stubFetch({ cmd_id: 'c1' })
    await sendCommand(2, 'opkg_upgrade', {})
    expect(calls[0].url).toBe('/v1/miniapp/routers/2/commands')
    expect(calls[0].body).toEqual({ action: 'opkg_upgrade', args: {} })
  })

  it('набранное имя уходит полем confirm', async () => {
    const calls = stubFetch({ cmd_id: 'c1' })
    await sendCommand(2, 'service_restart', { name: 'router' }, 'home')
    expect(calls[0].body).toEqual({ action: 'service_restart', args: { name: 'router' }, confirm: 'home' })
  })

  it('ответ несёт признак сна как есть', async () => {
    stubFetch({ cmd_id: 'c1', router_asleep: true, wake_window_min: 10 })
    expect(await sendCommand(2, 'hrneo_update', {})).toEqual({ cmd_id: 'c1', router_asleep: true, wake_window_min: 10 })
  })
})
