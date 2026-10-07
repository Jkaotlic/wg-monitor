// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// v0.57: «Эталонный DNS» -- пробы эталона в предпросмотре и итоге, пропущенные
// серверы, отказ reference_unreachable словами, старый агент без проб.
const PREVIEW = 'Предпросмотр сброса DNS — ничего не изменено\n\nУберём (1):\n  − tls upstream 8.8.8.8 sni dns.google\n\nЗаменим на эталонные (9):\n  + tls upstream 9.9.9.9 sni dns.quad9.net\n'
const RESET = 'DNS reset → reference DoT\n\nснимок «до»: /opt/etc/wg-monitor/dns-before-1757600000.txt\n'

const mocks = vi.hoisted(() => ({ settings: null, checks: { checks: [], tunnels: [] }, calls: [], answers: {} }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve(mocks.settings),
  fetchRouterChecks: () => Promise.resolve(mocks.checks),
}))

vi.mock('../src/useCommand.js', async () => {
  const { useState } = await import('preact/hooks')
  return {
    useCommand: () => {
      const [state, setState] = useState({ busy: false, result: null, error: null, errorCode: null })
      return {
        ...state,
        run: (action, args) => {
          mocks.calls.push({ action, args })
          const pick = mocks.answers[action]
          const res = typeof pick === 'function' ? pick(args) : (pick ?? null)
          setState({ busy: false, result: res, error: null, errorCode: null })
          return Promise.resolve(res)
        },
      }
    },
  }
})

const { DNSResetScreen } = await import('../src/screens/DNSResetScreen.jsx')
const { Sheet } = await import('../src/ui/Sheet.jsx')

async function flush() {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
}

async function mount(vnode) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(vnode, root)
  })
  await flush()
  return root
}

const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent === text)
const PROBES = [
  { server: '9.9.9.9', purpose: 'foreign', ok: true },
  { server: '1.1.1.1', purpose: 'foreign', ok: false, error: 'нет ответа за 5s' },
  { server: 'common.dot.dns.yandex.net', purpose: 'ru', ok: true },
]
const UNREACHABLE = 'reference_unreachable: не ответил ни один заграничный DNS-сервер эталона — сброс не применён, ничего не изменено.\n'

async function previewWith(answer) {
  mocks.settings = { role: 'admin', agent_version: 'v0.57.0' }
  mocks.calls = []
  mocks.answers = { dns_reset: answer }
  let sheet = null
  const root = await mount(<DNSResetScreen routerID={2} routerName="home" openSheet={(s) => (sheet = s)} onClose={() => {}} />)
  await act(async () => button(root, 'Посмотреть, что изменится').click())
  await flush()
  return { root, sheet: () => sheet }
}

describe('«Эталонный DNS» (v0.57)', () => {
  it('заголовок -- «Эталонный DNS»', async () => {
    mocks.settings = { role: 'admin', agent_version: 'v0.57.0' }
    const root = await mount(<DNSResetScreen routerID={2} routerName="home" openSheet={() => {}} onClose={() => {}} />)
    expect(root.querySelector('h1').textContent).toBe('Эталонный DNS')
    expect(root.querySelector('.overlay-title').textContent).toBe('Эталонный DNS')
    render(null, root)
    root.remove()
  })

  it('предпросмотр рисует пробы и пропущенный сервер', async () => {
    const { root } = await previewWith({ status: 'ok', output: PREVIEW, payload: { probes: PROBES } })
    const probes = root.querySelector('.dns-probes')
    expect(probes.textContent).toContain('9.9.9.9 · заграничный')
    expect(probes.textContent).toContain('отвечает')
    expect(probes.textContent).toContain('не отвечает')
    expect(probes.textContent).toContain('нет ответа за 5s')
    expect(root.querySelector('.dns-probes-skipped').textContent).toBe('Не отвечает и в роутер не ставится: 1.1.1.1.')
    expect(button(root, 'Сбросить DNS').disabled).toBe(false)
    render(null, root)
    root.remove()
  })

  it('старый агент без проб -- «проверка доступности — с агента v0.57»', async () => {
    const { root } = await previewWith({ status: 'ok', output: PREVIEW })
    expect(root.querySelector('.dns-probes-none').textContent).toBe('Проверка доступности — с агента v0.57.')
    render(null, root)
    root.remove()
  })

  it('reference_unreachable в предпросмотре -- человеческим текстом, без кода, сброс закрыт', async () => {
    const { root } = await previewWith({ status: 'err', output: UNREACHABLE, payload: { probes: PROBES.map((p) => ({ ...p, ok: p.purpose === 'ru', error: p.purpose === 'ru' ? '' : 'нет ответа' })) } })
    expect(root.querySelector('.dns-unreachable').textContent).toContain('Ни один заграничный DNS-сервер эталона не ответил')
    expect(root.textContent).not.toContain('reference_unreachable')
    expect(root.querySelectorAll('.dns-probes .data-row')).toHaveLength(3)
    expect(button(root, 'Сбросить DNS').disabled).toBe(true)
    render(null, root)
    root.remove()
  })

  it('итог сброса -- пробы и пропущенные; отказ reference_unreachable -- «ничего не изменено»', async () => {
    const { root, sheet } = await previewWith({ status: 'ok', output: PREVIEW, payload: { probes: PROBES } })
    await act(async () => button(root, 'Сбросить DNS').click())
    await act(async () => sheet().onResult({ status: 'ok', output: RESET, payload: { probes: PROBES } }))
    await flush()
    const after = [...root.querySelectorAll('.section')].find((s) => s.textContent.includes('После сброса')) ?? root
    expect(after.textContent).toContain('Не отвечает и в роутер не ставится: 1.1.1.1.')
    await act(async () => sheet().onResult({ status: 'err', output: UNREACHABLE, payload: { probes: PROBES } }))
    await flush()
    expect(root.textContent).toContain('ничего не изменено')
    expect(root.textContent).not.toContain('не смог прочитать свои настройки')
    render(null, root)
    root.remove()
  })
})
