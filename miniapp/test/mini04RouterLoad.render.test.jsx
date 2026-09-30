// @vitest-environment jsdom
// MINI-04: одна ошибка загрузки не ломает экран роутера навсегда, а поздний
// ответ по прошлому роутеру не рисуется поверх нового. Форма ответов --
// miniappRouterDetailResp / miniappRouterEventsResp, имена вымышленные.
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { OFFLINE_ERROR_TEXT } from '../src/errorText.js'

const mocks = vi.hoisted(() => ({ routerImpl: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouter: (id) => mocks.routerImpl(id),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [], traffic: null }),
  fetchRouterVersions: () => Promise.resolve(null),
}))
vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({ busy: false, result: null, error: null, errorCode: null, run: () => Promise.resolve(null) }),
}))

const { RouterDetail } = await import('../src/screens/RouterDetail.jsx')
const { AppContext } = await import('../src/appContext.js')

const NAMES = { 1: 'lesnaya', 2: 'polevaya' }
const detail = (id) => ({ router: { id, nickname: NAMES[id], kind: 'static', status: 'online', stale: false, last_seen_age_sec: 20 }, incidents: [] })
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

function mount(id, root) {
  return act(async () =>
    render(
      <AppContext.Provider value={{ mode: 'web', wide: false }}>
        <RouterDetail id={id} openSheet={() => {}} onTab={() => {}} />
      </AppContext.Provider>,
      root,
    ),
  )
}

describe('MINI-04', () => {
  it('после ошибки следующая удачная загрузка возвращает экран', async () => {
    let fail = true
    mocks.routerImpl = (id) => (fail ? Promise.reject(new Error('сервер не ответил')) : Promise.resolve(detail(id)))
    const root = document.createElement('div')
    document.body.appendChild(root)
    await mount(1, root)
    await flush()
    // v0.50: ошибка словами (errorText), не err.message.
    expect(root.textContent).toContain('Сервер не ответил')
    fail = false
    // Возврат к вкладке -- тот же повод перезагрузки, что и такт пульса.
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    await flush()
    expect(root.querySelector('.hero h1')?.textContent).toBe('lesnaya')
    expect(root.textContent).not.toContain(OFFLINE_ERROR_TEXT)
    render(null, root)
    root.remove()
  })

  it('поздний ответ по прошлому роутеру выбрасывается', async () => {
    const pending = {}
    mocks.routerImpl = (id) => new Promise((resolve) => { pending[id] = () => resolve(detail(id)) })
    const root = document.createElement('div')
    document.body.appendChild(root)
    await mount(1, root)
    await mount(2, root)
    await act(async () => { pending[2]() })
    await flush()
    await act(async () => { pending[1]() })
    await flush()
    expect(root.querySelector('.hero h1')?.textContent).toBe('polevaya')
    render(null, root)
    root.remove()
  })
})
