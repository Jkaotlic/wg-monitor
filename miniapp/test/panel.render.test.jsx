// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Секция «Панель роутера» на экране настроек: кнопка просит билет и открывает
// его во внешнем браузере; без адреса кнопки нет вовсе.
const mocks = vi.hoisted(() => ({ settings: null, opened: [], ticketCalls: 0, ticket: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve(mocks.settings),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [] }),
  fetchRouterVersions: () => Promise.resolve(null),
  createPanelTicket: () => {
    mocks.ticketCalls++
    return mocks.ticket instanceof Error ? Promise.reject(mocks.ticket) : Promise.resolve(mocks.ticket)
  },
}))

vi.mock('../src/telegram.js', async (importOriginal) => ({
  ...(await importOriginal()),
  openExternal: (url) => mocks.opened.push(url),
}))

const { SettingsScreen } = await import('../src/screens/SettingsScreen.jsx')

async function mount() {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(<SettingsScreen routerID={2} routerName="home" openSheet={() => {}} onClose={() => {}} />, root)
  })
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
  return root
}

const panelSection = (root) => [...root.querySelectorAll('section')].find((s) => s.querySelector('.section-title')?.textContent === 'Панель роутера')
const openButton = (root) => [...root.querySelectorAll('button')].find((b) => b.textContent === 'Открыть панель роутера')

describe('«Панель роутера»', () => {
  it('кнопка берёт билет и открывает его во внешнем браузере', async () => {
    mocks.settings = { role: 'owner', panel_known: true, panel_scope: 'public' }
    mocks.opened = []
    mocks.ticketCalls = 0
    mocks.ticket = { open_path: '/v1/panel/' + 'cd'.repeat(32), expires_in_sec: 60 }
    const root = await mount()

    expect(panelSection(root).textContent).toContain('известна')
    expect(mocks.ticketCalls).toBe(0) // билет не выдаётся заранее
    await act(async () => openButton(root).click())
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(mocks.ticketCalls).toBe(1)
    expect(mocks.opened).toEqual([window.location.origin + '/v1/panel/' + 'cd'.repeat(32)])
    expect(panelSection(root).textContent).toContain('Панель откроется во внешнем браузере')
    render(null, root)
    root.remove()
  })

  it('адреса нет -- строка «не сохранён» и ни одной кнопки', async () => {
    mocks.settings = { role: 'owner' }
    mocks.opened = []
    const root = await mount()
    expect(panelSection(root).textContent).toContain('Мы не знаем адрес панели этого роутера')
    expect(openButton(root)).toBeUndefined()
    render(null, root)
    root.remove()
  })

  it('оператору секции нет вовсе', async () => {
    mocks.settings = { role: 'operator' }
    const root = await mount()
    expect(panelSection(root)).toBeUndefined()
    render(null, root)
    root.remove()
  })

  it('сервер отказал -- говорим словами, браузер не открываем', async () => {
    mocks.settings = { role: 'owner', panel_known: true, panel_scope: 'public' }
    mocks.opened = []
    mocks.ticket = new Error('409')
    const root = await mount()
    await act(async () => openButton(root).click())
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(mocks.opened).toEqual([])
    expect(panelSection(root).textContent).toContain('Не удалось открыть панель. Попробуйте ещё раз.')
    render(null, root)
    root.remove()
  })
})
