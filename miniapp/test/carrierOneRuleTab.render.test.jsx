// @vitest-environment jsdom
// B1 (v0.56), проводка: вкладка «VPN-туннели» берёт несущего из traffic
// ответа /events (того же, что у экрана «Роутер»), а не из первой политики
// снимка. Сценарии -- общая фикстура carrier_screens.js.
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { CARRIER_SCENARIOS } from './fixtures/carrier_screens.js'

const mocks = vi.hoisted(() => ({ checks: null }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterChecks: () => Promise.resolve(mocks.checks),
  // Вкладки читают проверки вместе с тревогами (I1): тот же ответ, тревог нет.
  fetchRouterChecksWithIncidents: (id) => Promise.resolve((() => Promise.resolve(mocks.checks))(id)).then((ev) => ({ incidents: [], ...ev })),
  fetchRouterSettings: () => Promise.resolve({ role: 'owner' }),
  listAutorepair: () => Promise.resolve({ tunnels: [] }),
}))
const CMD = vi.hoisted(() => ({ value: null }))
vi.mock('../src/useCommand.js', () => ({ useCommand: () => CMD.value }))

const { TunnelsTab } = await import('../src/screens/TunnelsTab.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function mount(s) {
  mocks.checks = { checks: [], tunnels: s.checks.tunnels, traffic: s.events.traffic }
  CMD.value = { busy: false, result: { status: 'ok', output: JSON.stringify(s.snapshot) }, error: null, errorCode: null, run: () => Promise.resolve(null) }
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<TunnelsTab routerID={7} asleep={false} openSheet={() => {}} />, root))
  await flush()
  return root
}

const heroTitle = (root) => root.querySelector('.traffic-title')?.textContent ?? null

describe.each(CARRIER_SCENARIOS)('вкладка «VPN-туннели», B1: $title', (s) => {
  it('называет того же несущего, что фикстура (или никого)', async () => {
    const root = await mount(s)
    expect(heroTitle(root)).toBe(s.expect.name)
    if (s.expect.name && !s.expect.alive) {
      expect(root.textContent).toContain('Несёт трафик, но не отвечает')
      expect(root.textContent).not.toContain('Работает сейчас')
    }
    if (!s.expect.name) {
      expect(root.textContent).not.toContain('Работает сейчас')
      expect(root.textContent).not.toContain('ни один VPN-туннель не несёт трафик')
    }
    if (s.singbox) {
      expect(root.textContent).toContain('настроено, маршрут выбирается по адресу')
      // Подпись режима -- у вкладки, а не заголовком строки VPN-туннеля.
      expect(root.textContent).not.toContain('Настроено, маршрут выбирается по адресу')
    }
    render(null, root)
    root.remove()
  })
})

describe('вкладка «VPN-туннели», B5: «правил», а не «назн.»', () => {
  it('у несущего и в строке «Маршруты» -- слово «правил»', async () => {
    const root = await mount(CARRIER_SCENARIOS[0])
    expect(root.textContent).not.toContain('назн.')
    expect(root.textContent).toMatch(/\d+ правил/)
    render(null, root)
    root.remove()
  })
})
