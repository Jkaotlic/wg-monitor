// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Проводка «Хода работы»: экраны открывают его через openLayer('job', …)
// части 2, а оболочка добавляет returnTo. Сами экраны здесь -- заглушки,
// которые сразу зовут openLayer.
const JOB = { jobId: 'job-9', title: 'Переустановка агента на «home»' }

vi.mock('../src/screens/RouterAdminSections.jsx', () => ({
  AdminRepairSections: () => null,
  AdminSettingsSections: () => null,
  AdminAccessSection: () => null,
  AdminDangerZone: ({ openLayer, routerName }) => (
    <button type="button" class="stub-admin" data-name={routerName} onClick={() => openLayer('job', JOB)}>
      открыть
    </button>
  ),
}))
vi.mock('../src/screens/SettingsScreen.jsx', () => ({ SettingsSections: ({ dangerSlot }) => dangerSlot }))
vi.mock('../src/screens/ParkSection.jsx', () => ({
  ParkSection: ({ openLayer }) => (
    <button type="button" class="stub-park" onClick={() => openLayer('job', JOB)}>
      открыть
    </button>
  ),
}))
vi.mock('../src/useFleetRecheck.js', () => ({ useFleetRecheck: () => ({ batch: null, recheckAll: () => {} }) }))

const { OverlayHost } = await import('../src/screens/OverlayHost.jsx')
const { TabBody } = await import('../src/screens/TabBody.jsx')
const { FleetHome } = await import('../src/screens/FleetHome.jsx')

const ROUTERS = [{ id: 22, nickname: 'home', status: 'online', last_seen_age_sec: 30 }]

async function mount(vnode) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(vnode, root))
  return root
}

describe('открытие «Хода работы»', () => {
  it('из «Управления» -- действием навигации части 2, имя роутера передано, возврат во вкладку', async () => {
    const dispatch = vi.fn()
    const nav = { routerID: 22, tab: 'manage', overlay: null, sheet: null }
    const root = await mount(<TabBody nav={nav} dispatch={dispatch} routers={ROUTERS} isAdmin />)
    expect(root.querySelector('.stub-admin').dataset.name).toBe('home')
    await act(async () => root.querySelector('.stub-admin').click())
    expect(dispatch).toHaveBeenCalledWith({ type: 'overlay', overlay: 'job', params: { ...JOB, returnTo: 'manage' } })
    render(null, root)
    root.remove()
  })

  // v0.48: Парк -- вкладка; «Ход работы» из него возвращается в неё, с
  // роутером и без.
  it('из вкладки «Парк» -- с возвратом в Парк', async () => {
    for (const routerID of [22, null]) {
      const dispatch = vi.fn()
      const nav = { routerID, tab: 'park', overlay: null, sheet: null }
      const root = await mount(<TabBody nav={nav} dispatch={dispatch} routers={ROUTERS} isAdmin />)
      await act(async () => root.querySelector('.stub-park').click())
      expect(dispatch).toHaveBeenCalledWith({ type: 'overlay', overlay: 'job', params: { ...JOB, returnTo: 'park' } })
      render(null, root)
      root.remove()
    }
  })

  it('ни «Мои роутеры», ни сводка широкого экрана Парка больше не несут', async () => {
    let root = await mount(<OverlayHost nav={{ routerID: 22, tab: 'router', overlay: 'fleet', sheet: null }} dispatch={() => {}} routers={ROUTERS} isAdmin />)
    expect(root.querySelector('.stub-park')).toBe(null)
    render(null, root)
    root.remove()
    root = await mount(<FleetHome routers={ROUTERS} isAdmin onPick={() => {}} />)
    expect(root.querySelector('.stub-park')).toBe(null)
    render(null, root)
    root.remove()
  })
})
