// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ fleet: null, deploys: [], deployReply: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchFleet: () => Promise.resolve(mocks.fleet),
  deployBackend: (target, confirm) => {
    mocks.deploys.push({ target, confirm })
    return mocks.deployReply instanceof Error ? Promise.reject(mocks.deployReply) : Promise.resolve(mocks.deployReply)
  },
}))

const { ParkSection } = await import('../src/screens/ParkSection.jsx')
const { Sheet } = await import('../src/ui/Sheet.jsx')
const { ApiError } = await import('../src/api.js')
const { AppContext } = await import('../src/appContext.js')

const FLEET = (backend) => ({
  generated_at: '2026-09-17T10:00:00Z',
  totals: { routers: 1, online: 1, sleeping: 0, offline: 0, alerts: 0, pending_deploys: 0 },
  backend,
  routers: [{ id: 15, nickname: 'car', status: 'online', last_seen_age_sec: 30, agent_version: 'v0.35.0', pending_version: '', agent_behind: false, notify_muted: false }],
  notify: { unreachable: [], routers_without_recipients: [] },
})
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)
const cleanup = (root) => { render(null, root); root.remove() }

async function mountPark({ mode = 'telegram', withLayer = true } = {}) {
  const sheets = []
  const layers = []
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(
      <AppContext.Provider value={{ mode, wide: false }}>
        <ParkSection openSheet={(s) => sheets.push(s)} openLayer={withLayer ? (o, p) => layers.push([o, p]) : undefined} />
      </AppContext.Provider>,
      root,
    )
  })
  await flush()
  return { root, sheets, layers }
}

async function confirmSheet(sheet, typed) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<Sheet sheet={sheet} asleep={false} onClose={() => {}} />, root))
  const input = root.querySelector('#sheet-confirm-input')
  await act(async () => {
    input.value = typed
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await act(async () => [...root.querySelectorAll('.sheet-actions button')].pop().click())
  await flush()
  await flush()
  return root
}

beforeEach(() => {
  mocks.fleet = FLEET({ version: 'v0.35.0', latest_version: 'v0.36.0', update_available: true })
  mocks.deploys = []
  mocks.deployReply = { accepted: true, target_version: 'v0.36.0' }
})

describe('Парк: раскатка бэкенда', () => {
  it('есть новее -- кнопка в карточке «Бэкенд», подтверждение набором версии, дальше ожидание', async () => {
    const { root, sheets, layers } = await mountPark()
    const btn = root.querySelector('.park-backend .park-backend-actions button')
    expect(btn.textContent).toBe('Обновить бэкенд до v0.36.0')
    await act(async () => btn.click())
    expect(sheets).toHaveLength(1)
    expect(sheets[0].title).toBe('Обновить бэкенд до v0.36.0?')
    expect(sheets[0].confirmPhrase).toBe('v0.36.0')
    const sheetRoot = await confirmSheet(sheets[0], 'v0.36.0')
    expect(mocks.deploys).toEqual([{ target: 'v0.36.0', confirm: 'v0.36.0' }])
    expect(layers).toEqual([['backenddeploy', { targetVersion: 'v0.36.0' }]])
    cleanup(sheetRoot)
    cleanup(root)
  })

  it('нечего раскатывать -- кнопки нет', async () => {
    mocks.fleet = FLEET({ version: 'v0.36.0', latest_version: 'v0.36.0', update_available: false })
    const { root } = await mountPark()
    expect(root.querySelector('.park-backend-actions')).toBe(null)
    cleanup(root)
  })

  it('отказ сервера -- слова на листе, ожидания нет', async () => {
    mocks.deployReply = new ApiError(503, 'backend_update_not_configured', 'x')
    const { root, sheets, layers } = await mountPark()
    await act(async () => root.querySelector('.park-backend-actions button').click())
    const sheetRoot = await confirmSheet(sheets[0], 'v0.36.0')
    expect(sheetRoot.textContent).toContain('Раскатка бэкенда на этом сервере не настроена')
    expect(layers).toEqual([])
    cleanup(sheetRoot)
    cleanup(root)
  })
})

describe('Парк: добавить роутер', () => {
  it('кнопка открывает мастер', async () => {
    const { root, layers } = await mountPark()
    await act(async () => button(root, 'Добавить роутер').click())
    expect(layers).toEqual([['provision', undefined]])
    cleanup(root)
  })

  it('без openLayer (старые вызовы) новых кнопок нет', async () => {
    const { root } = await mountPark({ withLayer: false })
    expect(button(root, 'Добавить роутер')).toBeFalsy()
    expect(root.querySelector('.park-backend-actions')).toBe(null)
    cleanup(root)
  })

  it('web: мостика в классическое веб-управление больше нет', async () => {
    const { root } = await mountPark({ mode: 'web' })
    expect(root.querySelector('a.park-classic')).toBe(null)
    expect(root.textContent).not.toContain('классическом веб-управлении')
    expect(button(root, 'Открыть в браузере')).toBeFalsy()
    cleanup(root)
  })
})
