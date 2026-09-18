// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import SNAP from './fixtures/split_reserve_dead.json'

// Реплей прод-снимка workrouter 18.09 через весь экран «Сейчас»: схема,
// заголовок и строка резерва обязаны сказать одно и то же.

const mocks = vi.hoisted(() => ({ router: null, checks: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouter: () => Promise.resolve(mocks.router),
  fetchRouterChecks: () => Promise.resolve(mocks.checks),
}))

vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({ busy: false, result: null, error: null, errorCode: null, run: () => Promise.resolve(null) }),
}))

const { RouterDetail } = await import('../src/screens/RouterDetail.jsx')
const { AppContext } = await import('../src/appContext.js')

async function mount(traffic) {
  mocks.router = { router: structuredClone(SNAP.router), incidents: structuredClone(SNAP.incidents) }
  mocks.checks = { ...structuredClone(SNAP.events), traffic }
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () =>
    render(
      <AppContext.Provider value={{ mode: 'miniapp', wide: false }}>
        <RouterDetail id={56} panelURL="https://awg.example.com" openSheet={() => {}} onTab={() => {}} />
      </AppContext.Provider>,
      root,
    ),
  )
  await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
  return root
}

describe('«Сейчас» на снимке workrouter 18.09', () => {
  it('старый агент: нет красной ветки и нет ложного «Запасной готов»', async () => {
    const root = await mount(SNAP.events.traffic)
    const svg = root.querySelector('.traffic-path').textContent
    expect(svg).not.toContain('VPN-ТУННЕЛЬ МОЛЧИТ')
    expect(root.textContent).not.toContain('Запасной VPN-туннель готов')
    render(null, root)
    root.remove()
  })

  it('агент назвал несущего: зелёная ветка через vpn-hip, 117 мс, резерва нет', async () => {
    const root = await mount({ ...SNAP.events.traffic, egress_tunnel_id: 'awg14', egress_tunnel_name: 'vpn-hip', reserve_tunnel_ids: [] })
    const svg = root.querySelector('.traffic-path').textContent
    expect(svg).toContain('vpn-hip')
    expect(svg).not.toContain('VPN-ТУННЕЛЬ МОЛЧИТ')
    expect(root.querySelector('.stat-grid').textContent).toContain('117')
    // Плитка считает работающие, а не поднятые интерфейсы: у vpn-nl
    // интерфейс поднят, но обмен ключами не проходит.
    const tunnelsTile = [...root.querySelectorAll('.stat-grid > *')].find((n) => /VPN-туннели/i.test(n.textContent))
    expect(tunnelsTile.textContent).toMatch(/^VPN-туннели\s*1/i)
    expect(tunnelsTile.textContent).toContain('работает из 2')
    expect(root.querySelector('.hero').textContent).toContain('всё работает, резерва нет')
    expect(root.textContent).toContain('Запасного VPN-туннеля нет')
    // Адрес панели -- строкой под именем роутера в шапке телефона.
    expect(root.querySelector('.hero .panel-line').textContent).toBe('awg.example.com')
    render(null, root)
    root.remove()
  })
})
