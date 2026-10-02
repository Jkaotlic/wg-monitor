// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ session: null, routers: null }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchSession: () => Promise.resolve(mocks.session),
  createSession: () => Promise.resolve(mocks.session),
  fetchRouters: () => Promise.resolve(mocks.routers),
}))
vi.mock('../src/screens/RouterDetail.jsx', () => ({ RouterDetail: ({ id }) => <div class="stub">router {id}</div> }))
vi.mock('../src/screens/TunnelsTab.jsx', () => ({ TunnelsTab: () => <div class="stub">VPN-туннели</div> }))
vi.mock('../src/screens/DiagTab.jsx', () => ({ DiagTab: () => <div class="stub">Проверки</div> }))
vi.mock('../src/screens/EventsTab.jsx', () => ({ EventsTab: () => <div class="stub">Что было</div> }))
vi.mock('../src/screens/ManageTab.jsx', () => ({ ManageTab: () => <div class="stub">Настройки</div> }))
vi.mock('../src/screens/ParkTab.jsx', () => ({ ParkTab: () => <div class="stub stub-park">Парк</div> }))
vi.mock('../src/screens/RoutesTab.jsx', () => ({ RoutesTab: () => <div class="stub">Маршруты</div> }))

const { App } = await import('../src/App.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const R = (id, nickname, extra = {}) => ({ id, nickname, status: 'online', reach: 'online', last_seen_age_sec: 30, ...extra })
async function mountAt(url) {
  window.history.replaceState(null, '', url)
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<App />, root))
  await flush()
  await flush()
  return root
}
const cleanup = (root) => {
  render(null, root)
  root.remove()
}

beforeEach(() => {
  try {
    localStorage.removeItem('wgm.lastRouterID')
  } catch {
    // хранилища нет -- нечего чистить
  }
  window.matchMedia = () => ({ matches: true, addEventListener() {}, removeEventListener() {} })
})
const sideTabs = (root) => [...root.querySelectorAll('.side-tabs .side-link')].map((b) => b.textContent.trim())

describe('v0.52: широкая раскладка', () => {
  it('колонка: вкладки и роутеры; вкладок в шапке нет; «Парк» внизу колонки нет', async () => {
    mocks.session = { ok: true, is_admin: true, via: 'telegram' }
    mocks.routers = { routers: [R(2, 'Дача'), R(3, 'Офис')] }
    const root = await mountAt('/miniapp/?router=2')
    expect(sideTabs(root)).toEqual(['Парк', 'Роутер', 'VPN-туннели', 'Проверки', 'Настройки'])
    expect(root.querySelector('.main-tabs')).toBe(null)
    expect(root.querySelector('.side-foot')?.textContent ?? '').not.toContain('Парк')
    expect(root.querySelector('.side-head').textContent).toContain('Роутеры парка')
    cleanup(root)
  })
  it('админ без роутера: Парк в основной области, вкладка Парк активна', async () => {
    mocks.session = { ok: true, is_admin: true, via: 'telegram' }
    mocks.routers = { routers: [R(2, 'Дача'), R(3, 'Офис')] }
    const root = await mountAt('/miniapp/')
    expect(root.querySelector('.main .stub-park')).toBeTruthy()
    expect(sideTabs(root)).toEqual(['Парк'])
    expect(root.querySelector('.side-tabs .side-link-active').textContent.trim()).toBe('Парк')
    cleanup(root)
  })
  it('не-админ: заголовок списка «Мои роутеры», вкладка закрывает оверлей', async () => {
    mocks.session = { ok: true, is_admin: false, via: 'telegram' }
    mocks.routers = { routers: [R(2, 'Дача'), R(3, 'Офис')] }
    const root = await mountAt('/miniapp/?router=2&open=routes')
    expect(root.querySelector('.side-head').textContent).toContain('Мои роутеры')
    await act(async () => [...root.querySelectorAll('.side-tabs .side-link')].find((b) => b.textContent.trim() === 'Проверки').click())
    await flush()
    expect(root.querySelector('.main .overlay')).toBe(null)
    cleanup(root)
  })
})
