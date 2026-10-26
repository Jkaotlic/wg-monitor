// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// MINI-04: «Убрать» правило -- роутер не показал план удаления: экран говорит
// это словами, с повтором, правило не тронуто.
const mocks = vi.hoisted(() => ({ calls: [], answers: {}, errors: {} }))

vi.mock('../src/useCommand.js', async () => {
  const { useState } = await import('preact/hooks')
  return {
    useCommand: () => {
      const [state, setState] = useState({ busy: false, result: null, error: null, errorCode: null, sleepNote: '' })
      return {
        ...state,
        run: (action, args) => {
          mocks.calls.push({ action, args })
          const err = mocks.errors[action] ?? null
          const res = err ? null : (mocks.answers[action] ?? null)
          setState({ busy: false, result: res, error: err, errorCode: null, sleepNote: '' })
          return Promise.resolve(res)
        },
      }
    },
  }
})
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve({ role: 'owner' }),
}))

const { RoutesTab } = await import('../src/screens/RoutesTab.jsx')

const SNAP = {
  policy_model: true,
  default_egress: 'direct',
  tunnels: [{ id: 'nwg1', name: 'amsterdam', iface: 'nwg1', type: 'managed', status: 'running' }],
  counts: { nwg1: { dns: 1, static: 0 } },
  other: { dns: 0, static: 0 },
  policies: [],
  rules: [{ id: 'dns-1', kind: 'dns', bind: 'nwg1', targets: ['example.com'] }],
}
const PLAN_OK = { status: 'ok', output: JSON.stringify({ rules: [], hash: 'h1' }) }

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const byText = (el, text) => [...el.querySelectorAll('button')].find((b) => b.textContent.trim() === text)

async function mount() {
  const root = document.createElement('div')
  document.body.appendChild(root)
  const seen = { sheets: [] }
  await act(async () => render(
    <RoutesTab routerID={7} asleep={false} openSheet={(s) => seen.sheets.push(s)} layer="routes" layerParams={{}} openLayer={() => {}} closeLayer={() => {}} />, root))
  await flush()
  await flush()
  return { root, seen }
}

beforeEach(() => {
  mocks.calls = []
  mocks.errors = {}
  mocks.answers = { route_status: { status: 'ok', output: JSON.stringify(SNAP) }, route_delete_plan: PLAN_OK }
})

describe('MINI-04: отказ в плане удаления виден', () => {
  it('не-ok без русского вывода -- фраза экрана и повтор, лист не открыт', async () => {
    mocks.answers.route_delete_plan = { status: 'timeout', output: 'awgmgr GET /api/x: HTTP 500' }
    const { root, seen } = await mount()
    await act(async () => byText(root, 'Убрать').click())
    await flush()
    expect(root.textContent).toContain('Роутер не показал, что исчезнет, — правило не тронуто.')
    expect(root.textContent).not.toMatch(/HTTP 500/)
    expect(seen.sheets).toHaveLength(0)
    expect(byText(root, 'Повторить')).toBeTruthy()
  })

  it('русская фраза агента сохраняется после фразы экрана', async () => {
    mocks.answers.route_delete_plan = { status: 'error', output: 'Правило уже удалено.' }
    const { root } = await mount()
    await act(async () => byText(root, 'Убрать').click())
    await flush()
    expect(root.textContent).toContain('Роутер не показал, что исчезнет, — правило не тронуто. Правило уже удалено.')
  })

  it('ошибка команды -- фраза ошибки; «Повторить» запрашивает план заново и открывает лист', async () => {
    mocks.errors.route_delete_plan = 'Сервер не ответил — проверьте связь и повторите.'
    const { root, seen } = await mount()
    await act(async () => byText(root, 'Убрать').click())
    await flush()
    expect(root.textContent).toContain('Сервер не ответил — проверьте связь и повторите.')
    mocks.errors = {}
    await act(async () => byText(root, 'Повторить').click())
    await flush()
    expect(mocks.calls.filter((c) => c.action === 'route_delete_plan')).toHaveLength(2)
    expect(seen.sheets).toHaveLength(1)
    expect(seen.sheets[0]).toMatchObject({ action: 'route_delete', args: { kind: 'dns', route_id: 'dns-1' } })
    expect(root.textContent).not.toContain('правило не тронуто')
  })
})
