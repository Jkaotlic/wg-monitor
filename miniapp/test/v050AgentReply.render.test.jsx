// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Экраны, где раньше на экран шло «{result.output || result.status}»: любой
// не-ok ответ агента показывает фразу экрана, английский вывод и «timeout» -- нет.
const mocks = vi.hoisted(() => ({ result: null }))

vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({ busy: false, result: mocks.result, error: null, errorCode: null, sleepNote: '', run: () => Promise.resolve(null) }),
}))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [] }),
  fetchRouterFacts: () => Promise.resolve(null),
  fetchRouterSettings: () => Promise.resolve({ role: 'admin', agent_version: 'v0.50.0' }),
  fetchRouter: () => Promise.resolve({ router: { id: 2, nickname: 'home', status: 'online' }, incidents: [] }),
}))

const { DiagTab } = await import('../src/screens/DiagTab.jsx')
const { TunnelsTab } = await import('../src/screens/TunnelsTab.jsx')
const { RoutesTab } = await import('../src/screens/RoutesTab.jsx')
const { DNSResetScreen } = await import('../src/screens/DNSResetScreen.jsx')
const { AgentConfigScreen } = await import('../src/screens/AgentConfigScreen.jsx')
const { AwgmLogsSection } = await import('../src/screens/SignalSections.jsx')
const { RouteAddScreen } = await import('../src/screens/RouteAddScreen.jsx')
const { AppContext } = await import('../src/appContext.js')

const noop = () => {}
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

const clickText = (root, text) => {
  const el = [...root.querySelectorAll('button')].find((b) => b.textContent.includes(text))
  if (!el) throw new Error(`нет «${text}»: ${root.textContent}`)
  return act(async () => el.click())
}
// RouteAddScreen: превью -- третий шаг (туннель → «Вручную» → адрес → «Показать»).
async function toPreview(root) {
  await clickText(root, 'vpn-nl')
  await clickText(root, 'Вручную')
  const ta = root.querySelector('#route-add-targets')
  await act(async () => { ta.value = 'openai.com'; ta.dispatchEvent(new Event('input', { bubbles: true })) })
  await clickText(root, 'Показать, что изменится')
}

async function mount(node, steps) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<AppContext.Provider value={{ mode: 'miniapp', wide: false }}>{node}</AppContext.Provider>, root))
  await flush()
  if (steps) await steps(root)
  return root
}
const errors = (root) => [...root.querySelectorAll('.state-error')].map((e) => e.textContent)

const PLACES = [
  ['DiagTab (переспросить)', () => <DiagTab routerID={2} asleep={false} isAdmin openSheet={noop} />, 'Роутер не переспросил — попробуйте ещё раз через минуту.'],
  ['DiagTab (отчёт)', () => <DiagTab routerID={2} asleep={false} isAdmin openSheet={noop} />, 'Роутер не собрал отчёт — попробуйте ещё раз через минуту.'],
  ['TunnelsTab', () => <TunnelsTab routerID={2} asleep={false} openSheet={noop} />, 'Роутер не отдал снимок — попробуйте ещё раз через минуту.'],
  ['RoutesTab', () => <RoutesTab routerID={2} asleep={false} openSheet={noop} />, 'Роутер не отдал снимок маршрутизации — попробуйте ещё раз через минуту.'],
  ['DNSResetScreen', () => <DNSResetScreen routerID={2} routerName="home" asleep={false} openSheet={noop} onClose={noop} />, 'Роутер не показал предпросмотр — попробуйте ещё раз через минуту.'],
  ['AgentConfigScreen', () => <AgentConfigScreen routerID={2} routerName="home" asleep={false} openSheet={noop} onClose={noop} />, 'Роутер не ответил — попробуйте ещё раз через минуту.'],
  ['AwgmLogsSection', () => <AwgmLogsSection routerID={2} deadline={{}} />, 'Роутер не отдал журнал — попробуйте ещё раз через минуту.'],
  ['RouteAddScreen', () => <RouteAddScreen routerID={2} asleep={false} snapshot={{ tunnels: [{ id: 'awg10', name: 'vpn-nl', type: 'managed' }] }} openSheet={noop} onClose={noop} onApplied={noop} />, 'Роутер не смог собрать превью — попробуйте ещё раз через минуту.', toPreview],
]

beforeEach(() => { mocks.result = null })

describe('ответ агента без сырых строк', () => {
  for (const [name, view, phrase, steps] of PLACES) {
    it(`${name}: английский вывод и «timeout» -- фраза экрана`, async () => {
      mocks.result = { status: 'timeout', output: 'awgmgr GET /api/x: HTTP 500' }
      const root = await mount(view(), steps)
      const texts = errors(root)
      expect(texts.some((t) => t.includes(phrase))).toBe(true)
      expect(root.textContent).not.toMatch(/awgmgr|HTTP 500|timeout/)
      render(null, root)
    })

    it(`${name}: пустой вывод -- фраза экрана`, async () => {
      mocks.result = { status: 'timeout', output: '' }
      const root = await mount(view(), steps)
      expect(errors(root).some((t) => t.includes(phrase))).toBe(true)
      expect(root.textContent).not.toMatch(/timeout/)
      render(null, root)
    })

    it(`${name}: русский вывод агента сохраняется после фразы`, async () => {
      mocks.result = { status: 'error', output: 'Служба не отвечает.' }
      const root = await mount(view(), steps)
      expect(errors(root).some((t) => t.includes(`${phrase} Служба не отвечает.`))).toBe(true)
      render(null, root)
    })
  }
})
