// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ settings: null, versions: null, settingsFn: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => (mocks.settingsFn ? mocks.settingsFn() : Promise.resolve(mocks.settings)),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [{ tunnel_id: 'awg10', name: 'vpn-nl', run_state: 'running', status: 'ok', ping_check_status: 'pass' }] }),
  fetchRouterVersions: () => Promise.resolve(mocks.versions),
  fetchAccess: () => Promise.resolve({ owner: null, operators: [] }),
  fetchAgentConnection: () => Promise.resolve({ awgm_url: 'https://awg.example.com' }),
  fetchRouter: () => Promise.resolve({ router: { id: 2, nickname: 'home', status: 'online', last_seen_age_sec: 30 }, incidents: [] }),
  fetchRouterFacts: () => Promise.resolve(null),
}))

const { ManageTab } = await import('../src/screens/ManageTab.jsx')
const { DiagTab } = await import('../src/screens/DiagTab.jsx')
const { AppContext } = await import('../src/appContext.js')

const noop = () => {}
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)

async function mount(node) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<AppContext.Provider value={{ mode: 'miniapp', wide: false }}>{node}</AppContext.Provider>, root))
  await flush()
  await flush()
  return root
}
const manage = (isAdmin, focusGroup = null) =>
  mount(
    <ManageTab
      routerID={2}
      routerName="home"
      isAdmin={isAdmin}
      focusGroup={focusGroup}
      openSheet={noop}
      openLayer={noop}
      onOpenAgentConfig={noop}
      onOpenAgentConnection={noop}
      onOpenDNSReset={noop}
      onOpenPackages={noop}
    />,
  )
const cleanup = (root) => {
  render(null, root)
  root.remove()
}
const opened = (root) => Object.fromEntries([...root.querySelectorAll('details.manage-group')].map((d) => [d.id, d.open]))
const chips = (root) => [...root.querySelectorAll('.manage-anchors .manage-anchor')].map((b) => b.textContent)

beforeEach(() => {
  mocks.settings = { role: 'admin', agent_version: 'v0.47.0', panel_known: true, panel_scope: 'public', panel_url: 'https://awg.example.com' }
  mocks.versions = null
  mocks.settingsFn = null
})

describe('«Управление» без простыни (спека п. 3.1)', () => {
  it('админ: «Роутер» раскрыт, остальные свёрнуты, чипы все, «Проверить» ушла', async () => {
    const root = await manage(true)
    expect(opened(root)).toEqual({ 'mg-router': true, 'mg-versions': false, 'mg-repair': false, 'mg-settings': false })
    expect(chips(root)).toEqual(['Роутер', 'Версии', 'Починить', 'Настройки'])
    expect(root.textContent).not.toContain('Проверка связи')
    expect(root.textContent).not.toContain('Осмотр роутера')
    expect(root.querySelector('#mg-versions .fold-note').textContent).toBe('версии ещё не получены')
    expect(root.querySelector('#mg-access')).toBeTruthy()
    expect(root.querySelectorAll('.btn-primary')).toHaveLength(0)
    cleanup(root)
  })

  it('чип раскрывает свою группу', async () => {
    const root = await manage(true)
    await act(async () => button(root.querySelector('.manage-anchors'), 'Починить').click())
    await flush()
    expect(root.querySelector('#mg-repair').open).toBe(true)
    await act(async () => button(root.querySelector('.manage-anchors'), 'Настройки').click())
    await flush()
    expect(root.querySelector('#mg-settings').open).toBe(true)
    cleanup(root)
  })

  it('владелец: без «Доступ» и без админских разделов (Review Focus 5)', async () => {
    mocks.settings = { role: 'owner', agent_version: 'v0.47.0' }
    const root = await manage(false)
    expect(chips(root)).toEqual(['Роутер', 'Версии', 'Починить', 'Настройки'])
    expect(root.querySelector('#mg-access')).toBe(null)
    expect(root.textContent).not.toContain('Сброс DNS')
    cleanup(root)
  })

  it('без права обслуживания: ни группы, ни чипа «Починить»', async () => {
    mocks.settings = { role: 'viewer' }
    const root = await manage(false)
    expect(chips(root)).toEqual(['Роутер', 'Версии', 'Настройки'])
    expect(root.querySelector('#mg-repair')).toBe(null)
    cleanup(root)
  })

  it('забота раскрывает группу и красит итог: прошивка -- «Версии», перезагрузка -- «Починить»', async () => {
    mocks.versions = { installed: { awgmgr: '2.19.9' }, rows: [{ component: 'firmware', available: '5.03', installed: '5.02' }], reboot_hint: true }
    const root = await manage(true)
    expect(opened(root)).toMatchObject({ 'mg-versions': true, 'mg-repair': true, 'mg-settings': false })
    expect(root.querySelector('#mg-versions .fold-note').className).toContain('fold-note-danger')
    expect(root.querySelector('#mg-repair .fold-note').className).toContain('fold-note-warn')
    cleanup(root)
  })

  it('без забот группы остаются свёрнутыми', async () => {
    mocks.versions = { installed: { awgmgr: '2.19.9' }, rows: [] }
    const root = await manage(true)
    expect(opened(root)).toEqual({ 'mg-router': true, 'mg-versions': false, 'mg-repair': false, 'mg-settings': false })
    cleanup(root)
  })

  it('настройки ещё грузятся или не прочитались -- «агент старый» не выдумывается', async () => {
    mocks.settingsFn = () => new Promise(() => {})
    let root = await manage(true)
    expect(root.querySelector('#mg-repair')).toBeTruthy()
    expect(root.textContent).not.toContain('агент старый')
    cleanup(root)
    mocks.settingsFn = () => Promise.reject(new Error('boom'))
    root = await manage(true)
    expect(root.querySelector('#mg-repair')).toBeTruthy()
    expect(root.textContent).not.toContain('агент старый')
    cleanup(root)
  })

  it('фокус по старой ссылке раскрывает группу', async () => {
    const root = await manage(true, 'repair')
    expect(root.querySelector('#mg-repair').open).toBe(true)
    expect(root.querySelector('#mg-router').open).toBe(true)
    cleanup(root)
  })

  it('пустые версии -- один блок с «Сверить версии сейчас», а не три одинаковых строки (п. 3.7)', async () => {
    mocks.versions = { installed: {}, unknown: [{ reason: 'no_snapshot' }], rows: [] }
    const root = await manage(true)
    const group = root.querySelector('#mg-versions')
    expect(group.textContent.split('Версии ещё не получены').length - 1).toBe(1)
    expect(group.textContent).not.toContain('сведений нет')
    expect([...group.querySelectorAll('button')].filter((b) => b.textContent.trim() === 'Сверить версии сейчас')).toHaveLength(1)
    cleanup(root)
  })
})

describe('«Проверка связи» и «Осмотр» -- на вкладке «Проверки»', () => {
  it('разделы на месте, журнал -- владельцу с агентом v0.47+', async () => {
    mocks.settings = { role: 'owner', agent_version: 'v0.47.0' }
    const root = await mount(<DiagTab routerID={2} asleep={false} openSheet={noop} />)
    expect(root.textContent).toContain('Проверка связи')
    expect(button(root, 'Проверить связь сейчас')).toBeTruthy()
    expect(button(root, 'Осмотр роутера')).toBeTruthy()
    expect(root.textContent).toContain('Журнал awg-manager')
    expect(root.querySelectorAll('.btn-primary')).toHaveLength(1)
    cleanup(root)
  })
})
