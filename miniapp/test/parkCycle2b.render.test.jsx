// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Парк, цикл 2 (часть 3): сторож, отложенное, своя версия, переустановка,
// перенаправление. Моки заведены на все задачи сразу -- задачи 3 и 4
// дописывают сюда свои describe.
const mocks = vi.hoisted(() => ({
  fleet: null,
  updates: [],
  updateReply: null,
  reinstalls: [],
  reinstallReply: null,
  repoints: [],
  repointReply: null,
  conn: { awgm_url: 'https://router.example.com' },
}))

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  const reply = (v) => (v instanceof Error ? Promise.reject(v) : Promise.resolve(v))
  return {
    ...real,
    fetchFleet: () => Promise.resolve(mocks.fleet),
    fetchAccess: () => Promise.resolve({ owner: null, operators: [] }),
    fetchAgentConnection: () => Promise.resolve(mocks.conn),
    updateRouterAgent: (id, confirm, target, allow) => {
      mocks.updates.push({ id, confirm, target, allow })
      return reply(mocks.updateReply)
    },
    reinstallRouterAgent: (id, body) => {
      mocks.reinstalls.push({ id, body })
      return reply(mocks.reinstallReply)
    },
    repointRouterAgent: (id, body) => {
      mocks.repoints.push({ id, body })
      return reply(mocks.repointReply)
    },
  }
})

const { ParkSection } = await import('../src/screens/ParkSection.jsx')
const { Sheet } = await import('../src/ui/Sheet.jsx')
const { AppContext } = await import('../src/appContext.js')

const router = (over) => ({
  id: 0, nickname: '', status: 'online', last_seen_age_sec: 30, agent_version: 'v0.35.0',
  pending_version: '', pending_attempts: 0, pending_last_error_text: '', agent_behind: false,
  agent_update_warning: '', notify_muted: false, away: false, panel_address_known: true, revive: null,
  pending_since: null, last_deploy: null, incident: null, ...over,
})

export const FLEET = {
  generated_at: '2026-09-17T10:00:40Z',
  totals: { routers: 3, online: 1, sleeping: 0, offline: 1, alerts: 1, pending_deploys: 1 },
  backend: { version: 'v0.36.0', latest_version: '', update_available: false },
  routers: [
    router({ id: 21, nickname: 'bronya', status: 'offline', away: true, last_seen_age_sec: 345600, agent_version: 'v0.34.0', pending_version: 'v0.36.0', agent_behind: true, pending_since: '2026-09-12T14:20:00Z' }),
    router({ id: 22, nickname: 'home', status: 'alert', incident: { hard_since: '2026-09-17T06:00:00Z', fail_count: 5 }, last_deploy: { version: 'v0.35.0', at: '2026-09-15T08:05:00Z', ok: true } }),
    router({ id: 23, nickname: 'car', agent_version: 'v0.36.0' }),
  ],
  notify: { unreachable: [], routers_without_recipients: [] },
  watchdog: { alive: true, reason: 'обход 40s назад', last_scan_at: '2026-09-17T10:00:00Z', offline_errors: 0, scans_total: 1234, stale_users: 2, suppressed_users: 1, last_scan_ms: 85 },
  revive_enabled: true,
}

export const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

export async function mountPark({ mode = 'telegram', openLayer, onOpenConnection } = {}) {
  const sheets = []
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(
      <AppContext.Provider value={{ mode, wide: false }}>
        <ParkSection openSheet={(s) => sheets.push(s)} openLayer={openLayer} onOpenConnection={onOpenConnection} />
      </AppContext.Provider>,
      root,
    )
  })
  await flush()
  return { root, sheets }
}

export async function mountSheet(sheet) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  let closed = 0
  await act(async () => {
    render(<Sheet sheet={sheet} asleep={false} onClose={() => closed++} />, root)
  })
  return { root, closed: () => closed }
}

export async function fill(root, selector, value) {
  const el = root.querySelector(selector)
  await act(async () => {
    if (el.type === 'checkbox') {
      el.checked = value
      el.dispatchEvent(new Event('change', { bubbles: true }))
    } else {
      el.value = value
      el.dispatchEvent(new Event('input', { bubbles: true }))
    }
  })
}

export const primary = (root) => [...root.querySelectorAll('.sheet-actions button')].pop()
export const buttons = (root, label) => [...root.querySelectorAll('button')].filter((b) => b.textContent === label)
export const rowOf = (root, name) => [...root.querySelectorAll('.park-row')].find((r) => r.querySelector('.data-row-main')?.textContent === name)
export const cleanup = (root) => { render(null, root); root.remove() }

beforeEach(() => {
  mocks.fleet = FLEET
  mocks.updates = []
  mocks.updateReply = { queued: true, deferred: false, target_version: 'v0.34.0' }
  mocks.reinstalls = []
  mocks.reinstallReply = { job_id: 'job-1' }
  mocks.repoints = []
  mocks.repointReply = { job_id: 'job-2' }
  mocks.conn = { awgm_url: 'https://router.example.com' }
})

describe('Парк: сторож и отложенное', () => {
  it('строка «Сторож» -- в карточке бэкенда, старой строки внизу нет', async () => {
    const { root } = await mountPark()
    const wd = root.querySelector('.park-watchdog')
    expect(wd.querySelector('.park-watchdog-line').textContent).toBe('Сторож: последний обход 40 с назад · молчат 2 · заглушено 1')
    expect(wd.textContent).toContain('1234 обхода с запуска')
    expect(wd.classList.contains('park-watchdog-ok')).toBe(true)
    expect(root.textContent).not.toContain('Сторож парка:')
    cleanup(root)
  })

  it('мёртвый сторож -- причина красной строкой', async () => {
    mocks.fleet = { ...FLEET, watchdog: { ...FLEET.watchdog, alive: false, reason: 'сторож не обходил парк 5m0s' } }
    const { root } = await mountPark()
    expect(root.querySelector('.park-watchdog .state-error').textContent).toBe('сторож не обходил парк 5m0s')
    cleanup(root)
  })

  it('в строках роутеров -- ожидание, раскатка и тревога со временем', async () => {
    const { root } = await mountPark()
    expect(rowOf(root, 'bronya').textContent).toMatch(/ждёт обновления с \d\d\.\d\d \d\d:\d\d/)
    const home = rowOf(root, 'home').textContent
    expect(home).toMatch(/последняя раскатка v0\.35\.0 · \d\d\.\d\d \d\d:\d\d · прошла/)
    expect(home).toMatch(/тревога с \d\d\.\d\d \d\d:\d\d \(5 раз\)/)
    expect(rowOf(root, 'car').querySelectorAll('.park-delay')).toHaveLength(0)
    cleanup(root)
  })
})

describe('Парк: ссылка на аварийную страницу вместо мостика на классическое', () => {
  it('web: ни «Открыть в браузере», ни /dashboard/classic/, есть «Аварийная страница» в карточке бэкенда', async () => {
    const { root } = await mountPark({ mode: 'web' })
    expect(buttons(root, 'Открыть в браузере')).toEqual([])
    expect(root.querySelector('a[href^="/dashboard/classic"]')).toBe(null)
    expect(root.textContent).not.toContain('классическом')
    const link = root.querySelector('.park-backend a.park-rescue')
    expect(link).not.toBe(null)
    expect(link.getAttribute('href')).toBe('/dashboard/rescue/')
    expect(link.textContent).toBe('Аварийная страница')
    cleanup(root)
  })

  it('Telegram: «Открыть в браузере» на месте, ссылки на аварийную страницу нет', async () => {
    const { root } = await mountPark({ mode: 'telegram' })
    expect(buttons(root, 'Открыть в браузере')).toHaveLength(1)
    expect(root.querySelector('a[href^="/dashboard/rescue"]')).toBe(null)
    cleanup(root)
  })
})

describe('Парк: другая версия агента', () => {
  it('кнопка -- у роутеров с известной версией и без отложенного обновления', async () => {
    const { root } = await mountPark()
    expect(buttons(rowOf(root, 'home'), 'Другая версия…')).toHaveLength(1)
    expect(buttons(rowOf(root, 'car'), 'Другая версия…')).toHaveLength(1)
    expect(buttons(rowOf(root, 'bronya'), 'Другая версия…')).toHaveLength(0)
    cleanup(root)
  })

  it('откат: переключатель появляется, без него кнопка погашена; запрос с allow_downgrade', async () => {
    const { root, sheets } = await mountPark()
    await act(async () => buttons(rowOf(root, 'home'), 'Другая версия…')[0].click())
    expect(sheets).toHaveLength(1)
    expect(sheets[0].confirmPhrase).toBe('home')
    const s = await mountSheet(sheets[0])
    expect(s.root.querySelector('#sheet-field-allow_downgrade')).toBe(null)
    await fill(s.root, '#sheet-field-target_version', '0.34.0')
    expect(s.root.querySelector('.sheet-field-hint').textContent).toBe('Это откат: на роутере v0.35.0.')
    await fill(s.root, '#sheet-confirm-input', 'home')
    expect(primary(s.root).disabled).toBe(true)
    await fill(s.root, '#sheet-field-allow_downgrade', true)
    expect(primary(s.root).disabled).toBe(false)
    await act(async () => primary(s.root).click())
    await flush()
    await flush()
    expect(mocks.updates).toEqual([{ id: 22, confirm: 'home', target: 'v0.34.0', allow: true }])
    expect(root.textContent).toContain('Обновление «home» до v0.34.0 отправлено на роутер.')
    cleanup(s.root)
    cleanup(root)
  })

  it('новее бэкенда -- подсказка и погашенная кнопка', async () => {
    const { root, sheets } = await mountPark()
    await act(async () => buttons(rowOf(root, 'home'), 'Другая версия…')[0].click())
    const s = await mountSheet(sheets[0])
    await fill(s.root, '#sheet-field-target_version', 'v0.37.0')
    await fill(s.root, '#sheet-confirm-input', 'home')
    expect(s.root.querySelector('.sheet-field-hint').textContent).toBe('Выпуска новее бэкенда (v0.36.0) нет.')
    expect(primary(s.root).disabled).toBe(true)
    cleanup(s.root)
    cleanup(root)
  })

  it('отказ downgrade_rejected -- словами спеки', async () => {
    const { ApiError } = await import('../src/api.js')
    mocks.updateReply = new ApiError(400, 'downgrade_rejected', '/routers/22/agent/update failed: 400', 'Это откат версии — подтвердите откат')
    const { root, sheets } = await mountPark()
    await act(async () => buttons(rowOf(root, 'home'), 'Другая версия…')[0].click())
    const s = await mountSheet(sheets[0])
    await fill(s.root, '#sheet-field-target_version', 'v0.36.0')
    await fill(s.root, '#sheet-confirm-input', 'home')
    await act(async () => primary(s.root).click())
    await flush()
    expect(s.root.querySelector('.state-error').textContent).toBe('Это откат версии — подтвердите откат.')
    cleanup(s.root)
    cleanup(root)
  })
})

const SECRET = 'root-Пароль-9f3kq'

describe('Парк: переустановить агент сейчас', () => {
  it('кнопка -- только у роутеров на связи', async () => {
    const { root } = await mountPark({ openLayer: () => {} })
    expect(buttons(rowOf(root, 'home'), 'Переустановить агент')).toHaveLength(1)
    expect(buttons(rowOf(root, 'car'), 'Переустановить агент')).toHaveLength(1)
    expect(buttons(rowOf(root, 'bronya'), 'Переустановить агент')).toHaveLength(0)
    cleanup(root)
  })

  it('пароль и ник -- запуск, «Ход работы» открыт; пароля нет ни в листе, ни в консоли', async () => {
    const opened = []
    const log = vi.spyOn(console, 'log')
    const error = vi.spyOn(console, 'error')
    const { root, sheets } = await mountPark({ openLayer: (overlay, params) => opened.push([overlay, params]) })
    await act(async () => buttons(rowOf(root, 'home'), 'Переустановить агент')[0].click())
    const sheet = sheets[0]
    expect(sheet.confirmPhrase).toBe('home')
    expect(sheet.danger).toBe(true)
    expect(sheet.note).toBe('Пароли уходят на сервер один раз и не сохраняются.')
    const s = await mountSheet(sheet)
    for (const input of s.root.querySelectorAll('input')) {
      expect(input.getAttribute('autocomplete')).toBe(input.type === 'password' ? 'new-password' : 'off')
    }
    await fill(s.root, '#sheet-confirm-input', 'home')
    expect(primary(s.root).disabled).toBe(true)
    await fill(s.root, '#sheet-field-root_password', SECRET)
    expect(primary(s.root).disabled).toBe(false)
    await act(async () => primary(s.root).click())
    await flush()
    await flush()
    expect(mocks.reinstalls).toEqual([
      { id: 22, body: { root_password: SECRET, awgm_login: '', awgm_password: '', awgm_api_key: '', version: '', confirm: 'home' } },
    ])
    expect(opened).toEqual([['job', { jobId: 'job-1', title: 'Переустановка агента на «home»' }]])
    expect(JSON.stringify(sheet)).not.toContain(SECRET)
    expect(JSON.stringify(opened)).not.toContain(SECRET)
    for (const call of [...log.mock.calls, ...error.mock.calls]) expect(JSON.stringify(call)).not.toContain(SECRET)
    log.mockRestore()
    error.mockRestore()
    cleanup(s.root)
    cleanup(root)
  })

  it('отказ сервера -- его русское сообщение на листе, «Ход работы» не открыт', async () => {
    const { ApiError } = await import('../src/api.js')
    mocks.reinstallReply = new ApiError(409, 'router_offline', 'x', 'Роутер не на связи — переустановка сейчас невозможна.')
    const opened = []
    const { root, sheets } = await mountPark({ openLayer: (overlay, params) => opened.push([overlay, params]) })
    await act(async () => buttons(rowOf(root, 'home'), 'Переустановить агент')[0].click())
    const s = await mountSheet(sheets[0])
    await fill(s.root, '#sheet-field-root_password', 'pw')
    await fill(s.root, '#sheet-confirm-input', 'home')
    await act(async () => primary(s.root).click())
    await flush()
    expect(s.root.querySelector('.state-error').textContent).toBe('Роутер не на связи — переустановка сейчас невозможна.')
    expect(opened).toEqual([])
    cleanup(s.root)
    cleanup(root)
  })
})

async function mountAdmin(openLayer, onOpenAgentConnection) {
  const { RouterAdminSections } = await import('../src/screens/RouterAdminSections.jsx')
  const sheets = []
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(
      <RouterAdminSections routerID={22} routerName="home" isAdmin openSheet={(s) => sheets.push(s)} openLayer={openLayer} onOpenAgentConnection={onOpenAgentConnection} />,
      root,
    )
  })
  await flush()
  return { root, sheets }
}

describe('Управление: перенаправить агента', () => {

  it('свёрнуто под «Опасное»; адрес без https не пускает; запуск открывает «Ход работы»', async () => {
    const opened = []
    const { root, sheets } = await mountAdmin((overlay, params) => opened.push([overlay, params]))
    const zone = root.querySelector('details.danger-zone')
    expect(zone.open).toBe(false)
    expect(zone.querySelector('summary').textContent).toBe('Опасное')
    expect(zone.textContent).toContain('Агент начнёт отправлять отчёты на другой сервер. Этот сервер перестанет его видеть.')
    await act(async () => buttons(zone, 'Перенаправить агента')[0].click())
    const s = await mountSheet(sheets[0])
    expect(sheets[0].confirmPhrase).toBe('home')
    await fill(s.root, '#sheet-field-root_password', 'pw')
    await fill(s.root, '#sheet-confirm-input', 'home')
    expect(s.root.querySelector('.sheet-field-hint').textContent).toBe('Пусто — текущий публичный адрес этого сервера.')
    await fill(s.root, '#sheet-field-new_backend_url', 'http://wg2.example.com')
    expect(primary(s.root).disabled).toBe(true)
    await fill(s.root, '#sheet-field-new_backend_url', 'https://wg2.example.com')
    expect(primary(s.root).disabled).toBe(false)
    await act(async () => primary(s.root).click())
    await flush()
    await flush()
    expect(mocks.repoints).toEqual([
      { id: 22, body: { root_password: 'pw', new_backend_url: 'https://wg2.example.com', awgm_login: '', awgm_password: '', awgm_api_key: '', confirm: 'home' } },
    ])
    expect(opened).toEqual([['job', { jobId: 'job-2', title: 'Перенаправление агента «home»' }]])
    cleanup(s.root)
    cleanup(root)
  })

  it('не админу «Опасного» нет', async () => {
    const { RouterAdminSections } = await import('../src/screens/RouterAdminSections.jsx')
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<RouterAdminSections routerID={22} routerName="home" isAdmin={false} openSheet={() => {}} />, root))
    await flush()
    expect(root.querySelector('details.danger-zone')).toBe(null)
    cleanup(root)
  })
})

describe('без адреса панели awg-manager', () => {
  it('Парк: вместо «Переустановить агент» -- строка и переход в «Подключение агента»', async () => {
    mocks.fleet = { ...FLEET, routers: FLEET.routers.map((r) => (r.nickname === 'car' ? { ...r, panel_address_known: false } : r)) }
    const opened = []
    const { root } = await mountPark({ openLayer: () => {}, onOpenConnection: (id) => opened.push(id) })
    const car = rowOf(root, 'car')
    expect(buttons(car, 'Переустановить агент')).toHaveLength(0)
    expect(car.textContent).toContain('Сначала задайте адрес панели в «Подключении агента».')
    await act(async () => buttons(car, 'Подключение агента')[0].click())
    expect(opened).toEqual([23])
    expect(buttons(rowOf(root, 'home'), 'Переустановить агент')).toHaveLength(1)
    cleanup(root)
  })

  it('Обслуживание: адреса нет -- вместо «Перенаправить агента» строка и переход', async () => {
    mocks.conn = { awgm_url: '' }
    let openedConn = 0
    const { root } = await mountAdmin(() => {}, () => openedConn++)
    const zone = root.querySelector('details.danger-zone')
    expect(buttons(zone, 'Перенаправить агента')).toHaveLength(0)
    expect(zone.textContent).toContain('Сначала задайте адрес панели в «Подключении агента».')
    await act(async () => buttons(zone, 'Подключение агента')[0].click())
    expect(openedConn).toBe(1)
    cleanup(root)
  })
})

describe('«Другая версия…»: сервер считает откатом то, что клиент -- обновлением', () => {
  it('после downgrade_rejected появляется переключатель, повтор уходит с allow_downgrade', async () => {
    const { ApiError } = await import('../src/api.js')
    mocks.updateReply = new ApiError(400, 'downgrade_rejected', 'x', 'Это откат версии — подтвердите откат')
    const { root, sheets } = await mountPark()
    await act(async () => buttons(rowOf(root, 'home'), 'Другая версия…')[0].click())
    const s = await mountSheet(sheets[0])
    await fill(s.root, '#sheet-field-target_version', 'v0.36.0')
    await fill(s.root, '#sheet-confirm-input', 'home')
    expect(s.root.querySelector('#sheet-field-allow_downgrade')).toBe(null)
    await act(async () => primary(s.root).click())
    await flush()
    expect(s.root.querySelector('#sheet-field-allow_downgrade')).not.toBe(null)
    expect(primary(s.root).disabled).toBe(true)
    mocks.updateReply = { queued: true, deferred: false, target_version: 'v0.36.0' }
    await fill(s.root, '#sheet-field-allow_downgrade', true)
    await act(async () => primary(s.root).click())
    await flush()
    expect(mocks.updates.at(-1)).toEqual({ id: 22, confirm: 'home', target: 'v0.36.0', allow: true })
    cleanup(s.root)
    cleanup(root)
  })
})
