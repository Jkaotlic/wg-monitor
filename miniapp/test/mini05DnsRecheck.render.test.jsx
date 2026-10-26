// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// MINI-05: «Проверить ещё раз» после сброса DNS -- отказ роутера виден под
// кнопкой, а проверки перечитываются только когда роутер согласился.
const PREVIEW = 'Предпросмотр сброса DNS — ничего не изменено\n\nУберём (1):\n  − tls upstream 8.8.8.8 sni dns.google\n'
const RESET = 'DNS reset → reference DoT\n\nснимок «до»: /opt/etc/wg-monitor/dns-before-1757600000.txt\n'

const mocks = vi.hoisted(() => ({ checksCalls: 0, answers: {}, errors: {} }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve({ role: 'admin', agent_version: 'v0.57.0' }),
  fetchRouterChecks: () => {
    mocks.checksCalls++
    return Promise.resolve({ checks: [], tunnels: [] })
  },
}))
vi.mock('../src/useCommand.js', async () => {
  const { useState } = await import('preact/hooks')
  return {
    useCommand: () => {
      const [state, setState] = useState({ busy: false, result: null, error: null, errorCode: null })
      return {
        ...state,
        run: (action) => {
          const err = mocks.errors[action] ?? null
          const res = err ? null : (mocks.answers[action] ?? null)
          setState({ busy: false, result: res, error: err, errorCode: null })
          return Promise.resolve(res)
        },
      }
    },
  }
})

const { DNSResetScreen } = await import('../src/screens/DNSResetScreen.jsx')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent === text)

async function afterReset() {
  let sheet = null
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<DNSResetScreen routerID={2} routerName="home" openSheet={(s) => (sheet = s)} onClose={() => {}} />, root))
  await flush()
  await act(async () => button(root, 'Посмотреть, что изменится').click())
  await flush()
  await act(async () => button(root, 'Сбросить DNS').click())
  await act(async () => sheet.onResult({ status: 'ok', output: RESET }))
  await flush()
  return root
}

beforeEach(() => {
  mocks.checksCalls = 0
  mocks.errors = {}
  mocks.answers = { dns_reset: { status: 'ok', output: PREVIEW } }
})

describe('MINI-05: повторная проверка после сброса DNS', () => {
  it('отказ роутера (не-ok) виден под кнопкой, проверки не перечитываются', async () => {
    mocks.answers.force_recheck = { status: 'timeout', output: 'awgmgr GET /api/x: HTTP 500' }
    const root = await afterReset()
    const before = mocks.checksCalls
    await act(async () => button(root, 'Проверить ещё раз').click())
    await flush()
    expect(root.textContent).toContain('Роутер не выполнил повторную проверку — отчёт не обновлён.')
    expect(root.textContent).not.toMatch(/HTTP 500/)
    expect(mocks.checksCalls).toBe(before)
    render(null, root)
  })

  it('ошибка команды видна словами', async () => {
    mocks.errors.force_recheck = 'Сервер не ответил — проверьте связь и повторите.'
    const root = await afterReset()
    expect(root.textContent).toContain('Сервер не ответил — проверьте связь и повторите.')
    expect(mocks.checksCalls).toBe(1)
    render(null, root)
  })

  it('ok -- проверки перечитываются, отказа нет', async () => {
    mocks.answers.force_recheck = { status: 'ok', output: '' }
    const root = await afterReset()
    const before = mocks.checksCalls
    await act(async () => button(root, 'Проверить ещё раз').click())
    await flush()
    expect(mocks.checksCalls).toBe(before + 1)
    expect(root.textContent).not.toContain('отчёт не обновлён')
    render(null, root)
  })
})
