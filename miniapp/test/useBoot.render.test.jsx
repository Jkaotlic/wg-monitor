// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ routersCalls: 0, pending: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchSession: () => Promise.resolve({ ok: true, is_admin: true, via: 'web' }),
  // Первый список -- сразу, второй (пульс) -- висит, пока тест не отпустит.
  fetchRouters: () => {
    mocks.routersCalls++
    if (mocks.routersCalls === 1) return Promise.resolve({ routers: [{ id: 1, nickname: 'Дача' }] })
    return new Promise((r) => { mocks.pending = r })
  },
  dashboardLogout: () => Promise.resolve(null),
}))

const { useBoot } = await import('../src/useBoot.js')

let boot = null
function Probe() {
  boot = useBoot('web')
  return null
}

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

describe('useBoot', () => {
  it('ответ обновления списка после «Выйти» не возвращает роутеры', async () => {
    const root = document.createElement('div')
    await act(async () => render(<Probe />, root))
    await act(async () => { await boot.start() })
    expect(boot.status).toBe('ready')
    let refresh
    await act(async () => { refresh = boot.refreshRouters() })
    await act(async () => { await boot.logout() })
    expect(boot.status).toBe('login')
    await act(async () => { mocks.pending({ routers: [{ id: 1, nickname: 'Дача' }, { id: 2, nickname: 'Офис' }] }); await refresh })
    await flush()
    expect(boot.status).toBe('login')
    expect(boot.routers).toEqual([])
    render(null, root)
  })
})
