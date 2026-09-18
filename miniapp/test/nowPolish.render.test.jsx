// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import SNAP from './fixtures/split_reserve_dead.json'

// v0.41, спека C1–C2: «Сейчас» без повторов и карточка тревоги с меньшим
// числом кнопок до сути.
const mocks = vi.hoisted(() => ({ router: null, checks: null, silenced: [], acked: [], muted: [] }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouter: () => Promise.resolve(mocks.router),
  fetchRouterChecks: () => Promise.resolve(mocks.checks),
  silenceIncident: (id, check, ttl) => {
    mocks.silenced.push([id, check, ttl])
    return Promise.resolve({ incident: { check_name: check, silenced_until: new Date(Date.now() + 3600e3).toISOString() } })
  },
  ackIncident: (id, check) => {
    mocks.acked.push([id, check])
    return Promise.resolve({ incident: { check_name: check, acked: true } })
  },
  muteIncident: (id, check) => {
    mocks.muted.push([id, check])
    return Promise.resolve({ incident: { check_name: check, acked: true } })
  },
}))

vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({ busy: false, result: null, error: null, errorCode: null, run: () => Promise.resolve(null) }),
}))

const { RouterDetail } = await import('../src/screens/RouterDetail.jsx')
const { Sheet } = await import('../src/ui/Sheet.jsx')
const { AppContext } = await import('../src/appContext.js')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function mount() {
  mocks.router = { router: structuredClone(SNAP.router), incidents: structuredClone(SNAP.incidents) }
  const events = structuredClone(SNAP.events)
  // Одна служебная проверка проваливается -- она обязана встать над спойлером.
  events.checks = events.checks.map((c) => (c.check_name === 'dns' ? { ...c, status: 'fail' } : c))
  mocks.checks = { ...events, traffic: { ...events.traffic, egress_tunnel_id: 'awg14', egress_tunnel_name: 'vpn-hip' } }
  const sheets = []
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () =>
    render(
      <AppContext.Provider value={{ mode: 'miniapp', wide: false }}>
        <RouterDetail id={56} openSheet={(s) => sheets.push(s)} onTab={() => {}} />
      </AppContext.Provider>,
      root,
    ),
  )
  await flush()
  return { root, sheets }
}

const cleanup = (root) => {
  render(null, root)
  root.remove()
}
const buttons = (root, text) => [...root.querySelectorAll('button')].filter((b) => b.textContent.trim() === text)

describe('«Сейчас» без повторов', () => {
  it('число туннелей -- только в плитке «VPN-туннели»', async () => {
    const { root } = await mount()
    expect(root.querySelector('.hero').textContent).not.toMatch(/VPN-туннел\S* из \d/)
    expect(root.textContent).not.toMatch(/\d+ шт\./)
    expect(root.querySelector('.stat-grid').textContent).toContain('из 2 настроенных')
    cleanup(root)
  })

  it('проваленные проверки -- отдельным списком над спойлером, в спойлере только исправные', async () => {
    const { root } = await mount()
    const failing = root.querySelector('.checks-failing')
    expect(failing.textContent).toContain('Определение адресов')
    const spoiler = root.querySelector('details.checks-spoiler')
    expect(spoiler.open).toBe(false)
    expect(spoiler.querySelectorAll('.checks-status-bad, .checks-status-danger')).toHaveLength(0)
    expect(failing.compareDocumentPosition(spoiler) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    cleanup(root)
  })

  it('пояснения к «Проверить сейчас» спрятаны под «Как это работает»', async () => {
    const { root } = await mount()
    const section = [...root.querySelectorAll('section')].find((s) => s.querySelector('.section-title')?.textContent === 'Проверить сейчас')
    const how = section.querySelector('details.compare-how')
    expect(how.querySelector('summary').textContent).toBe('Как это работает')
    expect(how.textContent).toContain('Запускает оба зонда сразу')
    // Кнопка -- до пояснений, а не после.
    const run = buttons(section, 'Сравнить адреса выхода')[0]
    expect(run.compareDocumentPosition(how) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    cleanup(root)
  })
})

describe('карточка тревоги', () => {
  it('«Починить» и «Перезапустить» -- пара, акцент один', async () => {
    const { root } = await mount()
    const pair = root.querySelector('.incident-pair')
    const repair = buttons(pair, 'Починить')[0]
    const restart = buttons(pair, 'Перезапустить VPN-туннель')[0]
    expect(repair.classList.contains('btn-accent')).toBe(true)
    expect(restart.classList.contains('btn-accent')).toBe(false)
    expect(restart.classList.contains('btn-primary')).toBe(false)
    cleanup(root)
  })

  it('пять вариантов тишины -- за одной кнопкой «Не беспокоить…» с листом', async () => {
    mocks.silenced = []
    mocks.muted = []
    const { root, sheets } = await mount()
    const card = root.querySelector('.incident-pair').closest('li')
    expect(buttons(card, 'Час')).toHaveLength(0)
    await act(async () => buttons(card, 'Не беспокоить…')[0].click())
    expect(sheets).toHaveLength(1)
    expect(sheets[0].choices.map((c) => c.label)).toEqual(['Час', '4 часа', 'Сутки', 'Понятно, вижу', 'Больше не напоминать'])

    // Лист: выбор варианта уходит на сервер, карточка обновляется.
    const host = document.createElement('div')
    document.body.appendChild(host)
    let closed = 0
    await act(async () => render(<Sheet sheet={sheets[0]} onClose={() => closed++} />, host))
    await act(async () => buttons(host, '4 часа')[0].click())
    await flush()
    expect(mocks.silenced).toEqual([[56, 'tunnel_awg10', '4h']])
    expect(closed).toBe(1)
    await flush()
    expect(card.textContent).toContain('Уведомления скрыты до')
    render(null, host)
    host.remove()
    cleanup(root)
  })

  it('«История за 24ч» -- тихая ссылка', async () => {
    const { root } = await mount()
    const link = buttons(root, 'История за 24ч')[0]
    expect(link.classList.contains('link-quiet')).toBe(true)
    expect(link.classList.contains('btn')).toBe(false)
    cleanup(root)
  })
})
