// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { ApiError } from '../src/api.js'
import { FALLBACK_ERROR_TEXT } from '../src/errorText.js'

// Автопочинка на экране VPN-туннеля: выключена -- клик открывает лист с четырьмя
// разделами; включена -- клик сразу шлёт PUT enabled:false, без листа.
const mocks = vi.hoisted(() => ({ get: null, put: [], list: {}, reasons: {}, putError: null }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  getAutorepair: () => Promise.resolve(mocks.get),
  putAutorepair: (rid, tid, body) => {
    mocks.put.push([rid, tid, body])
    if (mocks.putError) return Promise.reject(mocks.putError)
    return Promise.resolve({ ...mocks.get, ...body, enabled: body.enabled })
  },
  listAutorepair: () => Promise.resolve({ tunnels: mocks.list, reasons: mocks.reasons }),
  fetchRouterSettings: () => Promise.resolve({ role: 'owner' }),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [] }),
}))

const SNAP = {
  tunnels: [
    { id: 'awg12', name: 'vpn-nl', type: 'managed', status: 'running', enabled: true, has_handshake: true, handshake_age_sec: 30 },
    { id: 'awg10', name: 'vpn-de', type: 'managed', status: 'running', enabled: true, has_handshake: true, handshake_age_sec: 30 },
  ],
  policies: [{ name: 'P', active_tunnel_id: 'awg12', interfaces: [{ tunnel_id: 'awg12', role: 'active' }, { tunnel_id: 'awg10', role: 'fallback' }] }],
}
const CMD = vi.hoisted(() => ({ value: null }))
vi.mock('../src/useCommand.js', () => ({ useCommand: () => CMD.value }))
CMD.value = { busy: false, result: { status: 'ok', output: JSON.stringify(SNAP) }, error: null, errorCode: null, run: () => Promise.resolve(null) }

const { TunnelScreen } = await import('../src/screens/TunnelScreen.jsx')
const { TunnelsTab } = await import('../src/screens/TunnelsTab.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

const BASE = {
  enabled: false, provider: '', option: '', allow_relocate: false, can_edit: true,
  suggested: { provider: 'amnezia', option: 'nl', why: 'так он был выпущен' },
  sources: [{ provider: 'amnezia', label: 'Amnezia Premium', ok: true, options: [{ id: 'nl', label: 'Нидерланды' }] }],
}

async function mount(opts = {}) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  const sheets = []
  await act(async () => render(<TunnelScreen routerID={4} asleep={false} snapshot={SNAP} tunnelID="awg12" role="owner" openSheet={(s) => sheets.push(s)} onClose={() => {}} {...opts} />, root))
  await flush()
  return { root, sheets }
}

beforeEach(() => {
  mocks.put = []
  mocks.putError = null
  mocks.list = {}
  mocks.reasons = {}
})

describe('автопочинка: экран VPN-туннеля', () => {
  it('выключена: строка, клик открывает лист с четырьмя разделами и полями', async () => {
    mocks.get = { ...BASE }
    const { root, sheets } = await mount()
    expect(root.textContent).toContain('Автопочинка')
    expect(root.textContent).toContain('выключена')
    const btn = root.querySelector('.tunnel-autorepair')
    expect(btn.textContent).toBe('Включить автопочинку')
    await act(async () => btn.click())
    expect(mocks.put).toEqual([])
    expect(sheets).toHaveLength(1)
    const sh = sheets[0]
    expect(sh.title).toBe('Включить автопочинку «vpn-nl»?')
    // Тело листа -- четыре раздела; рисуем его так же, как лист.
    const host = document.createElement('div')
    await act(async () => render(sh.body, host))
    const heads = [...host.querySelectorAll('.sheet-sec-h')].map((n) => n.textContent)
    expect(heads).toEqual(['Что будет делать', 'Чего стоит', 'Кому напишу', 'Как выключить'])
    expect(host.textContent).toContain('запасной VPN-туннель «vpn-de»')
    expect(sh.fields.map((f) => f.name)).toEqual(['source', 'allow_relocate'])
    expect(sh.buttonLabel).toBe('Включить')
    // perform шлёт PUT enabled:true с выбранным источником и галочкой.
    await sh.perform('', { source: 'amnezia|nl', allow_relocate: true })
    expect(mocks.put).toEqual([[4, 'awg12', { enabled: true, provider: 'amnezia', option: 'nl', allow_relocate: true }]])
    render(null, root)
    root.remove()
  })

  it('включена: клик сразу шлёт PUT enabled:false, листа нет, строка обновляется', async () => {
    mocks.get = { ...BASE, enabled: true, provider: 'amnezia', option: 'nl', allow_relocate: true }
    const { root, sheets } = await mount()
    expect(root.textContent).toContain('включена · из «Amnezia Premium»')
    const btn = root.querySelector('.tunnel-autorepair')
    expect(btn.textContent).toBe('Выключить автопочинку')
    await act(async () => btn.click())
    await flush()
    expect(sheets).toHaveLength(0)
    expect(mocks.put).toEqual([[4, 'awg12', { enabled: false, provider: 'amnezia', option: 'nl', allow_relocate: true }]])
    expect(root.textContent).toContain('выключена')
    render(null, root)
    root.remove()
  })

  it('нельзя править -- строки нет', async () => {
    mocks.get = { ...BASE, can_edit: false }
    const { root } = await mount()
    expect(root.querySelector('.tunnel-autorepair')).toBeNull()
    expect(root.textContent).not.toContain('Автопочинка')
    render(null, root)
    root.remove()
  })

  it('переименован: строка просит подтвердить, кнопка подтверждения открывает лист, выключение на месте', async () => {
    mocks.get = { ...BASE, enabled: true, provider: 'amnezia', option: 'nl', rename_pending: 'Старый' }
    const { root, sheets } = await mount()
    expect(root.textContent).toContain('ждёт подтверждения')
    expect(root.textContent).toContain('был «Старый»')
    expect(root.querySelector('.tunnel-autorepair').textContent).toBe('Выключить автопочинку')
    const confirm = root.querySelector('.tunnel-autorepair-confirm')
    expect(confirm.textContent).toBe('Подтвердить автопочинку')
    await act(async () => confirm.click())
    expect(sheets).toHaveLength(1)
    expect(sheets[0].title).toBe('Подтвердить автопочинку «vpn-nl»?')
    expect(sheets[0].buttonLabel).toBe('Подтвердить')
    await sheets[0].perform('', { source: 'amnezia|nl', allow_relocate: true })
    expect(mocks.put).toEqual([[4, 'awg12', { enabled: true, provider: 'amnezia', option: 'nl', allow_relocate: true }]])
    render(null, root)
    root.remove()
  })

  it('резерв, а первое звено лежит -- лист не называет лежащий VPN-туннель', async () => {
    mocks.get = { ...BASE }
    const snap = {
      tunnels: SNAP.tunnels,
      policies: [{ name: 'P', interfaces: [{ tunnel_id: 'awg10', role: 'unavailable', available: false }, { tunnel_id: 'awg12', role: 'unavailable' }] }],
    }
    const { root, sheets } = await mount({ snapshot: snap })
    await act(async () => root.querySelector('.tunnel-autorepair').click())
    const host = document.createElement('div')
    await act(async () => render(sheets[0].body, host))
    expect(host.textContent).toContain('у трафика сейчас нет рабочего VPN-туннеля')
    expect(host.textContent).not.toContain('«vpn-de»')
    render(null, root)
    root.remove()
  })

  it('резерв, чья проверка провалена, листом не называется -- как на главном экране', async () => {
    mocks.get = { ...BASE }
    const verdict = { ...SNAP, tunnels: SNAP.tunnels.map((t) => (t.id === 'awg10' ? { ...t, status: 'dead' } : t)) }
    const { root, sheets } = await mount({ verdictSnapshot: verdict })
    await act(async () => root.querySelector('.tunnel-autorepair').click())
    const host = document.createElement('div')
    await act(async () => render(sheets[0].body, host))
    expect(host.textContent).not.toContain('«vpn-de»')
    expect(host.textContent).toContain('Запасного VPN-туннеля нет')
    render(null, root)
    root.remove()
  })

  it('снимок не знает резерв -- фразы о резерве нет', async () => {
    mocks.get = { ...BASE }
    const { root, sheets } = await mount({ snapshot: { tunnels: SNAP.tunnels } })
    await act(async () => root.querySelector('.tunnel-autorepair').click())
    const host = document.createElement('div')
    await act(async () => render(sheets[0].body, host))
    expect(host.textContent).not.toContain('запасн')
    render(null, root)
    root.remove()
  })
})

describe('автопочинка: отказ сервера на PUT', () => {
  it('включение: русская фраза сервера -- как есть, английская -- общая русская', async () => {
    mocks.get = { ...BASE }
    const { root, sheets } = await mount()
    await act(async () => root.querySelector('.tunnel-autorepair').click())
    const sh = sheets[0]
    expect(sh.errorText(new ApiError(409, 'source_not_connected', 'x failed: 409', 'Кабинет не подключён — подключите его и повторите.'))).toBe('Кабинет не подключён — подключите его и повторите.')
    // Невыпущенная страна «Amnezia Premium» -- отказ сервера читается как есть.
    expect(sh.errorText(new ApiError(409, 'option_not_issued', 'x failed: 409', 'эта страна ещё не выпущена в кабинете — выберите выпущенную'))).toBe(
      'эта страна ещё не выпущена в кабинете — выберите выпущенную',
    )
    const en = sh.errorText(new ApiError(500, 'internal', 'x failed: 500', 'settings not saved'))
    expect(en).toBe(FALLBACK_ERROR_TEXT)
    expect(en).not.toMatch(/settings/)
    render(null, root)
    root.remove()
  })

  it('выключение: английский отказ -- русская фраза на экране', async () => {
    mocks.get = { ...BASE, enabled: true, provider: 'amnezia', option: 'nl' }
    mocks.putError = new ApiError(500, 'internal', 'x failed: 500', 'settings not saved')
    const { root } = await mount()
    await act(async () => root.querySelector('.tunnel-autorepair').click())
    await flush()
    const err = root.querySelector('.state-error')
    expect(err.textContent).toBe(FALLBACK_ERROR_TEXT)
    expect(root.textContent).not.toContain('settings not saved')
    render(null, root)
    root.remove()
  })

  it('выключение: русская фраза сервера показывается', async () => {
    mocks.get = { ...BASE, enabled: true, provider: 'amnezia', option: 'nl' }
    mocks.putError = new ApiError(409, 'busy', 'x failed: 409', 'Настройка сейчас занята — повторите через минуту.')
    const { root } = await mount()
    await act(async () => root.querySelector('.tunnel-autorepair').click())
    await flush()
    expect(root.querySelector('.state-error').textContent).toBe('Настройка сейчас занята — повторите через минуту.')
    render(null, root)
    root.remove()
  })
})

describe('автопочинка: метка во вкладке', () => {
  it('метки по состояниям; туннель вне снимка метки не получает', async () => {
    mocks.list = { awg12: 'blocked', awg10: 'limited', gone99: 'on' }
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<TunnelsTab routerID={4} asleep={false} openSheet={() => {}} />, root))
    await flush()
    const text = root.textContent
    expect(text).toContain('Автопочинка стоит — нужен человек')
    expect(text).toContain('Автопочинка: только перезапуск')
    expect(root.querySelectorAll('.pill').length).toBe(2)
    render(null, root)
    root.remove()
  })

  it('переименованный VPN-туннель -- метка просит подтвердить', async () => {
    mocks.list = { awg12: 'blocked' }
    mocks.reasons = { awg12: 'VPN-туннель переименован — подтвердите автопочинку на его экране' }
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<TunnelsTab routerID={4} asleep={false} openSheet={() => {}} />, root))
    await flush()
    expect(root.textContent).toContain('Автопочинка ждёт подтверждения')
    expect(root.textContent).not.toContain('нужен человек')
    render(null, root)
    root.remove()
  })
})
