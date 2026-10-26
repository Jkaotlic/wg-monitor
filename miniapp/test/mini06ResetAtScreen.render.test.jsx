// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// MINI-06 на экране: момент сброса берётся из имени снимка (часы роутера), а не
// с телефона. Часы телефона (реальные, 2026) уходят вперёд часов роутера
// (метки 2025) -- строки всё равно становятся свежими.
const PREVIEW = 'Предпросмотр сброса DNS — ничего не изменено\n\nУберём (1):\n  − tls upstream 8.8.8.8 sni dns.google\n'
const RESET = 'DNS reset → reference DoT\n\nснимок «до»: /opt/etc/wg-monitor/dns-before-1757600000.txt\n'
const RESET_NO_SNAP = 'DNS reset → reference DoT\n'

const split = (ts) => ({ check_name: 'dns_split', ts, details: { checked_at: ts, zones: { ru: 'yandex_dot' }, resolves: 'ok' } })
const mocks = vi.hoisted(() => ({ calls: 0, after: [] }))
const BEFORE = [{ check_name: 'dns_split', ts: '2025-09-11T14:00:00Z', details: { checked_at: '2025-09-11T14:00:00Z', zones: { ru: 'other' }, resolves: 'ok' } }]

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve({ role: 'admin', agent_version: 'v0.57.0' }),
  fetchRouterChecks: () => Promise.resolve({ checks: mocks.calls++ === 0 ? BEFORE : mocks.after, tunnels: [] }),
}))
vi.mock('../src/useCommand.js', async () => {
  const { useState } = await import('preact/hooks')
  return {
    useCommand: () => {
      const [state, setState] = useState({ busy: false, result: null, error: null, errorCode: null })
      return {
        ...state,
        run: (action) => {
          const res = action === 'force_recheck' ? { status: 'ok', output: '' } : { status: 'ok', output: PREVIEW }
          setState({ busy: false, result: res, error: null, errorCode: null })
          return Promise.resolve(res)
        },
      }
    },
  }
})
const { DNSResetScreen } = await import('../src/screens/DNSResetScreen.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent === text)

async function afterReset(resetOutput) {
  let sheet = null
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<DNSResetScreen routerID={2} routerName="home" openSheet={(s) => (sheet = s)} onClose={() => {}} />, root))
  await flush()
  await act(async () => button(root, 'Посмотреть, что изменится').click())
  await flush()
  await act(async () => button(root, 'Сбросить DNS').click())
  await act(async () => sheet.onResult({ status: 'ok', output: resetOutput }))
  await flush()
  return root
}
const values = (root) => [...root.querySelectorAll('.card-rows .data-row')].map((r) => r.textContent)

beforeEach(() => { mocks.calls = 0 })

describe('MINI-06: resetAt доходит до строк «после сброса»', () => {
  it('телефон сильно впереди роутера -- отчёт новее снимка всё равно свежий', async () => {
    mocks.after = [split('2025-09-11T14:14:20Z')]
    const root = await afterReset(RESET)
    expect(root.textContent).not.toContain('ждём отчёта')
    expect(root.textContent).toContain('1 из 1')
    render(null, root)
  })

  it('отчёт старше снимка (но новее прежнего) -- ждём', async () => {
    mocks.after = [split('2025-09-11T14:10:00Z')]
    const root = await afterReset(RESET)
    expect(root.textContent).toContain('ждём отчёта')
    render(null, root)
  })

  it('снимка нет -- отсечки по времени нет, работает сравнение «до/после»', async () => {
    mocks.after = [split('2025-09-11T14:10:00Z')]
    const root = await afterReset(RESET_NO_SNAP)
    expect(root.textContent).toContain('1 из 1')
    render(null, root)
  })
})
