// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Секция «Панель роутера» -- первой во вкладке «Управление» (v0.41): хост
// строкой, нажатие открывает панель напрямую во внешнем браузере; без адреса
// ссылки нет вовсе; оператору роутера секции нет.
const mocks = vi.hoisted(() => ({ settings: null, opened: [] }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve(mocks.settings),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [] }),
  fetchRouterVersions: () => Promise.resolve(null),
}))

vi.mock('../src/telegram.js', async (importOriginal) => ({
  ...(await importOriginal()),
  openExternal: (url) => mocks.opened.push(url),
}))

const { SettingsSections } = await import('../src/screens/SettingsScreen.jsx')

async function mount() {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(<SettingsSections routerID={2} routerName="home" openSheet={() => {}} />, root)
  })
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
  return root
}

const sections = (root) => [...root.querySelectorAll('section')]
const panelSection = (root) => sections(root).find((s) => s.querySelector('.section-title')?.textContent === 'Панель роутера')

describe('«Панель роутера»', () => {
  it('первая секция: хост, нажатие открывает адрес во внешнем браузере', async () => {
    mocks.settings = { role: 'owner', panel_known: true, panel_scope: 'public', panel_url: 'https://awg.example.com' }
    mocks.opened = []
    const root = await mount()
    expect(sections(root)[0]).toBe(panelSection(root))
    const btn = panelSection(root).querySelector('.panel-open')
    expect(btn.textContent).toContain('awg.example.com')
    await act(async () => btn.click())
    expect(mocks.opened).toEqual(['https://awg.example.com/'])
    render(null, root)
    root.remove()
  })

  it('частный адрес -- подсказка про домашнюю сеть', async () => {
    mocks.settings = { role: 'admin', panel_known: true, panel_scope: 'private', panel_url: 'http://198.51.100.1:2222' }
    const root = await mount()
    expect(panelSection(root).textContent).toContain('откроется только из домашней сети')
    render(null, root)
    root.remove()
  })

  it('адреса нет -- строка «не сохранён» и ни одной ссылки', async () => {
    mocks.settings = { role: 'owner' }
    mocks.opened = []
    const root = await mount()
    expect(panelSection(root).textContent).toContain('Мы не знаем адрес панели этого роутера')
    expect(panelSection(root).querySelector('.panel-open')).toBe(null)
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

  it('«Агент на роутере» -- в «Что стоит на роутере», а не в порогах', async () => {
    mocks.settings = { role: 'operator', agent_version: 'v0.41.0', silence_after_sec: 120 }
    const root = await mount()
    const byTitle = (t) => sections(root).find((s) => s.querySelector('.section-title')?.textContent === t)
    expect(byTitle('Что стоит на роутере').textContent).toContain('Агент на роутере')
    expect(byTitle('Что стоит на роутере').textContent).toContain('v0.41.0')
    expect(byTitle('Опрос и тревоги').textContent).not.toContain('Агент на роутере')
    render(null, root)
    root.remove()
  })

  it('клиента билетов больше нет', async () => {
    const real = await vi.importActual('../src/api.js')
    expect('createPanelTicket' in real).toBe(false)
  })
})
