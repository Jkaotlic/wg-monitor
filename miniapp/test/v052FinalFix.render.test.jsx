// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Финальное ревью v0.52: потерянные функции (п. 1, 3) и тихие провалы (п. 4, 5).
const A = vi.hoisted(() => ({ snap: null, role: 'owner', settings: null, issuable: null, settingsCalls: 0 }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [{ tunnel_id: 'awg10', status: 'ok', run_state: 'running' }] }),
  // Вкладки читают проверки вместе с тревогами (I1): тот же ответ, тревог нет.
  fetchRouterChecksWithIncidents: (id) => Promise.resolve((() => Promise.resolve({ checks: [], tunnels: [{ tunnel_id: 'awg10', status: 'ok', run_state: 'running' }] }))(id)).then((ev) => ({ incidents: [], ...ev })),
  fetchRouterSettings: (routerID) => {
    A.settingsCalls++
    return A.settings ? A.settings(routerID) : Promise.resolve({ role: A.role })
  },
  fetchAwg3Issuable: () => A.issuable(),
  fetchRouterFacts: () => Promise.resolve(null),
  fetchVPNAccounts: () => Promise.resolve({ accounts: [] }),
  fetchReplaceStatus: () => Promise.resolve(null),
}))
const CMD = vi.hoisted(() => ({ value: null }))
vi.mock('../src/useCommand.js', () => ({ useCommand: () => CMD.value }))

const { TunnelsTab } = await import('../src/screens/TunnelsTab.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const setSnap = (snap) => {
  CMD.value = { busy: false, result: { status: 'ok', output: JSON.stringify(snap) }, error: null, errorCode: null, run: () => Promise.resolve(null) }
}
const MANAGED = { id: 'awg10', name: 'vpn-nl', type: 'managed', status: 'running', enabled: true, has_handshake: true, handshake_age_sec: 30 }
const POLICY = { name: 'Policy0', active_tunnel_id: 'awg10', interfaces: [] }

async function mount(props) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<TunnelsTab routerID={4} asleep={false} openSheet={() => {}} layer={null} layerParams={{}} openLayer={() => {}} closeLayer={() => {}} {...props} />, root))
  await flush()
  await flush()
  return root
}
const cleanup = (root) => {
  render(null, root)
  root.remove()
}
const btn = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)

beforeEach(() => {
  A.role = 'owner'
  A.settings = null
  A.settingsCalls = 0
  A.issuable = () => Promise.resolve({ panels: [] })
  setSnap({ tunnels: [MANAGED], policies: [POLICY] })
})

describe('п. 1: «Заменить конфиг» у активного VPN-туннеля не managed-типа', () => {
  // Кнопка есть только там, где сервер найдёт туннель: событие tunnel_<id> в
  // checks.tunnels (его же читает miniappResolveTunnelArgs).
  it('нет события проверки у активного -- кнопки нет (иначе гарантированный unknown_tunnel)', async () => {
    setSnap({ tunnels: [{ ...MANAGED, id: 'Wireguard0', type: 'system' }], policies: [{ ...POLICY, active_tunnel_id: 'Wireguard0' }] })
    const root = await mount()
    expect(btn(root, 'Заменить конфиг')).toBeFalsy()
    cleanup(root)
  })
  it('вход в герое активного, открывает слой replace', async () => {
    setSnap({ tunnels: [{ ...MANAGED, type: 'system' }], policies: [POLICY] })
    const calls = []
    const root = await mount({ openLayer: (o, p) => calls.push([o, p]) })
    const b = btn(root, 'Заменить конфиг')
    expect(b).toBeTruthy()
    await act(async () => b.click())
    expect(calls[0][0]).toBe('replace')
    expect(calls[0][1]).toMatchObject({ tunnel: { id: 'awg10' }, policyName: 'Policy0' })
    cleanup(root)
  })
  it('у managed-активного второго входа на вкладке нет: он на экране VPN-туннеля', async () => {
    const root = await mount()
    expect(btn(root, 'Заменить конфиг')).toBeFalsy()
    cleanup(root)
  })
})

describe('п. 3: перезапуск здорового VPN-туннеля на его экране', () => {
  const screen = (root) => root.querySelector('.overlay')
  it('владелец: кнопка контурная, лист tunnel_restart с этим туннелем', async () => {
    const sheets = []
    const root = await mount({ layer: 'tunnel', layerParams: { tunnelID: 'awg10' }, openSheet: (s) => sheets.push(s) })
    const b = [...screen(root).querySelectorAll('button')].find((x) => x.textContent.trim() === 'Перезапустить VPN-туннель')
    expect(b).toBeTruthy()
    expect(b.classList.contains('btn-primary')).toBe(false)
    await act(async () => b.click())
    expect(sheets[0]).toMatchObject({ action: 'tunnel_restart', args: { tunnel_id: 'awg10' } })
    expect(screen(root).querySelectorAll('.btn-primary').length).toBe(0)
    cleanup(root)
  })
  it('оператор: перезапуск есть и открывает лист tunnel_restart; «Удалить» нет', async () => {
    A.role = 'operator'
    const sheets = []
    const root = await mount({ layer: 'tunnel', layerParams: { tunnelID: 'awg10' }, openSheet: (s) => sheets.push(s) })
    const texts = [...screen(root).querySelectorAll('button')].map((x) => x.textContent.trim())
    expect(texts).not.toContain('Удалить VPN-туннель')
    const b = [...screen(root).querySelectorAll('button')].find((x) => x.textContent.trim() === 'Перезапустить VPN-туннель')
    expect(b).toBeTruthy()
    await act(async () => b.click())
    expect(sheets[0]).toMatchObject({ action: 'tunnel_restart', args: { tunnel_id: 'awg10' } })
    cleanup(root)
  })
})

describe('п. 4: «Загрузить .conf» и роль', () => {
  it('роль не прочиталась -- пункт с повтором; повтор перечитывает роль', async () => {
    let n = 0
    A.settings = () => (n++ === 0 ? Promise.reject(new Error('502')) : Promise.resolve({ role: 'owner' }))
    const sheets = []
    const root = await mount({ openSheet: (s) => sheets.push(s) })
    await act(async () => root.querySelector('.btn-primary').click())
    const retry = sheets[0].choices.find((c) => c.value === 'conf-retry')
    expect(retry.pill.text).toBe('не загрузилось — повторить')
    let resp
    await act(async () => { resp = await sheets[0].perform('', {}, 'conf-retry') })
    await act(async () => { sheets[0].onDone(resp); await new Promise((r) => setTimeout(r, 0)) })
    expect(sheets[1].choices.map((c) => c.value)).toContain('conf')
    expect(sheets[1].choices.map((c) => c.value)).not.toContain('conf-retry')
    cleanup(root)
  })
  it('роль зависла -- кнопка занята, через ожидание лист открывается с повтором', async () => {
    A.settings = () => new Promise(() => {})
    const sheets = []
    const root = await mount({ openSheet: (s) => sheets.push(s) })
    vi.useFakeTimers()
    try {
      const lime = root.querySelector('.btn-primary')
      await act(async () => lime.click())
      expect(sheets.length).toBe(0)
      expect(lime.disabled).toBe(true)
      await act(async () => { await vi.advanceTimersByTimeAsync(8100) })
      expect(sheets.length).toBe(1)
      expect(sheets[0].choices.find((c) => c.value === 'conf-retry')).toBeTruthy()
      expect(root.querySelector('.btn-primary').disabled).toBe(false)
      cleanup(root)
    } finally {
      vi.useRealTimers()
    }
  })
  it('роль грузится -- кнопка ждёт ответ и открывает лист с итоговыми вариантами', async () => {
    let release
    A.settings = () => new Promise((r) => { release = () => r({ role: 'owner' }) })
    const sheets = []
    const root = await mount({ openSheet: (s) => sheets.push(s) })
    const lime = root.querySelector('.btn-primary')
    await act(async () => lime.click())
    expect(sheets.length).toBe(0)
    expect(lime.disabled).toBe(true)
    await act(async () => { release(); await new Promise((r) => setTimeout(r, 0)) })
    expect(sheets.length).toBe(1)
    expect(sheets[0].choices.map((c) => c.value)).toContain('conf')
    cleanup(root)
  })
})

describe('п. 5: панель VPN-сервера, пока список грузится', () => {
  it('кнопка ждёт список и открывает лист уже с пунктом панели', async () => {
    let release
    A.issuable = () => new Promise((r) => { release = () => r({ panels: [{ id: 'main', label: 'Main', ifaces: [] }] }) })
    const sheets = []
    const root = await mount({ openSheet: (s) => sheets.push(s) })
    await act(async () => root.querySelector('.btn-primary').click())
    expect(sheets.length).toBe(0)
    await act(async () => { release(); await new Promise((r) => setTimeout(r, 0)) })
    expect(sheets[0].choices.map((c) => c.value)).toContain('awg3')
    expect(sheets[0].choices.some((c) => c.disabled)).toBe(false)
    cleanup(root)
  })
})

describe('п. 6: повтор чтения роли после смены роутера', () => {
  it('поздний ответ старого роутера не записывает его роль новому', async () => {
    let n = 0
    let late
    A.settings = (rid) => {
      if (rid === 4) return n++ === 0 ? Promise.reject(new Error('502')) : new Promise((r) => { late = () => r({ role: 'owner' }) })
      return Promise.resolve({ role: 'viewer' })
    }
    const sheets = []
    const root = document.createElement('div')
    document.body.appendChild(root)
    const el = (id) => <TunnelsTab routerID={id} asleep={false} openSheet={(s) => sheets.push(s)} layer={null} layerParams={{}} openLayer={() => {}} closeLayer={() => {}} />
    await act(async () => render(el(4), root))
    await flush()
    await flush()
    await act(async () => root.querySelector('.btn-primary').click())
    const retry = sheets[0]
    let pending
    await act(async () => { pending = retry.perform('', {}, 'conf-retry') })
    await act(async () => render(el(5), root))
    await flush()
    await flush()
    await act(async () => { late(); await pending; await new Promise((r) => setTimeout(r, 0)) })
    await act(async () => { retry.onDone(await pending); await new Promise((r) => setTimeout(r, 0)) })
    sheets.length = 0
    await act(async () => root.querySelector('.btn-primary').click())
    expect(sheets.length).toBe(1)
    expect(sheets[0].choices.map((c) => c.value)).not.toContain('conf')
    cleanup(root)
  })
})

describe('п. 2: строка «Маршруты» без активного VPN-туннеля', () => {
  it('нет несущего -- строка на месте и открывает слой', async () => {
    setSnap({ tunnels: [MANAGED], policies: [] })
    const opened = []
    const root = await mount({ onOpenRoutes: () => opened.push(1) })
    const row = [...root.querySelectorAll('.list-row')].find((r) => r.textContent.includes('Маршруты: куда идёт трафик'))
    expect(row).toBeTruthy()
    await act(async () => row.click())
    expect(opened.length).toBe(1)
    cleanup(root)
  })
})
