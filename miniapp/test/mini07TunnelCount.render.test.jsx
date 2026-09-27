// @vitest-environment jsdom
// MINI-07: «Проверки» и «Сейчас» считают работающие VPN-туннели одним
// правилом. Было: «3 из 3 на связи» против «1 работает из 3». Форма --
// miniappTunnel (tunnel_id/status/run_state/enabled/handshake_age_sec).
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { workingTunnelCount } from '../src/labels.js'

const TUNNELS = [
  { tunnel_id: 'awg10', name: 'vymysel-a', status: 'ok', run_state: 'running', enabled: true, handshake_age_sec: 20 },
  // Проверка ещё «ok», но роутер сказал «stopped».
  { tunnel_id: 'awg11', name: 'vymysel-b', status: 'ok', run_state: 'stopped', enabled: true, handshake_age_sec: 20 },
  // Интерфейс поднят, но по нему открыта тревога.
  { tunnel_id: 'awg12', name: 'vymysel-c', status: 'ok', run_state: 'running', enabled: true, handshake_age_sec: 20 },
]
const INCIDENTS = [{ check_name: 'tunnel_awg12', severity: 'hard' }]

const mocks = vi.hoisted(() => ({}))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouter: () =>
    Promise.resolve({ router: { id: 9, nickname: 'vymysel', kind: 'static', status: 'alert', stale: false, last_seen_age_sec: 30 }, incidents: INCIDENTS }),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: TUNNELS, traffic: null }),
  fetchRouterVersions: () => Promise.resolve(null),
}))
vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({ busy: false, result: null, error: null, errorCode: null, run: () => Promise.resolve(null) }),
}))

const { DiagTab } = await import('../src/screens/DiagTab.jsx')

describe('MINI-07', () => {
  it('одно правило: поднят, проверка не провалена, тревоги нет', () => {
    expect(workingTunnelCount(TUNNELS, INCIDENTS)).toBe(1)
  })
  it('плитка «VPN-туннели» в «Проверках» -- 1 из 3, как на «Сейчас»', async () => {
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<DiagTab routerID={9} asleep={false} />, root))
    await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
    const tile = [...root.querySelectorAll('.stat-grid > *')].find((n) => /VPN-туннели/i.test(n.textContent))
    expect(tile.textContent).toContain('1')
    expect(tile.textContent).not.toContain('3 из 3')
    expect(tile.textContent).toContain('работает из 3')
    render(null, root)
    root.remove()
  })
})
