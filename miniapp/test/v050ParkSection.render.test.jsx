// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ fleet: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchFleet: () => Promise.resolve(mocks.fleet),
  fetchAccess: () => Promise.resolve({ owner: null, operators: [] }),
}))

const { ParkTab } = await import('../src/screens/ParkTab.jsx')
const { FleetOverlay } = await import('../src/screens/FleetOverlay.jsx')
const { AppContext } = await import('../src/appContext.js')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const R = (over) => ({ id: 0, nickname: '', status: 'online', last_seen_age_sec: 30, agent_version: 'v0.49.0', pending_version: '', agent_behind: false, agent_update_warning: '', notify_muted: false, ...over })
const LIST = [
  R({ id: 1, nickname: 'ok-router' }),
  R({ id: 2, nickname: 'behind', agent_version: 'v0.47.0', agent_behind: true, agent_update_warning: 'проверяет адрес загрузки' }),
  R({ id: 3, nickname: 'silent', status: 'offline', last_seen_age_sec: 90000 }),
  R({ id: 4, nickname: 'alarm', status: 'alert', active_incidents: [{ check_name: 'dns' }] }),
]
const FLEET = (routers = LIST) => ({
  generated_at: '2026-09-29T10:00:00Z',
  totals: { routers: routers.length },
  backend: { version: 'v0.49.0', latest_version: '', update_available: false },
  routers,
  notify: { unreachable: [], routers_without_recipients: [] },
})

async function mountPark(routers = LIST) {
  mocks.fleet = FLEET(routers)
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () =>
    render(
      <AppContext.Provider value={{ mode: 'miniapp', wide: false }}>
        <ParkTab routers={routers} onPick={() => {}} openSheet={() => {}} openLayer={() => {}} onOpenConnection={() => {}} />
      </AppContext.Provider>,
      root,
    ),
  )
  await flush()
  await flush()
  return root
}
const cleanup = (root) => {
  render(null, root)
  root.remove()
}
const visibleButtons = (el) => [...el.querySelectorAll('button')].filter((b) => !b.closest('[hidden]') && !b.closest('details:not([open]) > .fold-body'))

describe('Парк как пульт (спека пакет 2)', () => {
  it('сводка -- три плитки с согласованием, строки «N роутеров: …» нет', async () => {
    const root = await mountPark()
    const labels = [...root.querySelectorAll('.fleet-count-label')].map((l) => l.textContent)
    expect(labels).toEqual(['в порядке', 'тревога', 'молчит'])
    expect(root.textContent).not.toMatch(/\d+ роутер(а|ов)?: /)
    cleanup(root)
  })

  it('входы «Свои VPN-серверы» и «Добавить роутер» -- над карточками', async () => {
    const root = await mountPark()
    const entries = root.querySelector('.park-entries')
    expect([...entries.querySelectorAll('button')].map((b) => b.textContent.trim())).toEqual(['Свои VPN-серверы', 'Добавить роутер'])
    const firstCard = root.querySelector('.park-row')
    expect(entries.compareDocumentPosition(firstCard) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    cleanup(root)
  })

  it('порядок карточек: тревога → молчит → отстаёт → остальные', async () => {
    const root = await mountPark()
    const names = [...root.querySelectorAll('.park-row .data-row-main')].map((n) => n.textContent)
    expect(names).toEqual(['alarm', 'silent', 'behind', 'ok-router'])
    cleanup(root)
  })

  it('карточка: пилюля как в «Мои роутеры», одна строка, «Открыть роутер» и «Ещё ▸»', async () => {
    const root = await mountPark()
    const card = [...root.querySelectorAll('.park-row')].find((c) => c.querySelector('.data-row-main').textContent === 'silent')
    const pill = card.querySelector('.badge').textContent
    const list = document.createElement('div')
    document.body.appendChild(list)
    await act(async () => render(<FleetOverlay routers={LIST} currentID={null} onPick={() => {}} />, list))
    const listPill = [...list.querySelectorAll('.fleet-row')].find((r) => r.textContent.includes('silent')).querySelector('.badge').textContent
    expect(pill).toBe(listPill)
    cleanup(list)
    expect(card.querySelectorAll('.park-card-line')).toHaveLength(1)
    expect(visibleButtons(card).map((b) => b.textContent.trim())).toEqual(['Открыть роутер', 'Ещё ▸'])
    expect(card.querySelector('.park-tag')).toBe(null)
    cleanup(root)
  })

  it('«Ещё ▸» раскрывает остальные действия', async () => {
    const root = await mountPark()
    const card = [...root.querySelectorAll('.park-row')].find((c) => c.querySelector('.data-row-main').textContent === 'behind')
    const more = [...card.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Ещё ▸')
    expect(card.querySelector('.park-more-body').hidden).toBe(true)
    await act(async () => more.click())
    expect(card.querySelector('.park-more-body').hidden).toBe(false)
    expect(more.getAttribute('aria-expanded')).toBe('true')
    expect(visibleButtons(card).map((b) => b.textContent.trim())).toContain('Обновить агент')
    cleanup(root)
  })

  it('оговорки -- одной свёрнутой строкой над списком', async () => {
    const root = await mountPark()
    const fold = root.querySelector('details.park-warnings')
    expect(fold.querySelector('.fold-title').textContent).toBe('Что может помешать обновлению · 1')
    expect(fold.open).toBe(false)
    expect(fold.textContent).toContain('проверяет адрес загрузки')
    cleanup(root)
  })

  it('массовые -- сеткой, «Обновить всех отставших» -- единственный лайм', async () => {
    const root = await mountPark()
    const grid = root.querySelector('.park-batch')
    expect(grid.className).toContain('action-row-pair')
    expect([...grid.querySelectorAll('button')].map((b) => b.textContent.trim())).toEqual(['Обновить всех отставших (1)', 'Опросить все', 'Проверить все', 'Аудит всех'])
    expect([...root.querySelectorAll('.btn-primary')].map((b) => b.textContent.trim())).toEqual(['Обновить всех отставших (1)'])
    cleanup(root)
  })

  it('Парк с одним роутером: «Опросить все» нет, сетка на месте (Review Focus 2)', async () => {
    const root = await mountPark([R({ id: 1, nickname: 'solo' })])
    const grid = root.querySelector('.park-batch')
    expect([...grid.querySelectorAll('button')].map((b) => b.textContent.trim())).toEqual(['Проверить все', 'Аудит всех'])
    expect(root.querySelectorAll('.btn-primary')).toHaveLength(0)
    cleanup(root)
  })
})
