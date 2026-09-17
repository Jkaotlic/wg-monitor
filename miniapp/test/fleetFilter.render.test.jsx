// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

vi.mock('../src/useFleetRecheck.js', () => ({ useFleetRecheck: () => ({ batch: null, recheckAll: () => {} }) }))

const { Sidebar } = await import('../src/ui/Sidebar.jsx')
const { FleetOverlay } = await import('../src/screens/FleetOverlay.jsx')

const ROUTERS = [
  { id: 1, nickname: 'dom-kiev', status: 'alert', last_seen_age_sec: 10, active_incidents: [{ check_name: 'dns' }] },
  { id: 2, nickname: 'car-bmw', status: 'sleeping', last_seen_age_sec: 4000 },
  { id: 3, nickname: 'office', status: 'online', last_seen_age_sec: 30 },
  { id: 4, nickname: 'dacha', status: 'offline', last_seen_age_sec: 90000 },
]

async function mount(vnode) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(vnode, root))
  return root
}
const cleanup = (root) => {
  render(null, root)
  root.remove()
}
const search = (root) => root.querySelector('input.filter-search')
async function type(el, value) {
  await act(async () => {
    el.value = value
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
}
const chip = (root, label) => [...root.querySelectorAll('.filter-chip')].find((b) => b.querySelector('.filter-chip-label').textContent === label)
const count = (root, label) => chip(root, label).querySelector('.filter-chip-count').textContent
const sideNames = (root) => [...root.querySelectorAll('.side-row-name')].map((n) => n.textContent)
const sidebar = (props = {}) => (
  <Sidebar mode="web" routers={ROUTERS} currentID={null} isAdmin parkActive={false} onPick={() => {}} onPark={() => {}} onLogout={() => {}} {...props} />
)

describe('поиск и фильтры в боковой колонке', () => {
  it('поиск сужает список, счётчики следуют поиску', async () => {
    const root = await mount(sidebar())
    expect(sideNames(root)).toEqual(['dom-kiev', 'dacha', 'car-bmw', 'office'])
    expect(count(root, 'все')).toBe('4')
    expect(count(root, 'молчат')).toBe('1')
    await type(search(root), 'car')
    expect(sideNames(root)).toEqual(['car-bmw'])
    expect(count(root, 'все')).toBe('1')
    expect(count(root, 'спят')).toBe('1')
    expect(count(root, 'тревога')).toBe('0')
    cleanup(root)
  })

  it('чип фильтра -- нажат и сужает список; поле без автозаполнения', async () => {
    const root = await mount(sidebar())
    await act(async () => chip(root, 'молчат').click())
    expect(sideNames(root)).toEqual(['dacha'])
    expect(chip(root, 'молчат').getAttribute('aria-pressed')).toBe('true')
    expect(chip(root, 'все').getAttribute('aria-pressed')).toBe('false')
    expect(search(root).getAttribute('autocomplete')).toBe('off')
    cleanup(root)
  })

  it('ничего не нашлось -- фраза и «Сбросить» возвращает всех', async () => {
    const root = await mount(sidebar())
    await type(search(root), 'zzz')
    expect(sideNames(root)).toEqual([])
    expect(root.querySelector('.filter-empty').textContent).toContain('Ничего не нашлось по «zzz».')
    await act(async () => root.querySelector('.filter-empty button').click())
    expect(sideNames(root)).toHaveLength(4)
    expect(search(root).value).toBe('')
    cleanup(root)
  })

  it('«/» ставит курсор в поиск; при открытом листе -- нет', async () => {
    const root = await mount(sidebar())
    await act(async () => window.dispatchEvent(new KeyboardEvent('keydown', { key: '/', bubbles: true })))
    expect(document.activeElement).toBe(search(root))
    search(root).blur()
    await act(async () => render(sidebar({ shortcut: false }), root))
    await act(async () => window.dispatchEvent(new KeyboardEvent('keydown', { key: '/', bubbles: true })))
    expect(document.activeElement).not.toBe(search(root))
    cleanup(root)
  })

  it('Esc в поле стирает запрос', async () => {
    const root = await mount(sidebar())
    await type(search(root), 'car')
    await act(async () => search(root).dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    expect(sideNames(root)).toHaveLength(4)
    cleanup(root)
  })

  it('один роутер -- искать не в чем, поля нет', async () => {
    const root = await mount(sidebar({ routers: [ROUTERS[0]] }))
    expect(search(root)).toBe(null)
    expect(sideNames(root)).toEqual(['dom-kiev'])
    cleanup(root)
  })
})

describe('«Мои роутеры» на телефоне', () => {
  it('тот же поиск над списком, заголовок считает весь парк', async () => {
    const root = await mount(<FleetOverlay routers={ROUTERS} currentID={3} onPick={() => {}} onClose={() => {}} />)
    const names = () => [...root.querySelectorAll('.fleet-row .row-title')].map((n) => n.textContent)
    expect(names()).toHaveLength(4)
    await act(async () => chip(root, 'тревога').click())
    expect(names()).toEqual(['dom-kiev'])
    expect(root.querySelector('.router-lastseen').textContent).toContain('из 4')
    cleanup(root)
  })
})
