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
vi.mock('../src/screens/TunnelsTab.jsx', () => ({ TunnelsTab: ({ routerID }) => <div class="stub">туннели {routerID}</div> }))
vi.mock('../src/screens/DiagTab.jsx', () => ({ DiagTab: ({ routerID }) => <div class="stub stub-diag">проверки {routerID}</div> }))
vi.mock('../src/screens/EventsTab.jsx', () => ({ EventsTab: () => <div class="stub">события</div> }))
vi.mock('../src/screens/ParkSection.jsx', () => ({ ParkSection: () => <div class="stub stub-park">парк</div> }))

const { App } = await import('../src/App.jsx')
const { Fold } = await import('../src/ui/Fold.jsx')
const { Sheet } = await import('../src/ui/Sheet.jsx')
const { localSheet } = await import('../src/sheet.js')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)
const R = (id, nickname, over = {}) => ({ id, nickname, status: 'online', last_seen_age_sec: 30, role: 'owner', ...over })

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
  delete window.matchMedia
  mocks.session = { ok: true, is_admin: false, via: 'telegram' }
  mocks.routers = { routers: [R(2, 'Дача'), R(3, 'Офис', { status: 'offline', last_seen_age_sec: 7200 }), R(1, 'Дом', { status: 'alert', active_incidents: [{ check_name: 'dns' }] })] }
})

describe('Fold', () => {
  it('заголовок и итог видны, тело в DOM и в свёрнутом виде; open управляется', async () => {
    const root = document.createElement('div')
    document.body.appendChild(root)
    const seen = []
    await act(async () => render(<Fold title="Версии" note="агент v0.47.0, отстаёт" onToggle={(o) => seen.push(o)}><button type="button">внутри</button></Fold>, root))
    const d = root.querySelector('details.fold')
    expect(d.open).toBe(false)
    expect(root.querySelector('.fold-title').textContent).toBe('Версии')
    expect(root.querySelector('.fold-note').textContent).toBe('агент v0.47.0, отстаёт')
    expect(button(root, 'внутри')).toBeTruthy()
    await act(async () => render(<Fold title="Версии" open>{'x'}</Fold>, root))
    expect(root.querySelector('details.fold').open).toBe(true)
    cleanup(root)
  })
})

describe('лист выбора: пилюли и поиск', () => {
  const choices = Array.from({ length: 8 }, (_, i) => ({ value: i + 1, label: `роутер-${i + 1}`, pill: { tone: 'ok', text: 'в порядке' }, current: i === 0 }))

  it('вариант несёт пилюлю и отметку текущего; поиск сужает список', async () => {
    const picked = []
    const sheet = localSheet({ title: 'Какой роутер открыть', body: '', choices, search: true, perform: (_t, _v, id) => { picked.push(id); return null } })
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<Sheet sheet={sheet} onClose={() => {}} />, root))
    const items = root.querySelectorAll('.sheet-choice')
    expect(items).toHaveLength(8)
    expect(items[0].getAttribute('aria-current')).toBe('true')
    expect(items[0].querySelector('.badge').textContent).toBe('в порядке')
    const search = root.querySelector('input.sheet-search')
    await act(async () => {
      search.value = 'роутер-7'
      search.dispatchEvent(new Event('input', { bubbles: true }))
    })
    expect(root.querySelectorAll('.sheet-choice')).toHaveLength(1)
    await act(async () => root.querySelector('.sheet-choice').click())
    await flush()
    expect(picked).toEqual([7])
    cleanup(root)
  })

  it('без search поля поиска нет', async () => {
    const sheet = localSheet({ title: 't', body: '', choices: choices.slice(0, 3), perform: () => null })
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<Sheet sheet={sheet} onClose={() => {}} />, root))
    expect(root.querySelector('input.sheet-search')).toBe(null)
    cleanup(root)
  })
})

describe('шапка: переключатель роутера (спека п. 2.6)', () => {
  it('имя роутера -- кнопка, лист с пилюлями, смена сохраняет вкладку', async () => {
    const root = await mountAt('/miniapp/?router=2&tab=diag')
    const sw = root.querySelector('.router-switch')
    expect(sw.textContent.trim()).toBe('Дача')
    expect(root.querySelector('.app-header-fleet').textContent.trim()).toBe('Все роутеры')
    await act(async () => sw.click())
    await flush()
    const items = [...root.querySelectorAll('.sheet-choice')]
    expect(items.map((b) => b.querySelector('.sheet-choice-label').textContent)).toEqual(['Дом', 'Офис', 'Дача'])
    expect(items[1].querySelector('.badge').textContent).toBe('молчит 2 ч')
    expect(root.querySelector('input.sheet-search')).toBe(null)
    await act(async () => items[1].click())
    await flush()
    expect(root.querySelector('.stub-diag').textContent).toBe('проверки 3')
    expect(root.querySelector('.router-switch').textContent.trim()).toBe('Офис')
    expect(root.querySelector('.sheet-layer')).toBe(null)
    cleanup(root)
  })

  it('1 роутер у владельца -- переключателя нет, бренд на месте (Review Focus 2)', async () => {
    mocks.routers = { routers: [R(2, 'Дача')] }
    const root = await mountAt('/miniapp/')
    expect(root.querySelector('.router-switch')).toBe(null)
    expect(root.querySelector('.app-header-brand')).toBeTruthy()
    cleanup(root)
  })

  it('12 роутеров -- в листе есть поиск (Review Focus 2)', async () => {
    mocks.routers = { routers: Array.from({ length: 12 }, (_, i) => R(i + 1, `r-${i + 1}`)) }
    const root = await mountAt('/miniapp/?router=1')
    await act(async () => root.querySelector('.router-switch').click())
    await flush()
    expect(root.querySelector('input.sheet-search')).toBeTruthy()
    expect(root.querySelectorAll('.sheet-choice')).toHaveLength(12)
    cleanup(root)
  })

  it('на вкладке «Парк» у админа переключателя нет', async () => {
    mocks.session = { ok: true, is_admin: true, via: 'telegram' }
    const root = await mountAt('/miniapp/?router=2&tab=park')
    expect(root.querySelector('.router-switch')).toBe(null)
    cleanup(root)
  })
})

describe('заголовок слоя -- один (общие правила спеки)', () => {
  it('«Пакеты по расписанию»: H1 -- название экрана, роутер -- строкой под ним', async () => {
    const { PackagesScreen } = await import('../src/screens/PackagesScreen.jsx')
    const root = document.createElement('div')
    document.body.appendChild(root)
    // asleep: карточки не шлют status на входе -- api здесь не нужен.
    await act(async () => render(<PackagesScreen routerID={2} routerName="Дача" asleep onClose={() => {}} />, root))
    expect(root.querySelector('.overlay .screen-title').textContent).toBe('Пакеты по расписанию')
    expect(root.querySelector('.overlay .router-lastseen').textContent).toBe('Дача')
    cleanup(root)
  })
})
