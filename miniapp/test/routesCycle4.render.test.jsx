// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// «Маршруты», цикл 4: перенос из «Напрямую (WAN)», честный текст об обратном
// переносе, переход с экрана VPN-туннеля и блок HydraRoute Neo.
const mocks = vi.hoisted(() => ({ calls: [], answers: {}, role: 'owner' }))

vi.mock('../src/useCommand.js', async () => {
  const { useState } = await import('preact/hooks')
  return {
    useCommand: () => {
      const [state, setState] = useState({ busy: false, result: null, error: null, errorCode: null, sleepNote: '' })
      return {
        ...state,
        run: (action, args) => {
          mocks.calls.push({ action, args })
          const res = mocks.answers[action] ?? null
          setState({ busy: false, result: res, error: null, errorCode: null, sleepNote: '' })
          return Promise.resolve(res)
        },
      }
    },
  }
})

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve({ role: mocks.role }),
}))

const { RoutesTab } = await import('../src/screens/RoutesTab.jsx')
const { HRNEO_TEXTS } = await import('../src/hrneoBlock.js')

const SNAP = {
  policy_model: true,
  default_egress: 'direct',
  hr_neo: { installed: true, running: true },
  tunnels: [
    { id: 'nwg1', name: 'amsterdam', iface: 'nwg1', type: 'managed', status: 'running' },
    { id: 'nwg2', name: 'spare', iface: 'nwg2', type: 'managed', status: 'running' },
    { id: 'nwg3', name: 'old-home', iface: 'nwg3', type: 'managed', status: 'running' },
  ],
  counts: { nwg1: { dns: 2, static: 1, hr_neo: 2 }, nwg3: { dns: 1, static: 1, hr_neo: 1 } },
  other: { dns: 3, static: 1, hr_neo: 0 },
  policies: [],
  rules: [],
}
const INV = (status, rules = []) => ({ status: 'ok', output: JSON.stringify({ status, rules }) })

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const buttonsIn = (el) => [...(el?.querySelectorAll('button') ?? [])]
const byText = (el, text) => buttonsIn(el).find((b) => b.textContent.trim() === text)

async function mount(props = {}) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  const seen = { sheets: [] }
  await act(async () => render(<RoutesTab routerID={7} openSheet={(s) => seen.sheets.push(s)} {...props} />, root))
  await flush()
  await flush()
  return { root, seen }
}

function tunnelRow(root, name) {
  return [...root.querySelectorAll('li.tunnel-row')].find((li) => li.querySelector('.row-title')?.textContent === name)
}

async function pickTarget(root, name) {
  const row = [...root.querySelectorAll('.overlay .list-row-btn')].find((b) => b.querySelector('.row-title')?.textContent === name)
  expect(row, `цели «${name}» нет`).toBeTruthy()
  await act(async () => row.click())
}

beforeEach(() => {
  mocks.calls = []
  mocks.role = 'owner'
  mocks.answers = {
    route_status: { status: 'ok', output: JSON.stringify(SNAP) },
    hrneo_inventory: INV({ installed: true, running: true }, [
      { id: 'r1', name: 'youtube', enabled: true, bind: 'nwg1', domains: ['youtube.example.com', 'ytimg.example.com'] },
      { id: 'r2', name: 'office', enabled: false, policy_name: 'VPN', routes: ['203.0.113.0/24'] },
    ]),
  }
})

describe('перенос из «Напрямую (WAN)»', () => {
  it('строка источника и лист без обещания обратного переноса', async () => {
    const { root, seen } = await mount()
    const other = root.querySelector('li.routes-other')
    expect(other.querySelector('.row-title').textContent).toBe('Напрямую (WAN)')
    expect(other.textContent).toContain('4 правила идут мимо VPN-туннелей, напрямую через провайдера')
    await act(async () => byText(other, 'Перенести всё').click())
    expect(root.querySelector('.overlay .router-lastseen').textContent).toContain('напрямую через провайдера (4 правила)')
    expect([...root.querySelectorAll('.overlay .list-row-btn .row-title')].map((t) => t.textContent)).toEqual(['amsterdam', 'spare', 'old-home'])
    await pickTarget(root, 'spare')
    const sheet = seen.sheets.at(-1)
    expect(sheet).toMatchObject({ action: 'route_rebind', args: { src_tunnel_id: '__other__', dst_tunnel_id: 'nwg2' }, title: 'Перенести всё в «spare»?' })
    expect(sheet.body).toBe('4 правила из «Напрямую (WAN)» пойдут через VPN-туннель «spare». Обратно в «Напрямую (WAN)» приложение правила не переносит.')
    render(null, root)
  })

  it('без правил вне VPN-туннелей строки нет', async () => {
    mocks.answers.route_status = { status: 'ok', output: JSON.stringify({ ...SNAP, other: { dns: 0, static: 0 } }) }
    const { root } = await mount()
    expect(root.querySelector('li.routes-other')).toBe(null)
    render(null, root)
  })
})

describe('честный текст переноса', () => {
  it('на цели свои правила -- обратный перенос заберёт и их, с числом', async () => {
    const { root, seen } = await mount()
    await act(async () => byText(tunnelRow(root, 'amsterdam'), 'Перенести всё').click())
    await pickTarget(root, 'old-home')
    expect(seen.sheets.at(-1).body).toBe(
      '3 правила уедут из «amsterdam» в «old-home». В «amsterdam» не останется ничего. Отменить можно обратным переносом, но он заберёт и 2 правила, которые уже были на «old-home».',
    )
    render(null, root)
  })

  it('цель пустая -- обратный перенос без оговорок', async () => {
    const { root, seen } = await mount()
    await act(async () => byText(tunnelRow(root, 'amsterdam'), 'Перенести всё').click())
    await pickTarget(root, 'spare')
    expect(seen.sheets.at(-1).body).toMatch(/Отменить можно обратным переносом с «spare»\.$/)
    render(null, root)
  })

  it('пришли с экрана VPN-туннеля -- выбор цели открыт сразу', async () => {
    const { root } = await mount({ rebindFrom: 'nwg1' })
    expect(root.querySelector('.overlay .overlay-title').textContent).toBe('Куда перенести')
    expect(root.querySelector('.overlay .router-lastseen').textContent).toContain('«amsterdam»')
    render(null, root)
  })
})

describe('блок HydraRoute Neo', () => {
  const block = (root) => [...root.querySelectorAll('.section')].find((s) => s.querySelector('.section-title')?.textContent === HRNEO_TEXTS.title)

  it('работает: владелец может перезапустить и остановить; правила свёрнуты', async () => {
    const { root, seen } = await mount()
    const b = block(root)
    expect(b, 'блока HydraRoute Neo нет').toBeTruthy()
    expect(b.querySelector('.hrneo-status .badge').textContent).toBe('работает')
    expect(buttonsIn(b.querySelector('.hrneo-actions')).map((x) => x.textContent)).toEqual(['Перезапустить', 'Остановить'])
    const details = b.querySelector('details.hrneo-rules')
    expect(details.open).toBe(false)
    expect(details.querySelector('summary').textContent).toBe('Правила HydraRoute Neo · 2')
    expect([...details.querySelectorAll('.list-row-sub')].map((x) => x.textContent)).toEqual([
      '2 сайта · через «amsterdam»',
      '1 адрес сети · общий набор «VPN» · выключено',
    ])
    expect(details.querySelectorAll('button')).toHaveLength(0)

    await act(async () => byText(b, 'Остановить').click())
    const sheet = seen.sheets.at(-1)
    expect(sheet).toMatchObject({ action: 'service_restart', args: { name: 'hrneo_stop' }, danger: true, title: 'Остановить HydraRoute Neo?' })
    expect(sheet.body).toContain('Правила по имени сайта перестанут работать до запуска.')
    render(null, root)
  })

  it('оператор -- только перезапуск', async () => {
    mocks.role = 'operator'
    const { root } = await mount()
    expect(buttonsIn(block(root).querySelector('.hrneo-actions')).map((x) => x.textContent)).toEqual(['Перезапустить'])
    render(null, root)
  })

  it('остановлен -- «Запустить» владельцу', async () => {
    mocks.answers.hrneo_inventory = INV({ installed: true, running: false })
    const { root, seen } = await mount()
    const b = block(root)
    expect(b.querySelector('.hrneo-status .badge').textContent).toBe('остановлен')
    expect(buttonsIn(b.querySelector('.hrneo-actions')).map((x) => x.textContent)).toEqual(['Запустить'])
    await act(async () => byText(b, 'Запустить').click())
    expect(seen.sheets.at(-1)).toMatchObject({ action: 'service_restart', args: { name: 'hrneo_start' }, danger: false })
    render(null, root)
  })

  it('не установлен -- слова, кнопок нет', async () => {
    mocks.answers.hrneo_inventory = INV({ installed: false, running: false })
    const { root } = await mount()
    const b = block(root)
    expect(b.textContent).toContain('HydraRoute Neo на роутере не установлен.')
    expect(b.querySelector('.hrneo-actions')).toBe(null)
    render(null, root)
  })

  it('старый агент -- состояние по снимку и слова про агента', async () => {
    mocks.answers.hrneo_inventory = { status: 'err', output: 'unknown action: hrneo_inventory' }
    const { root } = await mount()
    const b = block(root)
    expect(b.querySelector('.hrneo-status .badge').textContent).toBe('работает')
    expect(b.textContent).toContain(HRNEO_TEXTS.oldAgent)
    expect(b.querySelector('details.hrneo-rules')).toBe(null)
    render(null, root)
  })
})
