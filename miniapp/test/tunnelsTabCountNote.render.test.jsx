// @vitest-environment jsdom
// Fix 2 (v0.56): подпись под «Все VPN-туннели · N» на вкладке -- обычная
// строка, а не плитка с числом, как на «Роутере» и «Проверках». Пересъёмка
// песочницы показала «работают из 3 настроенных» без числа работающих.
// Данные -- живые ответы песочницы (fixtures/sandbox_counts.json).
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import sandbox from './fixtures/sandbox_counts.json'
import { tunnelCountSummary } from '../src/labels.js'

const mocks = vi.hoisted(() => ({ ev: null }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterChecksWithIncidents: () => Promise.resolve(mocks.ev),
  fetchRouterSettings: () => Promise.resolve({ role: 'owner' }),
  listAutorepair: () => Promise.resolve({ tunnels: [] }),
}))
const CMD = vi.hoisted(() => ({ value: null }))
vi.mock('../src/useCommand.js', () => ({ useCommand: () => CMD.value }))

const { TunnelsTab } = await import('../src/screens/TunnelsTab.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

describe.each(['sandbox-broken', 'sandbox-work'])('вкладка «VPN-туннели» на данных песочницы: %s', (name) => {
  it('подпись называет число работающих -- то же, что «Роутер» и «Проверки»', async () => {
    const s = sandbox[name]
    mocks.ev = { checks: [], ...s.events, incidents: s.incidents }
    CMD.value = { busy: false, result: { status: 'ok', output: JSON.stringify(s.snapshot) }, error: null, errorCode: null, run: () => Promise.resolve(null) }
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<TunnelsTab routerID={7} asleep={false} openSheet={() => {}} />, root))
    await flush()
    // Так считают «Роутер» (RouterDetail.jsx) и «Проверки» (DiagTab.jsx).
    const main = tunnelCountSummary(s.events.tunnels, s.incidents)
    const note = [...root.querySelectorAll('p.state')].map((p) => p.textContent).find((t) => /настроенных/.test(t))
    expect(note).toBeTruthy()
    expect(note).toMatch(new RegExp(`^${main.working} работа\\S* из ${main.total} настроенных`))
    render(null, root)
    root.remove()
  })
})
