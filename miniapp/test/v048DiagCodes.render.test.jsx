// @vitest-environment jsdom
// v0.48: машинные имена проверок на «Проверках» (dns, hydraroute,
// agent_heartbeat, check_direct) -- только админу. Владельцу и оператору
// они ничего не говорят и теснят вопрос. Строка с ошибкой -- одна группа со
// своей строкой, без черты между ними.
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouter: () =>
    Promise.resolve({ router: { id: 9, nickname: 'vymysel', kind: 'static', status: 'alert', stale: false, last_seen_age_sec: 30 }, incidents: [] }),
  fetchRouterChecks: () =>
    Promise.resolve({
      checks: [
        { check_name: 'dns', status: 'fail', ts: '2026-09-29T08:00:00Z' },
        { check_name: 'hydraroute', status: 'ok', ts: '2026-09-29T08:00:00Z' },
      ],
      tunnels: [],
      traffic: null,
    }),
  fetchRouterVersions: () => Promise.resolve(null),
}))
vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({ busy: false, result: null, error: null, errorCode: null, run: () => Promise.resolve(null) }),
}))

const { DiagTab } = await import('../src/screens/DiagTab.jsx')

async function mount(props) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<DiagTab routerID={9} asleep={false} {...props} />, root))
  await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
  return root
}

function codes(root) {
  return [...root.querySelectorAll('.data-row-code')].map((n) => n.textContent)
}

describe('v0.48: машинные имена на «Проверках»', () => {
  it('не админу -- ни одного', async () => {
    const root = await mount({})
    expect(root.textContent).toContain('Сайты открываются по имени')
    expect(codes(root)).toEqual([])
    render(null, root)
    root.remove()
  })

  it('админу -- под вопросами и у адресов выхода', async () => {
    const root = await mount({ isAdmin: true })
    expect(codes(root)).toEqual(expect.arrayContaining(['dns', 'hydraroute', 'agent_heartbeat', 'check_direct', 'check_via_tunnel']))
    render(null, root)
    root.remove()
  })

  it('пояснение к сломанной строке живёт в одной группе со строкой', async () => {
    const root = await mount({})
    const consequence = root.querySelector('.diag-consequence')
    expect(consequence).not.toBeNull()
    const group = consequence.parentElement
    expect(group.classList.contains('data-row-group')).toBe(true)
    expect(group.querySelector('.data-row').textContent).toContain('Сайты открываются по имени')
    render(null, root)
    root.remove()
  })
})
