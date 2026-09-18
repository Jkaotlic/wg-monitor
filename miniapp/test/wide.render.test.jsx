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
  dashboardLogout: () => Promise.resolve(null),
}))

vi.mock('../src/screens/RouterDetail.jsx', () => ({ RouterDetail: ({ id }) => <div class="stub">Сейчас {id}</div> }))
vi.mock('../src/screens/TunnelsTab.jsx', () => ({ TunnelsTab: () => <div class="stub">туннели</div> }))
vi.mock('../src/screens/DiagTab.jsx', () => ({ DiagTab: () => <div class="stub">проверки</div> }))
vi.mock('../src/screens/EventsTab.jsx', () => ({ EventsTab: () => <div class="stub">события</div> }))
vi.mock('../src/screens/SettingsScreen.jsx', () => ({ SettingsSections: () => <div class="stub stub-settings">настройки</div> }))
vi.mock('../src/screens/RouterAdminSections.jsx', () => ({ RouterAdminSections: () => <div class="stub stub-admin">обслуживание</div> }))
vi.mock('../src/screens/AgentConfigScreen.jsx', () => ({ AgentConfigScreen: () => <div class="stub stub-agentcfg">настройки агента</div> }))
vi.mock('../src/screens/ParkSection.jsx', () => ({ ParkSection: ({ currentID }) => <div class="stub stub-park">парк {String(currentID)}</div> }))

const { App } = await import('../src/App.jsx')
const { Sidebar } = await import('../src/ui/Sidebar.jsx')

const ROUTERS = [
  { id: 2, nickname: 'Дача', status: 'online', last_seen_age_sec: 40 },
  { id: 3, nickname: 'Офис', status: 'offline', last_seen_age_sec: 7200 },
  { id: 1, nickname: 'Дом', status: 'alert', last_seen_age_sec: 12, active_incidents: [{ check_name: 'hydraroute' }] },
]
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)

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

beforeEach(() => {
  mocks.session = { ok: true, is_admin: true, via: 'web' }
  mocks.routers = { routers: ROUTERS }
})

describe('широкая раскладка в браузере', () => {
  it('боковая колонка и сводка вместо нижних вкладок', async () => {
    setWide(true)
    const root = await mountAt('/dashboard/')
    expect(root.querySelector('.wide-shell')).toBeTruthy()
    expect(root.querySelector('.tabbar')).toBe(null)
    expect(root.querySelector('.app-header')).toBe(null)
    const side = root.querySelector('aside.side')
    expect(side.textContent).toContain('веб-управление')
    expect(button(side, 'Выйти')).toBeTruthy()
    expect(button(side, 'Парк')).toBeTruthy()
    // Порядок -- сломанное сверху, как в FleetOverlay.
    expect([...side.querySelectorAll('.side-row-name')].map((n) => n.textContent)).toEqual(['Дом', 'Офис', 'Дача'])
    // Роутер не выбран -- сводка.
    expect([...root.querySelectorAll('.fleet-count-value')].map((n) => n.textContent)).toEqual(['1', '1', '1'])
    expect(root.querySelectorAll('.fleet-card')).toHaveLength(2)
    // Админу Парк -- прямо под сводкой: от выбранного роутера он не зависит.
    expect(root.querySelector('.fleet-home .stub-park')).toBeTruthy()
    cleanup(root)
  })

  it('«Парк» без выбранного роутера не погашен и остаётся на сводке с Парком', async () => {
    setWide(true)
    const root = await mountAt('/dashboard/')
    const park = button(root.querySelector('aside.side'), 'Парк')
    expect(park.disabled).toBe(false)
    await act(async () => park.click())
    await flush()
    expect(root.querySelector('.fleet-home .stub-park')).toBeTruthy()
    expect(root.querySelector('.stub-admin')).toBe(null)
    expect(window.location.search).toBe('')
    cleanup(root)
  })

  it('выбор роутера в колонке -- шапка с вкладками сверху', async () => {
    setWide(true)
    const root = await mountAt('/dashboard/')
    const row = [...root.querySelectorAll('.side-row')].find((b) => b.textContent.includes('Дача'))
    await act(async () => row.click())
    await flush()
    expect(root.querySelector('.main-head-name').textContent).toBe('Дача')
    expect([...root.querySelectorAll('.main-tab')].map((b) => b.textContent)).toEqual(['Сейчас', 'VPN-туннели', 'Проверки', 'Что было', 'Управление'])
    expect(root.querySelector('.main-tab-active').textContent).toBe('Сейчас')
    expect(row.getAttribute('aria-current')).toBe('page')
    expect(root.textContent).toContain('Сейчас 2')
    expect(window.location.search).toBe('?router=2')
    cleanup(root)
  })

  it('оверлей -- в основной области, вкладка закрывает его', async () => {
    setWide(true)
    const root = await mountAt('/dashboard/?router=2&open=agentcfg')
    expect(root.querySelector('main .stub-agentcfg')).toBeTruthy()
    expect(root.querySelector('aside.side')).toBeTruthy()
    await act(async () => button(root.querySelector('.main-tabs'), 'Что было').click())
    await flush()
    expect(root.querySelector('.stub-agentcfg')).toBe(null)
    expect(window.location.search).toBe('?router=2&tab=events')
    cleanup(root)
  })

  it('шестерёнки нет: «Управление» -- вкладка; «Парк» -- сводка с Парком, подсвечивается', async () => {
    setWide(true)
    const root = await mountAt('/dashboard/?router=2')
    expect(root.querySelector('.main-gear')).toBe(null)
    await act(async () => button(root.querySelector('.main-tabs'), 'Управление').click())
    await flush()
    expect(root.querySelector('.stub-settings')).toBeTruthy()
    expect(root.querySelector('.stub-admin')).toBeTruthy()
    expect(window.location.search).toBe('?router=2&tab=manage')
    await act(async () => button(root.querySelector('aside.side'), 'Парк').click())
    await flush()
    expect(root.querySelector('.fleet-home #park .stub-park')).toBeTruthy()
    expect(button(root.querySelector('aside.side'), 'Парк').classList.contains('side-link-active')).toBe(true)
    cleanup(root)
  })

  it('старая ссылка ?open=settings открывает вкладку «Управление»', async () => {
    setWide(true)
    const root = await mountAt('/dashboard/?router=2&open=settings')
    expect(root.querySelector('.main-tab-active').textContent).toBe('Управление')
    expect(root.querySelector('main .stub-settings')).toBeTruthy()
    cleanup(root)
  })
})

describe('широкая раскладка в Telegram Desktop', () => {
  it('без «Выйти» и без подписи веб-управления', async () => {
    setWide(true)
    mocks.session = { ok: true, is_admin: false, via: 'telegram' }
    const root = await mountAt('/miniapp/?router=2')
    const side = root.querySelector('aside.side')
    expect(side).toBeTruthy()
    expect(side.textContent).not.toContain('веб-управление')
    expect(button(side, 'Выйти')).toBeUndefined()
    expect(button(side, 'Парк')).toBeUndefined()
    cleanup(root)
  })

  it('не админ: сводка без Парка', async () => {
    setWide(true)
    mocks.session = { ok: true, is_admin: false, via: 'telegram' }
    const root = await mountAt('/miniapp/')
    expect(root.querySelector('.fleet-home')).toBeTruthy()
    expect(root.querySelector('.stub-park')).toBe(null)
    cleanup(root)
  })
})

describe('телефонная раскладка', () => {
  it('нижние вкладки, колонки нет', async () => {
    setWide(false)
    const root = await mountAt('/dashboard/?router=2')
    expect(root.querySelector('.tabbar')).toBeTruthy()
    expect(root.querySelector('aside.side')).toBe(null)
    expect(root.querySelector('.wide-shell')).toBe(null)
    cleanup(root)
  })
})

describe('Sidebar', () => {
  it('«Парк» без выбранного роутера не погашен и зовёт onPark', async () => {
    const root = document.createElement('div')
    const onPark = vi.fn()
    await act(async () =>
      render(<Sidebar mode="web" routers={ROUTERS} currentID={null} isAdmin parkActive={false} onPick={() => {}} onPark={onPark} onLogout={() => {}} />, root),
    )
    const park = button(root, 'Парк')
    expect(park.disabled).toBe(false)
    expect(park.getAttribute('title')).toBe(null)
    await act(async () => park.click())
    expect(onPark).toHaveBeenCalledTimes(1)
    render(null, root)
  })
})
