// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({
  session: null, sessionCalls: 0, sessionHashes: [], tgSession: null, routers: null, logouts: 0, logins: [], redeems: [],
}))

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  const reply = (v) => (v instanceof Error ? Promise.reject(v) : Promise.resolve(v))
  return {
    ...real,
    // Настоящий модуль -- тот же экземпляр, в котором живёт обработчик 401.
    __real: real,
    fetchSession: () => {
      mocks.sessionCalls++
      mocks.sessionHashes.push(window.location.hash)
      return reply(typeof mocks.session === 'function' ? mocks.session() : mocks.session)
    },
    createSession: () => reply(mocks.tgSession),
    fetchRouters: () => reply(mocks.routers),
    dashboardLogout: () => {
      mocks.logouts++
      return Promise.resolve(null)
    },
    redeemWebLink: (t) => {
      mocks.redeems.push(t)
      return Promise.resolve({ ok: true })
    },
    dashboardLogin: (t) => {
      mocks.logins.push(t)
      return Promise.resolve({ ok: true })
    },
  }
})

// Экраны вкладок и оверлеев -- заглушки: здесь проверяется оболочка, а не они.
vi.mock('../src/screens/RouterDetail.jsx', () => ({ RouterDetail: ({ id }) => <div class="stub">Сейчас {id}</div> }))
vi.mock('../src/screens/TunnelsTab.jsx', () => ({ TunnelsTab: () => <div class="stub">VPN-туннели</div> }))
vi.mock('../src/screens/DiagTab.jsx', () => ({ DiagTab: () => <div class="stub">Проверки</div> }))
vi.mock('../src/screens/EventsTab.jsx', () => ({ EventsTab: () => <div class="stub">Что было</div> }))
vi.mock('../src/screens/SettingsScreen.jsx', () => ({ SettingsScreen: () => <div class="stub stub-settings">Настройки роутера</div> }))
vi.mock('../src/screens/AdminOverlay.jsx', () => ({ AdminOverlay: () => <div class="stub stub-admin">Обслуживание</div> }))

const { ApiError } = await import('../src/api.js')
const { __real: real } = await import('../src/api.js')
const { App } = await import('../src/App.jsx')

const ROUTERS = [
  { id: 2, nickname: 'Дача', status: 'online', last_seen_age_sec: 40 },
  { id: 3, nickname: 'Офис', status: 'offline', last_seen_age_sec: 7200 },
]
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

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

const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)

beforeEach(() => {
  delete window.matchMedia
  mocks.session = { ok: true, telegram_user_id: 1, is_admin: true, via: 'web' }
  mocks.sessionCalls = 0
  mocks.tgSession = { ok: true, is_admin: false, via: 'telegram' }
  mocks.routers = { routers: ROUTERS }
  mocks.logouts = 0
  mocks.logins = []
  mocks.redeems = []
  mocks.sessionHashes = []
})

afterEach(() => vi.unstubAllGlobals())

describe('оболочка: веб-управление', () => {
  it('не вошёл -- экран входа, без слов про Telegram', async () => {
    mocks.session = new ApiError(401, 'unauthorized', 'x')
    const root = await mountAt('/dashboard/')
    expect(root.textContent).toContain('Токен доступа')
    expect(root.textContent).not.toContain('Telegram заново')
    cleanup(root)
  })

  it('администратор не задан -- объяснение на экране входа', async () => {
    mocks.session = new ApiError(401, 'admin_not_configured', 'x')
    const root = await mountAt('/dashboard/')
    expect(root.textContent).toContain('На сервере не задан администратор — вход в веб-управление невозможен')
    cleanup(root)
  })

  it('сервер молчит -- «Сервер не отвечает», «Повторить» переспрашивает', async () => {
    mocks.session = new TypeError('Failed to fetch')
    const root = await mountAt('/dashboard/')
    expect(root.textContent).toContain('Сервер не отвечает')
    mocks.session = { ok: true, is_admin: true, via: 'web' }
    await act(async () => button(root, 'Повторить').click())
    await flush()
    await flush()
    expect(mocks.sessionCalls).toBe(2)
    expect(root.textContent).toContain('Выйти')
    cleanup(root)
  })

  it('вход токеном ведёт в приложение, /dashboard/login уходит из адреса', async () => {
    let first = true
    mocks.session = () => {
      if (first) {
        first = false
        return new ApiError(401, 'unauthorized', 'x')
      }
      return { ok: true, is_admin: true, via: 'web' }
    }
    const root = await mountAt('/dashboard/login?router=3&tab=diag')
    const input = root.querySelector('#login-token')
    await act(async () => {
      input.value = 'tok'
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => root.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    await flush()
    await flush()
    expect(mocks.logins).toEqual(['tok'])
    expect(root.textContent).toContain('Проверки')
    expect(window.location.pathname + window.location.search).toBe('/dashboard/?router=3&tab=diag')
    cleanup(root)
  })

  it('место из адреса открывается сразу: роутер, вкладка, оверлей', async () => {
    const root = await mountAt('/dashboard/?router=2&open=settings')
    expect(root.querySelector('.stub-settings')).toBeTruthy()
    cleanup(root)
  })

  it('401 посреди работы -- «Сессия закончилась», адрес на месте', async () => {
    const root = await mountAt('/dashboard/?router=2&tab=events')
    vi.stubGlobal('fetch', async () => ({ ok: false, status: 401, json: async () => ({ code: 'unauthorized' }) }))
    await act(async () => { await real.fetchRouter(2).catch(() => {}) })
    await flush()
    expect(root.textContent).toContain('Сессия закончилась — войдите снова')
    expect(window.location.search).toBe('?router=2&tab=events')
    cleanup(root)
  })

  it('«Выйти» -- запрос выхода и экран входа', async () => {
    const root = await mountAt('/dashboard/?router=2')
    await act(async () => button(root, 'Выйти').click())
    await flush()
    expect(mocks.logouts).toBe(1)
    expect(root.textContent).toContain('Токен доступа')
    cleanup(root)
  })

  it('Esc закрывает оверлей', async () => {
    const root = await mountAt('/dashboard/?router=2&open=admin')
    expect(root.querySelector('.stub-admin')).toBeTruthy()
    await act(async () => window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })))
    await flush()
    expect(root.querySelector('.stub-admin')).toBe(null)
    expect(window.location.search).toBe('?router=2')
    cleanup(root)
  })

  it('«назад» браузера возвращает прежнюю вкладку', async () => {
    const root = await mountAt('/dashboard/?router=2')
    await act(async () => button(root, 'Что было').click())
    await flush()
    expect(window.location.search).toBe('?router=2&tab=events')
    window.history.replaceState(null, '', '/dashboard/?router=2')
    await act(async () => window.dispatchEvent(new PopStateEvent('popstate')))
    await flush()
    expect(root.textContent).toContain('Сейчас 2')
    cleanup(root)
  })
})

describe('личная ссылка и кука', () => {
  it('токен из ссылки снимается с адреса до первого запроса, даже если сервер молчит', async () => {
    mocks.session = new TypeError('Failed to fetch')
    const root = await mountAt('/dashboard/login?router=2#token=raw-1')
    expect(mocks.sessionHashes).toEqual([''])
    expect(window.location.hash).toBe('')
    expect(window.location.search).toBe('?router=2')
    expect(root.textContent).toContain('Сервер не отвечает')
    expect(mocks.redeems).toEqual([])
    // Сервер ожил: сессии нет -- токен из памяти обменивается, место открывается.
    let n = 0
    mocks.session = () => (n++ === 0 ? new ApiError(401, 'unauthorized', 'x') : { ok: true, is_admin: true, via: 'web' })
    await act(async () => button(root, 'Повторить').click())
    await flush()
    await flush()
    await flush()
    expect(mocks.redeems).toEqual(['raw-1'])
    expect(root.textContent).toContain('Сейчас 2')
    cleanup(root)
  })

  it('токен ссылки используется один раз: после выхода форма, а не повторный обмен', async () => {
    let n = 0
    mocks.session = () => (n++ === 0 ? new ApiError(401, 'unauthorized', 'x') : { ok: true, is_admin: true, via: 'web' })
    const root = await mountAt('/dashboard/login#token=raw-2')
    await flush()
    expect(mocks.redeems).toEqual(['raw-2'])
    await act(async () => button(root, 'Выйти').click())
    await flush()
    expect(root.querySelector('#login-token')).toBeTruthy()
    expect(mocks.redeems).toEqual(['raw-2'])
    cleanup(root)
  })

  it('вход прошёл, а сессии нет -- браузер не сохранил куку', async () => {
    mocks.session = new ApiError(401, 'unauthorized', 'x')
    const root = await mountAt('/dashboard/')
    const input = root.querySelector('#login-token')
    await act(async () => {
      input.value = 'tok'
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => root.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    await flush()
    await flush()
    expect(mocks.logins).toEqual(['tok'])
    expect(root.textContent).toContain('Вход прошёл, но браузер не сохранил сессию')
    cleanup(root)
  })
})

describe('оболочка: Telegram', () => {
  it('ошибка входа -- прежний текст', async () => {
    mocks.tgSession = new ApiError(401, 'unauthorized', 'x')
    const root = await mountAt('/miniapp/')
    expect(root.textContent).toContain('Не удалось войти. Откройте mini-app из Telegram заново.')
    cleanup(root)
  })

  it('нет «Выйти», адрес не меняется', async () => {
    const root = await mountAt('/miniapp/?router=2')
    expect(button(root, 'Выйти')).toBeUndefined()
    await act(async () => button(root, 'Что было').click())
    await flush()
    expect(window.location.pathname + window.location.search).toBe('/miniapp/?router=2')
    cleanup(root)
  })

  it('401 посреди работы -- в Telegram нет ни экрана входа, ни «Сессия закончилась»', async () => {
    const root = await mountAt('/miniapp/?router=2')
    vi.stubGlobal('fetch', async () => ({ ok: false, status: 401, json: async () => ({ code: 'unauthorized' }) }))
    await act(async () => { await real.fetchRouter(2).catch(() => {}) })
    await flush()
    expect(root.textContent).not.toContain('Токен доступа')
    expect(root.textContent).not.toContain('Сессия закончилась')
    expect(root.textContent).toContain('Сейчас 2')
    cleanup(root)
  })

  it('ссылка из тревоги: tab=routes и open=settings открываются и в Telegram', async () => {
    const root = await mountAt('/miniapp/?router=2&tab=routes&open=settings')
    expect(root.querySelector('.app-body .stub').textContent).toBe('VPN-туннели')
    expect(root.querySelector('.stub-settings')).toBeTruthy()
    cleanup(root)
  })
})
