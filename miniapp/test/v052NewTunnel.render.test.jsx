// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const SNAP = {
  tunnels: [{ id: 'awg10', name: 'vpn-nl', type: 'managed', status: 'running', enabled: true, has_handshake: true, handshake_age_sec: 30 }],
  policies: [{ name: 'Policy0', active_tunnel_id: 'awg10', interfaces: [] }],
}
const A = vi.hoisted(() => ({ issuable: null, role: 'owner' }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [{ tunnel_id: 'awg10', status: 'ok', run_state: 'running' }] }),
  fetchRouterSettings: () => Promise.resolve({ role: A.role }),
  fetchAwg3Issuable: () => A.issuable(),
  fetchRouterFacts: () => Promise.resolve(null),
}))
const CMD = vi.hoisted(() => ({ value: null }))
vi.mock('../src/useCommand.js', () => ({ useCommand: () => CMD.value }))
CMD.value = { busy: false, result: { status: 'ok', output: JSON.stringify(SNAP) }, error: null, errorCode: null, run: () => Promise.resolve(null) }

const { TunnelsTab } = await import('../src/screens/TunnelsTab.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
async function mount(props) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<TunnelsTab routerID={4} asleep={false} openSheet={() => {}} {...props} />, root))
  await flush()
  await flush()
  return root
}
const cleanup = (root) => {
  render(null, root)
  root.remove()
}

describe('v0.52: «Новый VPN-туннель»', () => {
  it('главная кнопка -- одна лаймовая, открывает лист «Откуда взять конфиг»', async () => {
    A.role = 'owner'
    A.issuable = () => Promise.resolve({ panels: [{ id: 'main', label: 'Main', ifaces: [] }] })
    const sheets = []
    const calls = []
    const root = await mount({ openSheet: (s) => sheets.push(s), layer: null, layerParams: {}, openLayer: (o, p) => calls.push([o, p]), closeLayer: () => {} })
    const limes = [...root.querySelectorAll('.btn-primary')]
    expect(limes.map((b) => b.textContent.trim())).toEqual(['Новый VPN-туннель'])
    await act(async () => limes[0].click())
    expect(sheets[0].title).toBe('Откуда взять конфиг')
    expect(sheets[0].choices.map((c) => c.value)).toEqual(['amnezia', 'hidemy', 'awg3', 'conf'])
    await act(async () => sheets[0].perform('', {}, 'hidemy'))
    expect(calls).toEqual([['cabinet', { tab: 'hidemy' }]])
    cleanup(root)
  })
  it('панели не загрузились -- пункт с повтором; повтор перечитывает и открывает лист заново', async () => {
    A.role = 'owner'
    let n = 0
    A.issuable = () => (n++ === 0 ? Promise.reject(new Error('502')) : Promise.resolve({ panels: [{ id: 'main', label: 'Main', ifaces: [] }] }))
    const sheets = []
    const root = await mount({ openSheet: (s) => sheets.push(s), layer: null, layerParams: {}, openLayer: () => {}, closeLayer: () => {} })
    await act(async () => root.querySelector('.btn-primary').click())
    expect(sheets[0].choices.find((c) => c.value === 'awg3-retry').pill.text).toBe('не загрузилось — повторить')
    let resp
    await act(async () => { resp = await sheets[0].perform('', {}, 'awg3-retry') })
    await act(async () => { sheets[0].onDone(resp); await new Promise((r) => setTimeout(r, 0)) })
    expect(sheets[1].choices.map((c) => c.value)).toContain('awg3')
    cleanup(root)
  })
  it('строка «Маршруты: куда идёт трафик» на месте', async () => {
    A.role = 'owner'
    A.issuable = () => Promise.resolve({ panels: [] })
    const root = await mount({ layer: null, layerParams: {}, openLayer: () => {}, closeLayer: () => {} })
    expect([...root.querySelectorAll('.list-row')].some((r) => r.textContent.includes('Маршруты: куда идёт трафик'))).toBe(true)
    expect(root.textContent).not.toContain('из кабинета')
    cleanup(root)
  })

  // Восстановлено после батча A (вход в загрузку .conf переехал из NavCard
  // в лист «Откуда взять конфиг»). M5: право на .conf -- cabinetPerms(role).manage,
  // оператор тоже (решение оператора 01.10); роль не прочиталась -- пункта нет.
  for (const [role, isAdmin, expected] of [
    ['admin', true, ['amnezia', 'hidemy', 'selfhosted', 'conf']],
    ['owner', false, ['amnezia', 'hidemy', 'conf']],
    ['operator', false, ['amnezia', 'hidemy', 'conf']],
    ['', false, ['amnezia', 'hidemy']],
  ]) {
    it(`загрузка .conf в листе по роли «${role || 'не прочиталась'}»`, async () => {
      A.role = role
      A.issuable = () => Promise.resolve({ panels: [] })
      const sheets = []
      const root = await mount({ isAdmin, openSheet: (s) => sheets.push(s), layer: null, layerParams: {}, openLayer: () => {}, closeLayer: () => {} })
      await act(async () => root.querySelector('.btn-primary').click())
      expect(sheets[0].choices.map((c) => c.value)).toEqual(expected)
      cleanup(root)
    })
  }

  it('выбор «Загрузить .conf» открывает слой загрузки', async () => {
    A.role = 'operator'
    A.issuable = () => Promise.resolve({ panels: [] })
    const sheets = []
    const calls = []
    const root = await mount({ openSheet: (s) => sheets.push(s), layer: null, layerParams: {}, openLayer: (o, p) => calls.push([o, p]), closeLayer: () => {} })
    await act(async () => root.querySelector('.btn-primary').click())
    expect(sheets[0].choices.find((c) => c.value === 'conf').label).toBe('Загрузить .conf')
    await act(async () => sheets[0].perform('', {}, 'conf'))
    expect(calls).toEqual([['confimport', undefined]])
    cleanup(root)
  })
})
