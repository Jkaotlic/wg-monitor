// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Автопочинка на экране VPN-туннеля: выключена -- клик открывает лист с четырьмя
// разделами; включена -- клик сразу шлёт PUT enabled:false, без листа.
const mocks = vi.hoisted(() => ({ get: null, put: [], list: {} }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  getAutorepair: () => Promise.resolve(mocks.get),
  putAutorepair: (rid, tid, body) => {
    mocks.put.push([rid, tid, body])
    return Promise.resolve({ ...mocks.get, ...body, enabled: body.enabled })
  },
  listAutorepair: () => Promise.resolve({ tunnels: mocks.list }),
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
  mocks.list = {}
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
})
