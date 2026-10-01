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
const barLabels = (root) => [...root.querySelectorAll('.tabbar .tabbar-item')].map((b) => b.textContent.trim())

beforeEach(() => {
  delete window.matchMedia
  mocks.session = { ok: true, is_admin: false, via: 'telegram' }
})

describe('v0.52: оболочка телефона', () => {
  it('не-админ: 4 вкладки, подписи полностью, ни «Все роутеры», ни «Роутеры»', async () => {
    mocks.routers = { routers: [R(2, 'Дача')] }
    const root = await mountAt('/miniapp/')
    expect(barLabels(root)).toEqual(['Роутер', 'VPN-туннели', 'Проверки', 'Настройки'])
    expect(root.textContent).not.toContain('Все роутеры')
    expect(root.querySelector('.router-strip')).toBe(null)
    expect(root.querySelector('.router-switch')).toBe(null)
    expect(root.querySelector('.app-header-title').textContent).toBe('Дача')
    cleanup(root)
  })

  it('2–5 роутеров: полоса чипов, тревога первой, нажатие держит вкладку', async () => {
    mocks.routers = { routers: [R(2, 'Дача'), R(3, 'router4car4new', { status: 'alert' }), R(4, 'дача-северная')] }
    const root = await mountAt('/miniapp/?router=2&tab=diag')
    const chips = [...root.querySelectorAll('.router-strip .strip-chip')]
    expect(chips.map((c) => c.textContent.trim())).toEqual(['router4car4new', 'Дача', 'дача-северная'])
    expect(chips[0].querySelector('.strip-dot-danger')).toBeTruthy()
    expect(chips[1].getAttribute('aria-current')).toBe('page')
    await act(async () => chips[0].click())
    await flush()
    expect(root.querySelector('.tabbar-item-active').textContent.trim()).toBe('Проверки')
    expect(root.querySelector('.app-header-title').textContent).toBe('router4car4new')
    cleanup(root)
  })

  it('2–5 без ссылки: открывается роутер в тревоге', async () => {
    mocks.routers = { routers: [R(2, 'Дача'), R(3, 'Офис', { status: 'alert' })] }
    const root = await mountAt('/miniapp/')
    expect(root.querySelector('.stub').textContent).toBe('router 3')
    cleanup(root)
  })

  it('6+: имя в шапке открывает список «Мои роутеры» с поиском', async () => {
    mocks.routers = { routers: [1, 2, 3, 4, 5, 6].map((i) => R(i, `r${i}`)) }
    const root = await mountAt('/miniapp/?router=1')
    expect(root.querySelector('.router-strip')).toBe(null)
    await act(async () => root.querySelector('.router-switch').click())
    await flush()
    expect(root.querySelector('.overlay .screen-title').textContent).toBe('Мои роутеры')
    expect(root.querySelector('.overlay input[type=search]')).toBeTruthy()
    cleanup(root)
  })

  it('админ: Парк сразу, «Выбрать роутер» в шапке, полосы нет; 5 вкладок с роутером', async () => {
    mocks.session = { ok: true, is_admin: true, via: 'telegram' }
    mocks.routers = { routers: [R(2, 'Дача'), R(3, 'Офис')] }
    const root = await mountAt('/miniapp/')
    expect(root.querySelector('.stub-park')).toBeTruthy()
    expect(root.querySelector('.router-strip')).toBe(null)
    expect(root.querySelector('.tabbar')).toBe(null)
    await act(async () => root.querySelector('.router-switch').click())
    await flush()
    await act(async () => [...root.querySelectorAll('.overlay .fleet-row')][0].click())
    await flush()
    expect(barLabels(root)).toEqual(['Парк', 'Роутер', 'VPN-туннели', 'Проверки', 'Настройки'])
    cleanup(root)
  })
  it('N2: молчащий роутер -- красная точка, но не окраска тревоги и не первый', async () => {
    mocks.routers = { routers: [R(2, 'Дача'), R(3, 'Яблоко', { status: 'alert', reach: 'offline', last_seen_age_sec: 7200 })] }
    const root = await mountAt('/miniapp/?router=2')
    const chips = [...root.querySelectorAll('.router-strip .strip-chip')]
    expect(chips.map((c) => c.textContent.trim())).toEqual(['Дача', 'Яблоко'])
    expect(chips[1].classList.contains('strip-chip-alert')).toBe(false)
    expect(chips[1].getAttribute('aria-label')).toBe('Яблоко: молчит')
    cleanup(root)
  })
})
