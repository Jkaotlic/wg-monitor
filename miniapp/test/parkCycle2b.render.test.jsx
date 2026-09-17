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
}))

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  const reply = (v) => (v instanceof Error ? Promise.reject(v) : Promise.resolve(v))
  return {
    ...real,
    fetchFleet: () => Promise.resolve(mocks.fleet),
    fetchAccess: () => Promise.resolve({ owner: null, operators: [] }),
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

export async function mountPark({ mode = 'telegram', openLayer } = {}) {
  const sheets = []
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(
      <AppContext.Provider value={{ mode, wide: false }}>
        <ParkSection openSheet={(s) => sheets.push(s)} openLayer={openLayer} />
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

describe('Парк: мостика на классическое веб-управление больше нет', () => {
  it('web: ни «Открыть в браузере», ни ссылки на /dashboard/classic/', async () => {
    const { root } = await mountPark({ mode: 'web' })
    expect(buttons(root, 'Открыть в браузере')).toEqual([])
    expect(root.querySelector('a[href^="/dashboard/classic"]')).toBe(null)
    expect(root.textContent).not.toContain('классическом')
    cleanup(root)
  })

  it('Telegram: «Открыть в браузере» на месте', async () => {
    const { root } = await mountPark({ mode: 'telegram' })
    expect(buttons(root, 'Открыть в браузере')).toHaveLength(1)
    cleanup(root)
  })
})
