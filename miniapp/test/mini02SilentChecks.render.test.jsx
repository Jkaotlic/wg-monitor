// @vitest-environment jsdom
// MINI-02: список проверок молчащего роутера не говорит «работает» в
// настоящем времени. Форма ответов -- miniappRouterDetailResp и
// miniappRouterEventsResp (check_name/status/ts), роутер вымышленный.
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ router: null, checks: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouter: () => Promise.resolve(mocks.router),
  fetchRouterChecks: () => Promise.resolve(mocks.checks),
  fetchRouterVersions: () => Promise.resolve(null),
}))

vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({ busy: false, result: null, error: null, errorCode: null, run: () => Promise.resolve(null) }),
}))

const { RouterDetail } = await import('../src/screens/RouterDetail.jsx')
const { AppContext } = await import('../src/appContext.js')

const TS = '2026-09-25T10:00:00Z'

async function mount(router) {
  mocks.router = { router, incidents: [] }
  mocks.checks = {
    checks: [
      { check_name: 'agent_heartbeat', status: 'ok', ts: TS },
      { check_name: 'dns', status: 'ok', ts: TS },
      { check_name: 'awg_manager', status: 'ok', ts: TS },
    ],
    tunnels: [],
    traffic: null,
  }
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () =>
    render(
      <AppContext.Provider value={{ mode: 'web', wide: false }}>
        <RouterDetail id={router.id} openSheet={() => {}} onTab={() => {}} />
      </AppContext.Provider>,
      root,
    ),
  )
  await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
  return root
}

function rowText(root, title) {
  const row = [...root.querySelectorAll('.checks-row')].find((r) => r.textContent.includes(title))
  return row?.textContent ?? ''
}

describe('MINI-02: проверки молчащего роутера', () => {
  it('«Отчёты от роутера» не «работает», пока роутер молчит (тревога + stale)', async () => {
    const root = await mount({ id: 31, nickname: 'tihaya-dacha', kind: 'static', status: 'alert', stale: true, last_seen_age_sec: 47 * 3600 })
    const hb = rowText(root, 'Отчёты от роутера')
    expect(hb).not.toBe('')
    expect(hb).not.toContain('работает')
    // Прочие проверки -- прошлое, а не «работает сейчас».
    expect(rowText(root, 'DNS')).not.toMatch(/·\s*работает/)
    render(null, root)
    root.remove()
  })

  it('offline без тревоги -- то же самое', async () => {
    const root = await mount({ id: 32, nickname: 'tihaya-dacha', kind: 'static', status: 'offline', last_seen_age_sec: 3 * 3600 })
    // Строка про «Отчёты» на «Роутере» может не рисоваться; но всё, что
    // нарисовано, -- не в настоящем времени (проверка не пустая).
    const rows = [...root.querySelectorAll('.checks-row')]
    expect(rows.length).toBeGreaterThan(0)
    for (const r of rows) expect(r.textContent).not.toMatch(/·\s*работает/)
    render(null, root)
    root.remove()
  })

  it('живой роутер -- исправные проверки на «Роутере» не рисуются (их дом -- «Проверки»)', async () => {
    const root = await mount({ id: 33, nickname: 'zhivaya-dacha', kind: 'static', status: 'online', stale: false, last_seen_age_sec: 30 })
    expect(root.querySelectorAll('.checks-row')).toHaveLength(0)
    expect(root.querySelector('.checks-failing')).toBe(null)
    render(null, root)
    root.remove()
  })
})
