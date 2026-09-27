// @vitest-environment jsdom
// Статус unknown (v0.46) на плитке «VPN-туннели» в «Проверках»: не красное
// «0 работает», а «· 1 не проверено». Формы -- miniappRouterDetailResp и
// miniappTunnel, имена вымышленные.
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouter: () => Promise.resolve({ router: { id: 12, nickname: 'vymysel', kind: 'static', status: 'online', stale: false, reach: 'online', last_seen_age_sec: 30 }, incidents: [] }),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [{ tunnel_id: 'awg11', name: 'vymysel-de', status: 'unknown', run_state: 'running', enabled: true, handshake_age_sec: 20 }], traffic: null }),
}))
vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({ busy: false, result: null, error: null, errorCode: null, run: () => Promise.resolve(null) }),
}))

const { DiagTab } = await import('../src/screens/DiagTab.jsx')

describe('плитка «VPN-туннели» при статусе unknown', () => {
  it('подпись говорит «не проверено», тон не danger', async () => {
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<DiagTab routerID={12} asleep={false} />, root))
    await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
    const tile = [...root.querySelectorAll('.stat-grid > *')].find((n) => /VPN-туннели/i.test(n.textContent))
    expect(tile.textContent).toContain('1 не проверено')
    expect(tile.outerHTML).not.toMatch(/danger/)
    render(null, root)
    root.remove()
  })
})
