// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// v0.57: проводка «Обслуживания» -- четыре раздела и два новых слоя.
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve({ role: 'admin', agent_version: 'v0.56.0' }),
}))
vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({ busy: false, result: null, error: null, errorCode: null, sleepNote: '', run: () => Promise.resolve(null) }),
}))

const { OverlayHost } = await import('../src/screens/OverlayHost.jsx')
const { AdminRepairSections } = await import('../src/screens/RouterAdminSections.jsx')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
async function mount(vnode) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(vnode, root))
  await flush()
  return root
}
const cleanup = (root) => { render(null, root); root.remove() }
const ROUTERS = [{ id: 2, nickname: 'home', status: 'online' }]

describe('v0.57: «Обслуживание»', () => {
  it('разделы по порядку и кнопки открывают свои экраны', async () => {
    const opened = []
    const root = await mount(
      <AdminRepairSections
        isAdmin
        onOpenPorthop={() => opened.push('porthop')}
        onOpenSpace={() => opened.push('space')}
        onOpenPackages={() => opened.push('packages')}
        onOpenDNSReset={() => opened.push('dnsreset')}
      />,
    )
    const titles = [...root.querySelectorAll('.section-title, h2')].map((h) => h.textContent)
    expect(titles).toEqual(['Смена порта при блокировке', 'Свободное место', 'Пакеты по расписанию', 'Эталонный DNS'])
    for (const b of root.querySelectorAll('button')) await act(async () => b.click())
    expect(opened).toEqual(['porthop', 'space', 'packages', 'dnsreset'])
    expect(root.textContent).not.toContain('Сброс DNS')
    cleanup(root)
  })

  it.each([
    ['porthop', 'Смена порта при блокировке'],
    ['space', 'Свободное место'],
  ])('слой %s: админу -- экран, «назад» -- во вкладку; не-админу -- слова', async (overlay, title) => {
    const dispatch = vi.fn()
    const nav = { routerID: 2, tab: 'manage', overlay, sheet: null }
    let root = await mount(<OverlayHost nav={nav} dispatch={dispatch} routers={ROUTERS} isAdmin />)
    expect(root.querySelector('.overlay-title').textContent).toBe(title)
    await act(async () => root.querySelector('.overlay-back').click())
    expect(dispatch).toHaveBeenCalledWith({ type: 'overlay', overlay: 'manage' })
    cleanup(root)
    root = await mount(<OverlayHost nav={nav} dispatch={() => {}} routers={ROUTERS} isAdmin={false} />)
    expect(root.textContent).toContain('Этот экран доступен только администратору.')
    cleanup(root)
  })
})
