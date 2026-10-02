// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve({ role: 'owner', agent_version: 'v0.47.0' }),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [{ tunnel_id: 'awg10', name: 'vpn-nl', run_state: 'running', status: 'ok', ping_check_status: 'ok' }], traffic: null }),
  fetchRouter: () => Promise.resolve({ router: { id: 2, nickname: 'home', status: 'online', last_seen_age_sec: 30 }, incidents: [] }),
  fetchRouterFacts: () => Promise.resolve(null),
}))
const { DiagTab } = await import('../src/screens/DiagTab.jsx')
const { DIAG_SECTIONS } = await import('../src/diag.js')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const btn = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)

async function mount() {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<DiagTab routerID={2} asleep={false} openSheet={() => {}} />, root))
  await flush()
  await flush()
  return root
}

describe('v0.52: «Проверки» -- «Сейчас» в пять разделов', () => {
  it('разделы по порядку спеки', async () => {
    const root = await mount()
    expect([...root.querySelectorAll('.diag-group-title')].map((h) => h.textContent)).toEqual(DIAG_SECTIONS.map((s) => s.title))
    expect(DIAG_SECTIONS.map((s) => s.title)).toEqual(['Что спросили и что ответили', 'Адрес выхода', 'Интернет и DNS', 'Проверка связи VPN-туннелей', 'Осмотр изнутри'])
    render(null, root)
    root.remove()
  })
  it('главная кнопка -- «Проверить заново», одна лаймовая', async () => {
    const root = await mount()
    const limes = [...root.querySelectorAll('.btn-primary')]
    expect(limes.map((b) => b.textContent.trim())).toEqual(['Проверить заново'])
    expect(root.querySelector('#dg-answers').contains(limes[0])).toBe(true)
    expect(root.textContent).not.toContain('Проверить сейчас')
    render(null, root)
    root.remove()
  })
  it('дом каждого действия -- свой раздел', async () => {
    const root = await mount()
    expect(btn(root.querySelector('#dg-exit'), 'Сравнить адреса выхода')).toBeTruthy()
    expect(btn(root.querySelector('#dg-ping'), 'Не следить')).toBeTruthy()
    expect(btn(root.querySelector('#dg-ping'), 'Выключить')).toBeUndefined()
    expect(btn(root.querySelector('#dg-ping'), 'Проверить связь сейчас')).toBeTruthy()
    const inspect = root.querySelector('#dg-inspect')
    expect(btn(inspect, 'Осмотреть роутер')).toBeTruthy()
    expect(btn(inspect, 'Осмотр HydraRoute Neo')).toBeTruthy()
    expect(btn(inspect, 'Собрать отчёт')).toBeTruthy()
    expect(inspect.textContent).toContain('Журнал awg-manager')
    render(null, root)
    root.remove()
  })
})
