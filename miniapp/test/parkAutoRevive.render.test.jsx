// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Парк, v0.45: пароль root сохранён для авто-оживления, «Забыть пароль»,
// авто-оживление помечено, причина, почему оно не начнётся.
const mocks = vi.hoisted(() => ({ fleet: null, forgets: [], forgetReply: null, cancels: [] }))

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  const reply = (v) => (v instanceof Error ? Promise.reject(v) : Promise.resolve(v))
  return {
    ...real,
    fetchFleet: () => Promise.resolve(mocks.fleet),
    forgetRouterCredentials: (id) => {
      mocks.forgets.push(id)
      return reply(mocks.forgetReply)
    },
    cancelRouterAgentRevive: (id) => {
      mocks.cancels.push(id)
      return reply({ cleared: true })
    },
  }
})

const { ParkSection } = await import('../src/screens/ParkSection.jsx')
const { Sheet } = await import('../src/ui/Sheet.jsx')
const { AppContext } = await import('../src/appContext.js')

const router = (over) => ({
  id: 0, nickname: '', status: 'online', last_seen_age_sec: 30, agent_version: 'v0.45.0',
  pending_version: '', pending_attempts: 0, pending_last_error_text: '', agent_behind: false,
  agent_update_warning: '', notify_muted: false, away: false, panel_address_known: true, revive: null,
  pending_since: null, last_deploy: null, incident: null, root_password_saved: false, auto_revive_blocked: '', ...over,
})

const FLEET = {
  generated_at: '2026-09-18T10:00:40Z',
  totals: { routers: 3, online: 1, sleeping: 0, offline: 2, alerts: 0, pending_deploys: 0 },
  backend: { version: 'v0.45.0', latest_version: '', update_available: false },
  routers: [
    router({
      id: 31, nickname: 'oldcar', status: 'offline', away: true, last_seen_age_sec: 3456000, agent_version: 'v0.12.0',
      root_password_saved: true,
      revive: { status: 'waiting', expires_at: '2026-10-18T10:00:00Z', attempts: 0, last_error_text: '', last_probe_text: '', last_probe_at: '', auto: true },
    }),
    router({
      id: 32, nickname: 'dacha', status: 'offline', away: true, last_seen_age_sec: 3456000, agent_version: 'v0.40.0',
      auto_revive_blocked: 'для авто-оживления нужен пароль root',
    }),
    router({ id: 33, nickname: 'home' }),
  ],
  notify: { unreachable: [], routers_without_recipients: [] },
  revive_enabled: true,
}

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function mountPark() {
  const sheets = []
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(
      <AppContext.Provider value={{ mode: 'telegram', wide: false }}>
        <ParkSection openSheet={(s) => sheets.push(s)} />
      </AppContext.Provider>,
      root,
    )
  })
  await flush()
  return { root, sheets }
}

async function mountSheet(sheet) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(<Sheet sheet={sheet} asleep={false} onClose={() => {}} />, root)
  })
  return root
}

const primary = (root) => [...root.querySelectorAll('.sheet-actions button')].pop()
const buttons = (root, label) => [...root.querySelectorAll('button')].filter((b) => b.textContent === label)
const rowOf = (root, name) => [...root.querySelectorAll('.park-row')].find((r) => r.querySelector('.data-row-main')?.textContent === name)
const cleanup = (root) => { render(null, root); root.remove() }

beforeEach(() => {
  mocks.fleet = FLEET
  mocks.forgets = []
  mocks.forgetReply = { cleared: true, revive_cancelled: true }
  mocks.cancels = []
})

describe('Парк: сохранённый пароль и авто-оживление', () => {
  it('авто-оживление помечено, отмена та же', async () => {
    const { root } = await mountPark()
    const row = rowOf(root, 'oldcar')
    expect(row.textContent).toContain('оживление: поставлено автоматически · ждёт роутер')
    expect(buttons(row, 'Отменить оживление')).toHaveLength(1)
    cleanup(root)
  })

  it('пароль сохранён -- признак и «Забыть пароль»; где не сохранён -- ни того, ни другого', async () => {
    const { root } = await mountPark()
    const old = rowOf(root, 'oldcar')
    expect(old.textContent).toContain('пароль root сохранён для авто-оживления')
    expect(buttons(old, 'Забыть пароль')).toHaveLength(1)
    for (const name of ['dacha', 'home']) {
      const row = rowOf(root, name)
      expect(row.textContent).not.toContain('пароль root сохранён')
      expect(buttons(row, 'Забыть пароль')).toHaveLength(0)
    }
    cleanup(root)
  })

  it('причина, почему авто-оживление не начнётся, -- в строке; обычная кнопка «Оживить агент» на месте', async () => {
    const { root } = await mountPark()
    const row = rowOf(root, 'dacha')
    expect(row.textContent).toContain('агент давно не обновлялся — для авто-оживления нужен пароль root')
    expect(buttons(row, 'Оживить агент')).toHaveLength(1)
    expect(rowOf(root, 'home').textContent).not.toContain('давно не обновлялся')
    cleanup(root)
  })

  it('«Забыть пароль»: лист без набора имени, запрос, итог словами, список перечитан', async () => {
    const { root, sheets } = await mountPark()
    mocks.fleet = { ...FLEET, routers: FLEET.routers.map((r) => (r.id === 31 ? { ...r, root_password_saved: false, revive: null } : r)) }
    await act(async () => buttons(rowOf(root, 'oldcar'), 'Забыть пароль')[0].click())
    const sheet = sheets[0]
    expect(sheet.title).toBe('Забыть пароль root для «oldcar»?')
    expect(sheet.confirmPhrase ?? '').toBe('')
    const s = await mountSheet(sheet)
    await act(async () => primary(s).click())
    await flush()
    expect(mocks.forgets).toEqual([31])
    expect(root.textContent).toContain('Пароль root для «oldcar» стёрт, авто-оживление снято.')
    expect(rowOf(root, 'oldcar').textContent).not.toContain('пароль root сохранён')
    cleanup(s)
    cleanup(root)
  })

  it('отмена авто-оживления: лист честно говорит, что пароль останется', async () => {
    const { root, sheets } = await mountPark()
    await act(async () => buttons(rowOf(root, 'oldcar'), 'Отменить оживление')[0].click())
    expect(sheets[0].body).toContain('Сохранённый пароль root останется')
    cleanup(root)
  })
})
