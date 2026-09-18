// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// v0.41: адрес панели awg-manager строкой под именем роутера -- в «Моих
// роутерах», в боковой колонке и в шапке роутера. В шапке это ссылка на
// панель; в строках списка -- просто текст: строка сама кнопка, и ссылку в
// кнопку не вкладываем (ревью 18.09).
const mocks = vi.hoisted(() => ({ opened: [] }))

vi.mock('../src/telegram.js', async (importOriginal) => ({
  ...(await importOriginal()),
  openExternal: (url) => mocks.opened.push(url),
}))
vi.mock('../src/screens/ParkSection.jsx', () => ({ ParkSection: () => <div class="stub-park">парк</div> }))
vi.mock('../src/useFleetRecheck.js', () => ({ useFleetRecheck: () => ({ batch: null, recheckAll: () => {} }) }))

const { FleetOverlay } = await import('../src/screens/FleetOverlay.jsx')
const { Sidebar } = await import('../src/ui/Sidebar.jsx')
const { WideHeader } = await import('../src/ui/WideHeader.jsx')
const { Header } = await import('../src/ui/Header.jsx')
const { TabBar } = await import('../src/ui/TabBar.jsx')
const { TABS } = await import('../src/nav.js')

const ROUTERS = [
  { id: 1, nickname: 'work', status: 'alert', last_seen_age_sec: 20, reserve_only_alert: true, panel_url: 'https://awg.example.com', active_incidents: [{ check_name: 'tunnel_awg10' }] },
  { id: 2, nickname: 'dacha', status: 'online', last_seen_age_sec: 40 },
]

async function mount(node) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(node, root))
  return root
}
const cleanup = (root) => {
  render(null, root)
  root.remove()
}

describe('адрес панели под именем роутера', () => {
  it('«Мои роутеры»: хост у роутера с адресом -- текстом, нажатие открывает роутер', async () => {
    mocks.opened = []
    const picked = []
    const root = await mount(<FleetOverlay routers={ROUTERS} currentID={1} onPick={(id) => picked.push(id)} onClose={() => {}} />)
    const lines = root.querySelectorAll('.panel-line')
    expect(lines).toHaveLength(1)
    expect(lines[0].textContent).toBe('awg.example.com')
    expect(lines[0].getAttribute('role')).toBe(null)
    expect(lines[0].tagName).toBe('SPAN')
    expect(root.querySelector('button [role="link"], button button')).toBe(null)
    await act(async () => lines[0].click())
    expect(mocks.opened).toEqual([])
    expect(picked).toEqual([1])
    // Янтарная плашка «резерв не работает» вместо красной «тревоги».
    expect(root.textContent).toContain('резерв не работает')
    expect(root.querySelector('.stub-park')).toBe(null)
    cleanup(root)
  })

  it('«Мои роутеры» админу -- Парк под списком', async () => {
    const root = await mount(<FleetOverlay routers={ROUTERS} currentID={1} onPick={() => {}} onClose={() => {}} isAdmin />)
    expect(root.querySelector('.fleet-park .stub-park')).toBeTruthy()
    cleanup(root)
  })

  it('боковая колонка: хост под именем текстом, янтарная точка', async () => {
    mocks.opened = []
    const picked = []
    const root = await mount(<Sidebar mode="web" routers={ROUTERS} currentID={2} onPick={(id) => picked.push(id)} onPark={() => {}} onLogout={() => {}} />)
    const line = root.querySelector('.side-row .panel-line')
    expect(line.textContent).toBe('awg.example.com')
    expect(line.getAttribute('role')).toBe(null)
    await act(async () => line.click())
    expect(mocks.opened).toEqual([])
    expect(picked).toEqual([1])
    expect(root.querySelector('.side-dot-warn')).toBeTruthy()
    cleanup(root)
  })

  it('шапка широкого экрана: хост под именем и без шестерёнки', async () => {
    const root = await mount(<WideHeader router={ROUTERS[0]} tab="router" onTab={() => {}} />)
    const link = root.querySelector('.main-head .panel-line')
    expect(link.textContent).toBe('awg.example.com')
    expect(link.tagName).toBe('BUTTON')
    mocks.opened = []
    await act(async () => link.click())
    expect(mocks.opened).toEqual(['https://awg.example.com/'])
    expect(root.querySelector('.main-gear')).toBe(null)
    cleanup(root)
    const plain = await mount(<WideHeader router={ROUTERS[1]} tab="router" onTab={() => {}} />)
    expect(plain.querySelector('.panel-line')).toBe(null)
    cleanup(plain)
  })
})

describe('пятая вкладка', () => {
  it('нижняя панель: «Управление» последней', async () => {
    const root = await mount(<TabBar tabs={TABS} tab="manage" onTab={() => {}} />)
    const items = [...root.querySelectorAll('.tabbar-item')].map((b) => b.textContent)
    expect(items).toEqual(['Сейчас', 'VPN-туннели', 'Проверки', 'Что было', 'Управление'])
    expect(root.querySelector('.tabbar-item-active').textContent).toBe('Управление')
    cleanup(root)
  })

  it('шапка телефона без шестерёнки', async () => {
    const root = await mount(<Header fleetVisible onFleet={() => {}} />)
    expect(root.querySelector('.app-header-gear')).toBe(null)
    expect(root.textContent).toContain('Мои роутеры')
    cleanup(root)
  })
})
