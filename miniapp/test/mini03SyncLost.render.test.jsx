// @vitest-environment jsdom
// MINI-03: сбой опроса списка роутеров не замораживает экран молча. После
// неудачного обновления видно «нет связи с сервером, данные на ЧЧ:ММ»;
// удачное обновление метку снимает.
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ calls: 0, fail: false }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchSession: () => Promise.resolve({ ok: true, is_admin: false, via: 'web' }),
  fetchRouters: () => {
    mocks.calls++
    if (mocks.fail) return Promise.reject(Object.assign(new Error('network'), { code: 'network' }))
    return Promise.resolve({ routers: [{ id: 1, nickname: 'vymysel', status: 'online', last_seen_age_sec: 20 }] })
  },
}))

const { useBoot } = await import('../src/useBoot.js')
const { syncLostText } = await import('../src/pulse.js')

let boot = null
function Probe() {
  boot = useBoot('web')
  return null
}

describe('MINI-03: сбой опроса списка виден', () => {
  it('текст метки называет время последних данных по местным часам', () => {
    const at = new Date(2026, 8, 27, 9, 5, 0)
    expect(syncLostText(at)).toBe('нет связи с сервером, данные на 09:05')
    expect(syncLostText(null)).toBe('нет связи с сервером')
  })

  it('неудачное обновление ставит метку, удачное -- снимает', async () => {
    mocks.calls = 0
    mocks.fail = false
    const root = document.createElement('div')
    await act(async () => render(<Probe />, root))
    await act(async () => { await boot.start() })
    expect(boot.syncLost).toBe(false)
    expect(boot.routersAt).toBeInstanceOf(Date)
    const firstAt = boot.routersAt

    mocks.fail = true
    await act(async () => { await boot.refreshRouters() })
    expect(boot.syncLost).toBe(true)
    // Список остаётся прежним, время данных -- прежним.
    expect(boot.routers).toHaveLength(1)
    expect(boot.routersAt).toBe(firstAt)

    mocks.fail = false
    await act(async () => { await boot.refreshRouters() })
    expect(boot.syncLost).toBe(false)
    render(null, root)
  })
})
