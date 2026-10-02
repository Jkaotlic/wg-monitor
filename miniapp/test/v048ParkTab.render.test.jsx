// @vitest-environment jsdom
// v0.48: «Парк» -- своя вкладка админа, первой в нижней панели. «Мои роутеры»
// -- чистый список: ни Парка под ним, ни массовых кнопок у админа. Владелец и
// оператор (не админы) вкладки не видят, и «Мои роутеры» у них прежние.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ session: null, routers: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchSession: () => Promise.resolve(mocks.session),
  createSession: () => Promise.resolve(mocks.session),
  fetchRouters: () => Promise.resolve(mocks.routers),
  dashboardLogout: () => Promise.resolve(null),
}))

vi.mock('../src/screens/RouterDetail.jsx', () => ({ RouterDetail: ({ id }) => <div class="stub">Сейчас {id}</div> }))
vi.mock('../src/screens/TunnelsTab.jsx', () => ({ TunnelsTab: () => <div class="stub">туннели</div> }))
vi.mock('../src/screens/DiagTab.jsx', () => ({ DiagTab: () => <div class="stub">проверки</div> }))
vi.mock('../src/screens/EventsTab.jsx', () => ({ EventsTab: () => <div class="stub">события</div> }))
vi.mock('../src/screens/ParkSection.jsx', () => ({ ParkSection: () => <div class="stub stub-park">парк</div> }))

const { App } = await import('../src/App.jsx')

const ROUTERS = [
  { id: 2, nickname: 'Дача', status: 'online', last_seen_age_sec: 40, role: 'owner' },
  { id: 3, nickname: 'Офис', status: 'offline', last_seen_age_sec: 7200, role: 'operator' },
  { id: 1, nickname: 'Дом', status: 'alert', last_seen_age_sec: 12, active_incidents: [{ check_name: 'hydraroute' }], role: 'owner' },
]
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)
const barLabels = (root) => [...root.querySelectorAll('.tabbar .tabbar-item')].map((b) => b.textContent.trim())

function setWide(wide) {
  if (wide) window.matchMedia = () => ({ matches: true, addEventListener() {}, removeEventListener() {} })
  else delete window.matchMedia
}

async function mountAt(url) {
  window.history.replaceState(null, '', url)
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<App />, root))
  await flush()
  await flush()
  return root
}

function cleanup(root) {
  render(null, root)
  root.remove()
}

// Админские массовые кнопки и Парк -- чего в «Моих роутерах» больше нет.
const PARK_BUTTONS = ['Добавить роутер', 'Свои VPN-серверы', 'Осмотреть все', 'Сверить версии у всех', 'Открыть в браузере']

beforeEach(() => {
  setWide(false)
  mocks.routers = { routers: ROUTERS }
})

describe('v0.48: вкладка «Парк» на телефоне', () => {
  it('оператору -- нет', async () => {
    mocks.session = { ok: true, is_admin: false, via: 'telegram' }
    mocks.routers = { routers: [ROUTERS[1]] }
    const root = await mountAt('/miniapp/')
    expect(barLabels(root)).not.toContain('Парк')
    expect(root.querySelector('.stub-park')).toBe(null)
    cleanup(root)
  })
})

describe('v0.48: ссылка на Парк', () => {
  it('?tab=park без роутера открывает Парк админу', async () => {
    mocks.session = { ok: true, is_admin: true, via: 'telegram' }
    const root = await mountAt('/miniapp/?tab=park')
    expect(root.querySelector('.overlay')).toBe(null)
    expect(root.querySelector('.park-tab .stub-park')).toBeTruthy()
    cleanup(root)
  })

  it('?tab=park не-админу не значит ничего: открывается роутер, Парка нет', async () => {
    mocks.session = { ok: true, is_admin: false, via: 'telegram' }
    const root = await mountAt('/miniapp/?tab=park')
    expect(root.querySelector('.stub-park')).toBe(null)
    expect(barLabels(root)).not.toContain('Парк')
    cleanup(root)
  })
})

describe('v0.48: «Парк» на широком экране', () => {
  it('админу -- первой вкладкой в колонке; экран Парка в основной области', async () => {
    setWide(true)
    mocks.session = { ok: true, is_admin: true, via: 'web' }
    const root = await mountAt('/dashboard/?router=2')
    expect([...root.querySelectorAll('.side-tabs .side-link')].map((b) => b.textContent.trim())).toEqual(['Парк', 'Роутер', 'VPN-туннели', 'Проверки', 'Настройки'])
    await act(async () => button(root.querySelector('.side-tabs'), 'Парк').click())
    await flush()
    expect(root.querySelector('main .park-tab .stub-park')).toBeTruthy()
    expect(window.location.search).toBe('?router=2&tab=park')
    cleanup(root)
  })

  it('«Парк» в колонке подсвечивается; пункта «Парк» в подвале нет', async () => {
    setWide(true)
    mocks.session = { ok: true, is_admin: true, via: 'web' }
    const root = await mountAt('/dashboard/')
    expect(root.querySelector('main .park-tab .stub-park')).toBeTruthy()
    expect(button(root.querySelector('.side-tabs'), 'Парк').classList.contains('side-link-active')).toBe(true)
    expect(root.querySelector('.side-foot').textContent).not.toContain('Парк')
    expect(window.location.search).toBe('?tab=park')
    cleanup(root)
  })

  it('не-админу вкладки нет', async () => {
    setWide(true)
    mocks.session = { ok: true, is_admin: false, via: 'web' }
    const root = await mountAt('/dashboard/?router=2')
    expect([...root.querySelectorAll('.side-tabs .side-link')].map((b) => b.textContent.trim())).not.toContain('Парк')
    cleanup(root)
  })
})
