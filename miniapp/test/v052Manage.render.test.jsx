// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ settings: null, versions: null }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve(mocks.settings),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [] }),
  fetchRouterVersions: () => Promise.resolve(mocks.versions),
  fetchAccess: () => Promise.resolve({ owner: null, operators: [] }),
  fetchAgentConnection: () => Promise.resolve({ awgm_url: 'https://awg.example.com' }),
  fetchRouterFacts: () => Promise.resolve(null),
}))
const { ManageTab } = await import('../src/screens/ManageTab.jsx')
const { AppContext } = await import('../src/appContext.js')

const noop = () => {}
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)
async function manage(isAdmin, focusGroup = null) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () =>
    render(
      <AppContext.Provider value={{ mode: 'miniapp', wide: false }}>
        <ManageTab routerID={2} routerName="home" isAdmin={isAdmin} focusGroup={focusGroup} openSheet={noop} openLayer={noop} onOpenAgentConfig={noop} onOpenAgentConnection={noop} onOpenDNSReset={noop} onOpenPackages={noop} />
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

beforeEach(() => {
  mocks.settings = { role: 'admin', agent_version: 'v0.47.0', panel_known: true, panel_scope: 'public', panel_url: 'https://awg.example.com' }
  mocks.versions = null
})

const groups = (root) => [...root.querySelectorAll('details.manage-group')].map((d) => d.id)
const titles = (root) => [...root.querySelectorAll('details.manage-group .manage-group-title')].map((t) => t.textContent)

describe('v0.52: «Настройки» -- четыре раздела', () => {
  it('админ: четыре раздела, всё свёрнуто без заботы, лаймов нет', async () => {
    const root = await manage(true)
    expect(groups(root)).toEqual(['mg-service', 'mg-people', 'mg-agent', 'mg-danger'])
    expect(titles(root)).toEqual(['Обслуживание', 'Люди и уведомления', 'Роутер и агент', 'Опасное'])
    expect([...root.querySelectorAll('details.manage-group')].every((d) => !d.open)).toBe(true)
    expect(root.querySelectorAll('.btn-primary')).toHaveLength(0)
    expect(root.querySelector('details.danger-zone')).toBe(null)
    cleanup(root)
  })
  it('дом каждого пункта', async () => {
    const root = await manage(true, 'service')
    const service = root.querySelector('#mg-service')
    for (const t of ['Сверить версии', 'Проверить прошивку', 'Перезагрузить роутер', 'Перезапустить awg-manager', 'Обновить пакеты Entware', 'Открыть пакеты по расписанию', 'Открыть сброс DNS']) {
      expect(button(service, t), t).toBeTruthy()
    }
    expect(button(service, 'Перезапустить HydraRoute')).toBeUndefined()
    expect(root.querySelector('#mg-people').textContent).toContain('Писать мне об этом роутере')
    expect(root.querySelector('#mg-people #mg-access')).toBeTruthy()
    const agent = root.querySelector('#mg-agent')
    expect(agent.textContent).toContain('Панель роутера')
    expect(agent.textContent).toContain('Опрос и тревоги')
    expect(button(agent, 'Открыть настройки агента')).toBeTruthy()
    expect(button(agent, 'Открыть подключение агента')).toBeTruthy()
    expect(button(root.querySelector('#mg-danger'), 'Перенаправить агента')).toBeTruthy()
    cleanup(root)
  })
  it('оператор: «Перезагрузить роутер» постоянно, без плашки; админских пунктов нет', async () => {
    mocks.settings = { role: 'operator', agent_version: 'v0.47.0' }
    mocks.versions = { rows: [], unknown: [] }
    const root = await manage(false, 'service')
    expect(button(root.querySelector('#mg-service'), 'Перезагрузить роутер')).toBeTruthy()
    expect(root.querySelector('#mg-danger')).toBe(null)
    expect(root.querySelector('#mg-access')).toBe(null)
    expect(root.querySelector('#mg-agent').textContent).not.toContain('Панель роутера')
    cleanup(root)
  })
  it('«Опрос и тревоги»: сноски «у бота» больше нет', async () => {
    mocks.settings = { role: 'owner', agent_version: 'v0.47.0' }
    const root = await manage(false, 'agent')
    expect(root.textContent).not.toContain('там, где он запущен')
    expect(root.textContent).toContain('Эти числа меняет администратор')
    cleanup(root)
  })
  it('справка -- под новую схему, Парк админу', async () => {
    const admin = await manage(true)
    const help = [...admin.querySelectorAll('.section')].find((s) => s.textContent.startsWith('Что умеет приложение'))
    for (const w of ['Роутер', 'VPN-туннели', 'Проверки', 'Настройки', 'Парк']) expect(help.textContent).toContain(w)
    expect(help.textContent).not.toContain('Управление')
    cleanup(admin)
    const owner = await manage(false)
    expect(owner.textContent).not.toContain('Парк —')
    cleanup(owner)
  })
  // Перенесено из v050Manage.render.test.jsx (механика прежняя, разделы новые).
  it('чип раскрывает свой раздел', async () => {
    const root = await manage(true)
    const chips = [...root.querySelectorAll('.manage-anchors .manage-anchor')].map((b) => b.textContent)
    expect(chips).toEqual(['Обслуживание', 'Люди', 'Роутер и агент', 'Опасное'])
    await act(async () => button(root.querySelector('.manage-anchors'), 'Люди').click())
    await flush()
    expect(root.querySelector('#mg-people').open).toBe(true)
    cleanup(root)
  })
  it('забота раскрывает «Обслуживание» и красит итог: прошивка -- danger, перезагрузка -- warn', async () => {
    mocks.versions = { installed: { awgmgr: '2.19.9' }, rows: [{ component: 'firmware', available: '5.03', installed: '5.02' }], reboot_hint: true }
    const root = await manage(true)
    expect(root.querySelector('#mg-service').open).toBe(true)
    expect(root.querySelector('#mg-people').open).toBe(false)
    expect(root.querySelector('#mg-service .fold-note').className).toContain('fold-note-danger')
    cleanup(root)
    mocks.versions = { installed: { awgmgr: '2.19.9' }, rows: [], reboot_hint: true }
    const warn = await manage(true)
    expect(warn.querySelector('#mg-service .fold-note').className).toContain('fold-note-warn')
    expect(warn.querySelector('#mg-service').textContent).toContain('Перезагрузить роутер')
    cleanup(warn)
  })
  it('фокус по старой ссылке раскрывает раздел', async () => {
    const root = await manage(true, 'agent')
    expect(root.querySelector('#mg-agent').open).toBe(true)
    expect(root.querySelector('#mg-service').open).toBe(false)
    cleanup(root)
  })
  it('без права обслуживания: ни «Перезагрузить роутер», ни админских пунктов', async () => {
    mocks.settings = { role: 'viewer' }
    const root = await manage(false)
    expect(button(root, 'Перезагрузить роутер')).toBeUndefined()
    expect(root.querySelector('#mg-access')).toBe(null)
    cleanup(root)
  })
  it('настройки ещё грузятся -- «агент старый» не выдумывается', async () => {
    mocks.settings = null
    const root = await manage(true)
    expect(root.textContent).not.toContain('агент старый')
    cleanup(root)
  })
})
