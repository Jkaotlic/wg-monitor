// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Проводка «Хода работы»: экраны открывают его через openLayer('job', …)
// части 2, а оболочка добавляет returnTo. Сами экраны здесь -- заглушки,
// которые сразу зовут openLayer.
const JOB = { jobId: 'job-9', title: 'Переустановка агента на «home»' }

vi.mock('../src/screens/AdminOverlay.jsx', () => ({
  AdminOverlay: ({ openLayer, routerName }) => (
    <button type="button" class="stub-admin" data-name={routerName} onClick={() => openLayer('job', JOB)}>
      открыть
    </button>
  ),
}))
vi.mock('../src/screens/ParkSection.jsx', () => ({
  ParkSection: ({ openLayer }) => (
    <button type="button" class="stub-park" onClick={() => openLayer('job', JOB)}>
      открыть
    </button>
  ),
}))
vi.mock('../src/useFleetRecheck.js', () => ({ useFleetRecheck: () => ({ batch: null, recheckAll: () => {} }) }))

const { OverlayHost } = await import('../src/screens/OverlayHost.jsx')
const { FleetHome } = await import('../src/screens/FleetHome.jsx')

const ROUTERS = [{ id: 22, nickname: 'home', status: 'online', last_seen_age_sec: 30 }]

async function mount(vnode) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(vnode, root))
  return root
}

describe('открытие «Хода работы»', () => {
  it('из Обслуживания -- действием навигации части 2, имя роутера передано', async () => {
    const dispatch = vi.fn()
    const nav = { routerID: 22, tab: 'router', overlay: 'admin', sheet: null }
    const root = await mount(<OverlayHost nav={nav} dispatch={dispatch} routers={ROUTERS} isAdmin />)
    expect(root.querySelector('.stub-admin').dataset.name).toBe('home')
    await act(async () => root.querySelector('.stub-admin').click())
    expect(dispatch).toHaveBeenCalledWith({ type: 'overlay', overlay: 'job', params: { ...JOB, returnTo: 'admin' } })
    render(null, root)
    root.remove()
  })

  it('из Парка под сводкой широкого экрана -- тем же пропом', async () => {
    const openLayer = vi.fn()
    const root = await mount(<FleetHome routers={ROUTERS} isAdmin onPick={() => {}} openSheet={() => {}} openLayer={openLayer} />)
    await act(async () => root.querySelector('.stub-park').click())
    expect(openLayer).toHaveBeenCalledWith('job', JOB)
    render(null, root)
    root.remove()
  })
})
