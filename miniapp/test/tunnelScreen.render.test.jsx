// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Экран VPN-туннеля на вкладке «VPN-туннели»: список, права, причины отказа,
// удаление набором имени и итог.
const mocks = vi.hoisted(() => ({ calls: [], answers: {}, role: 'owner', api: [], deleteReply: null, deleteReplies: [], result: null }))

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

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  return {
    ...real,
    // role === null -- роль ещё не пришла.
    fetchRouterSettings: () => (mocks.role === null ? new Promise(() => {}) : Promise.resolve({ role: mocks.role })),
    // Контракт части 1: пока сервер ждёт снимок роутера -- {state:'checking'},
    // итог -- {state:'queued', cmd_id}.
    deleteTunnel: (routerID, tunnelID, confirm) => {
      mocks.api.push(['delete', routerID, tunnelID, confirm])
      if (mocks.deleteReply instanceof Error) return Promise.reject(mocks.deleteReply)
      return Promise.resolve(mocks.deleteReplies.shift() ?? { state: 'queued', cmd_id: 'c9' })
    },
    fetchCommandResult: (routerID, cmdID) => {
      mocks.api.push(['result', routerID, cmdID])
      return mocks.result instanceof Error ? Promise.reject(mocks.result) : Promise.resolve(mocks.result)
    },
  }
})

// Паузы между повторами -- мгновенные, часы -- быстрые: тест не ждёт две минуты.
vi.mock('../src/commandWait.js', async (importOriginal) => {
  const real = await importOriginal()
  return {
    ...real,
    repeatWhilePending: (once, opts) => {
      let t = 0
      return real.repeatWhilePending(once, { ...opts, sleep: () => Promise.resolve(), now: () => (t += 10_000) })
    },
  }
})

const { TunnelsTab } = await import('../src/screens/TunnelsTab.jsx')
const { TunnelScreen } = await import('../src/screens/TunnelScreen.jsx')
const { Sheet } = await import('../src/ui/Sheet.jsx')
const { ApiError } = await import('../src/api.js')

const SNAP = {
  policy_model: true,
  default_egress: 'direct',
  tunnels: [
    { id: 'nwg1', name: 'amsterdam', iface: 'nwg1', type: 'managed', status: 'running' },
    { id: 'nwg2', name: 'spare', iface: 'nwg2', type: 'managed', status: 'disabled' },
    { id: 'nwg3', name: 'old-home', iface: 'nwg3', type: 'managed', status: 'running' },
    { id: 'ISP', name: 'Провайдер', iface: 'ISP', type: 'wan', status: 'up' },
  ],
  counts: { nwg1: { dns: 2, static: 1, hr_neo: 2 } },
  other: { dns: 0, static: 0, hr_neo: 0 },
  policies: [
    {
      name: 'VPN',
      dns: 5,
      hr_neo: 5,
      active_tunnel_id: 'nwg1',
      via_vpn: true,
      interfaces: [
        { bind: 'nwg1', name: 'amsterdam', role: 'active', tunnel_id: 'nwg1', via_vpn: true },
        { bind: 'nwg2', name: 'spare', role: 'fallback', tunnel_id: 'nwg2' },
      ],
    },
  ],
}
const snapResult = (snap) => ({ status: 'ok', output: JSON.stringify(snap) })

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const byText = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)
const routeStatusCalls = () => mocks.calls.filter((c) => c.action === 'route_status').length

async function mount(props = {}) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  const seen = { sheets: [], rebind: [] }
  await act(async () =>
    render(
      <TunnelsTab
        routerID={7}
        openSheet={(s) => seen.sheets.push(s)}
        onOpenRoutes={() => {}}
        onOpenRebind={(id) => seen.rebind.push(id)}
        {...props}
      />,
      root,
    ),
  )
  await flush()
  await flush()
  return { root, seen }
}

async function openTunnel(root, name) {
  const row = [...root.querySelectorAll('.list-row-btn')].find((b) => b.querySelector('.row-title')?.textContent === name)
  expect(row, `строки «${name}» нет`).toBeTruthy()
  await act(async () => row.click())
}

async function mountSheet(sheet) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  await act(async () => render(<Sheet sheet={sheet} onClose={() => {}} />, host))
  return host
}

async function typeConfirm(host, text) {
  const input = host.querySelector('#sheet-confirm-input')
  await act(async () => {
    input.value = text
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function confirmDelete(sheet, typed) {
  const host = await mountSheet(sheet)
  await typeConfirm(host, typed)
  await act(async () => byText(host, 'Удалить').click())
  for (let i = 0; i < 4; i++) await flush()
  return host
}

beforeEach(() => {
  mocks.calls = []
  mocks.api = []
  mocks.answers = { route_status: snapResult(SNAP) }
  mocks.role = 'owner'
  mocks.deleteReply = null
  mocks.deleteReplies = []
  mocks.result = { status: 'ok', output: '' }
})

describe('экран VPN-туннеля', () => {
  it('список своих VPN-туннелей ведёт на экран, «назад» закрывает', async () => {
    const { root } = await mount()
    // Строки «Все VPN-туннели»; ниже -- строки переходов (Маршруты, загрузка
    // .conf, замена конфига) в своей карточке .tunnels-more.
    const rows = [...root.querySelectorAll('.list-row-btn')].filter((b) => !b.closest('.tunnels-more'))
    expect(rows.map((b) => b.querySelector('.row-title').textContent)).toEqual(['amsterdam', 'spare', 'old-home'])
    expect(root.textContent).toContain('Все VPN-туннели · 3')
    await openTunnel(root, 'spare')
    const screen = root.querySelector('.tunnel-screen')
    expect(screen.querySelector('.screen-title').textContent).toBe('«spare»')
    expect(screen.textContent).toContain('выключен')
    expect(screen.textContent).toContain('Удаление необратимо')
    expect(byText(root, 'Удалить VPN-туннель')).toBeTruthy()
    await act(async () => root.querySelector('.overlay-back').click())
    expect(root.querySelector('.tunnel-screen')).toBe(null)
    render(null, root)
  })

  it('оператор видит экран, но не кнопку удаления', async () => {
    mocks.role = 'operator'
    const { root } = await mount()
    await openTunnel(root, 'spare')
    expect(byText(root, 'Удалить VPN-туннель')).toBeFalsy()
    expect(root.textContent).toContain('Удалять VPN-туннели и загружать конфиги могут владелец роутера и администратор.')
    render(null, root)
  })

  it('на VPN-туннеле правила -- слова и переход к переносу вместо кнопки', async () => {
    const { root, seen } = await mount()
    await openTunnel(root, 'amsterdam')
    expect(root.querySelector('.tunnel-block').textContent).toBe(
      'На этом VPN-туннеле 8 правил — сначала перенесите их на другой VPN-туннель в «Маршрутах», потом удаляйте.',
    )
    expect(byText(root, 'Удалить VPN-туннель')).toBeFalsy()
    await act(async () => byText(root, 'Перенести правила в «Маршрутах»').click())
    expect(seen.rebind).toEqual(['nwg1'])
    render(null, root)
  })

  it('главный выход роутера -- удалить нельзя, переноса не предлагает', async () => {
    mocks.answers.route_status = snapResult({ ...SNAP, default_egress: 'nwg3' })
    const { root } = await mount()
    await openTunnel(root, 'old-home')
    expect(root.querySelector('.tunnel-block').textContent).toContain('«old-home» — главный выход роутера')
    expect(byText(root, 'Удалить VPN-туннель')).toBeFalsy()
    expect(byText(root, 'Перенести правила в «Маршрутах»')).toBeFalsy()
    render(null, root)
  })

  it('удаление: набор имени, повтор на «проверяю», команда, итог и перечитанный снимок', async () => {
    const { root, seen } = await mount()
    await openTunnel(root, 'spare')
    await act(async () => byText(root, 'Удалить VPN-туннель').click())
    const sheet = seen.sheets.at(-1)
    // Сервер сверяет набранное как ник роутера (регистр, дефисы, пробелы по
    // краям) -- лист сверяет так же.
    expect(sheet).toMatchObject({ title: 'Удалить VPN-туннель «spare»?', confirmPhrase: 'spare', confirmStrict: false, danger: true, buttonLabel: 'Удалить' })

    const host = await mountSheet(sheet)
    expect(byText(host, 'Удалить').disabled).toBe(true)
    await typeConfirm(host, 'spar')
    expect(byText(host, 'Удалить').disabled).toBe(true)
    await typeConfirm(host, ' Spare ')
    expect(byText(host, 'Удалить').disabled).toBe(false)
    render(null, host)

    mocks.deleteReplies = [{ state: 'checking' }, { state: 'checking' }]

    const before = routeStatusCalls()
    mocks.answers.route_status = snapResult({ ...SNAP, tunnels: SNAP.tunnels.filter((t) => t.id !== 'nwg2') })
    await confirmDelete(sheet, 'spare')
    await flush()
    expect(mocks.api).toEqual([
      ['delete', 7, 'nwg2', 'spare'],
      ['delete', 7, 'nwg2', 'spare'],
      ['delete', 7, 'nwg2', 'spare'],
      ['result', 7, 'c9'],
    ])
    expect(root.querySelector('.tunnel-outcome').textContent).toBe('VPN-туннель «spare» удалён.')
    expect(routeStatusCalls()).toBe(before + 1)
    // Удалённый VPN-туннель: прежние интерфейс и правила не показываются.
    expect(root.querySelector('.tunnel-screen').textContent).toContain('удалён с роутера')
    expect(root.querySelector('.tunnel-screen').textContent).not.toContain('Интерфейс')
    expect(byText(root, 'Удалить VPN-туннель')).toBeFalsy()
    expect(byText(root, 'К списку VPN-туннелей')).toBeTruthy()
    render(null, root)
  })

  it('сервер нашёл правила -- те же слова и переход к переносу', async () => {
    mocks.deleteReply = new ApiError(409, 'tunnel_has_rules', 'x', 'На VPN-туннеле 2 правила', '', { code: 'tunnel_has_rules', rules: { total: 2, dns: 2, static: 0, hr_neo: 0, via_policy: 0 } })
    const { root, seen } = await mount()
    await openTunnel(root, 'spare')
    await act(async () => byText(root, 'Удалить VPN-туннель').click())
    await confirmDelete(seen.sheets.at(-1), 'spare')
    expect(root.querySelector('.tunnel-outcome').textContent).toBe(
      'На этом VPN-туннеле 2 правила — сначала перенесите их на другой VPN-туннель в «Маршрутах», потом удаляйте.',
    )
    expect(byText(root, 'Удалить VPN-туннель')).toBeFalsy()
    await act(async () => byText(root, 'Перенести правила в «Маршрутах»').click())
    expect(seen.rebind).toEqual(['nwg2'])
    expect(mocks.api.some((c) => c[0] === 'result')).toBe(false)
    render(null, root)
  })

  it('имя не совпало на сервере -- лист остаётся и говорит словами', async () => {
    mocks.deleteReply = new ApiError(400, 'confirm_mismatch', 'x')
    const { root, seen } = await mount()
    await openTunnel(root, 'spare')
    await act(async () => byText(root, 'Удалить VPN-туннель').click())
    const host = await confirmDelete(seen.sheets.at(-1), 'spare')
    expect(host.textContent).toContain('Имя VPN-туннеля набрано неверно — ничего не удалено.')
    expect(root.querySelector('.tunnel-outcome')).toBe(null)
    render(null, host)
    render(null, root)
  })

  it('роутер так и не ответил на проверку -- лист говорит словами, команды нет', async () => {
    mocks.deleteReplies = Array.from({ length: 50 }, () => ({ state: 'checking' }))
    const { root, seen } = await mount()
    await openTunnel(root, 'spare')
    await act(async () => byText(root, 'Удалить VPN-туннель').click())
    const host = await confirmDelete(seen.sheets.at(-1), 'spare')
    expect(host.textContent).toContain('Роутер не отвечает — проверьте, что он на связи. Ничего не удалено.')
    expect(mocks.api.some((c) => c[0] === 'result')).toBe(false)
    expect(root.querySelector('.tunnel-outcome')).toBe(null)
    render(null, host)
    render(null, root)
  })

})

// Ревью цикла 4.
describe('экран VPN-туннеля: ревью', () => {
  async function askAndConfirm(root, seen, name = 'spare') {
    await openTunnel(root, name)
    await act(async () => byText(root, 'Удалить VPN-туннель').click())
    return confirmDelete(seen.sheets.at(-1), name)
  }

  it('команда ушла, а ожидание сорвалось -- не «ничего не удалено», а «ушла на роутер»', async () => {
    mocks.result = new Error('502')
    const { root, seen } = await mount()
    const before = routeStatusCalls()
    const host = await askAndConfirm(root, seen)
    await flush()
    expect(host.textContent).not.toContain('ничего не удалено')
    expect(root.querySelector('.tunnel-outcome').textContent).toBe('Команда ушла на роутер, но он пока не ответил. Обновите список VPN-туннелей через минуту.')
    expect(routeStatusCalls()).toBe(before + 1)
    // Команда в очереди -- второй раз удалять нечего.
    expect(byText(root, 'Удалить VPN-туннель')).toBeFalsy()
    render(null, host)
    render(null, root)
  })

  it('упавший VPN-туннель в цепочке общего набора -- фраза сервера и переход к переносу', async () => {
    mocks.deleteReply = new ApiError(409, 'tunnel_in_policy_chain', 'x', 'VPN-туннель «spare» стоит в цепочке «VPN» перед работающим.', '', { rules: 5 })
    const { root, seen } = await mount()
    await askAndConfirm(root, seen)
    expect(root.querySelector('.tunnel-outcome').textContent).toBe('VPN-туннель «spare» стоит в цепочке «VPN» перед работающим.')
    expect(byText(root, 'Удалить VPN-туннель')).toBeFalsy()
    await act(async () => byText(root, 'Перенести правила в «Маршрутах»').click())
    expect(seen.rebind).toEqual(['nwg2'])
    render(null, root)
  })

  it('отказ не залипает: снимок поменялся -- экран решает по свежему', async () => {
    mocks.deleteReply = new ApiError(409, 'tunnel_has_rules', 'x', '', '', { rules: { total: 1 } })
    const { root, seen } = await mount()
    await askAndConfirm(root, seen)
    expect(root.querySelector('.tunnel-outcome')).toBeTruthy()
    mocks.answers.route_status = snapResult({ ...SNAP, counts: { ...SNAP.counts, nwg2: { dns: 1, static: 0 } } })
    await act(async () => byText(root, 'Обновить').click())
    await flush()
    expect(root.querySelector('.tunnel-outcome')).toBe(null)
    expect(root.querySelector('.tunnel-block').textContent).toContain('1 правило')
    render(null, root)
  })

  it('пустой главный выход -- кнопка есть, сервер решит сам', async () => {
    mocks.answers.route_status = snapResult({ ...SNAP, default_egress: '' })
    const { root } = await mount()
    await openTunnel(root, 'spare')
    expect(byText(root, 'Удалить VPN-туннель')).toBeTruthy()
    render(null, root)
  })

  it('роль ещё не пришла -- ни кнопки, ни слов про права', async () => {
    mocks.role = null
    const { root } = await mount()
    await openTunnel(root, 'spare')
    expect(byText(root, 'Удалить VPN-туннель')).toBeFalsy()
    expect(root.textContent).not.toContain('могут владелец роутера и администратор')
    render(null, root)
  })

  it('не свой VPN-туннель (из мастера замены) -- удалять в панели роутера', async () => {
    const root = document.createElement('div')
    document.body.appendChild(root)
    const snap = { ...SNAP, tunnels: [...SNAP.tunnels, { id: 'Wireguard0', name: 'старый', type: 'ndms', status: 'disabled' }] }
    await act(async () => render(<TunnelScreen routerID={7} snapshot={snap} tunnelID="Wireguard0" role="owner" openSheet={() => {}} onClose={() => {}} />, root))
    expect(root.textContent).toContain('Этот VPN-туннель создан не через awg-manager — удалите его в панели роутера.')
    expect(root.textContent).not.toContain('нет в снимке роутера')
    render(null, root)
  })
})
