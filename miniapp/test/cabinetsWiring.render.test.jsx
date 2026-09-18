// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ props: {} }))

vi.mock('../src/screens/CabinetScreen.jsx', () => ({
  CabinetScreen: (p) => {
    mocks.props.cabinet = p
    return <div class="stub stub-cabinet">кабинет {p.routerID}</div>
  },
}))
vi.mock('../src/screens/SelfhostedScreen.jsx', () => ({
  SelfhostedScreen: (p) => {
    mocks.props.list = p
    return <div class="stub stub-selfhosted">свои серверы</div>
  },
}))
vi.mock('../src/screens/SelfhostedInstanceScreen.jsx', () => ({
  SelfhostedInstanceScreen: (p) => {
    mocks.props.inst = p
    return <div class="stub stub-selfhostedinst">сервер {p.instanceId}</div>
  },
}))
vi.mock('../src/screens/TunnelsTab.jsx', () => ({
  TunnelsTab: (p) => {
    mocks.props.tunnels = p
    return <div class="stub stub-tunnels">туннели</div>
  },
}))
vi.mock('../src/screens/ParkSection.jsx', () => ({
  ParkSection: (p) => {
    mocks.props.park = p
    return <div class="stub stub-park">парк</div>
  },
}))
vi.mock('../src/screens/AccessSection.jsx', () => ({ AccessSection: () => <div class="stub">доступ</div> }))
vi.mock('../src/screens/RouterDetail.jsx', () => ({ RouterDetail: ({ id }) => <div class="stub stub-router">Сейчас {id}</div> }))
vi.mock('../src/screens/DiagTab.jsx', () => ({ DiagTab: () => <div class="stub">проверки</div> }))
vi.mock('../src/screens/EventsTab.jsx', () => ({ EventsTab: () => <div class="stub">события</div> }))

const { OverlayHost, returnLabel } = await import('../src/screens/OverlayHost.jsx')
const { TabBody } = await import('../src/screens/TabBody.jsx')
const { WideLayout } = await import('../src/ui/WideLayout.jsx')

const ROUTERS = [
  { id: 1, nickname: 'dom', status: 'online', last_seen_age_sec: 20 },
  { id: 2, nickname: 'dacha', status: 'offline', last_seen_age_sec: 4000 },
]
const nav = (over) => ({ routerID: null, tab: 'router', overlay: null, sheet: null, ...over })
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function mount(node) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(node, root))
  await flush()
  return root
}
const cleanup = (root) => { render(null, root); root.remove() }

function host(navState, { isAdmin = true } = {}) {
  const actions = []
  const node = <OverlayHost nav={navState} dispatch={(a) => actions.push(a)} routers={ROUTERS} isAdmin={isAdmin} refreshRouters={() => Promise.resolve()} />
  return { node, actions }
}

beforeEach(() => {
  mocks.props = {}
})

describe('кабинет роутера -- слой навигации', () => {
  it('OverlayHost: роутер, имя, сон, лист и закрытие', async () => {
    const h = host(nav({ routerID: 2, tab: 'tunnels', overlay: 'cabinet' }), { isAdmin: false })
    const root = await mount(h.node)
    const p = mocks.props.cabinet
    expect(p.routerID).toBe(2)
    expect(p.routerName).toBe('dacha')
    expect(p.asleep).toBe(true)
    p.openSheet({ title: 'x' })
    expect(h.actions.pop()).toEqual({ type: 'sheet', sheet: { title: 'x' } })
    p.onClose()
    expect(h.actions.pop()).toEqual({ type: 'overlay', overlay: null })
    cleanup(root)
  })

  it('TabBody: вход в кабинет -- действие навигации, признак открытого кабинета', async () => {
    const actions = []
    let root = await mount(<TabBody nav={nav({ routerID: 1, tab: 'tunnels' })} dispatch={(a) => actions.push(a)} routers={ROUTERS} isAdmin={false} />)
    expect(mocks.props.tunnels.cabinetOpen).toBe(false)
    mocks.props.tunnels.onOpenCabinet()
    expect(actions.pop()).toEqual({ type: 'overlay', overlay: 'cabinet' })
    cleanup(root)

    root = await mount(<TabBody nav={nav({ routerID: 1, tab: 'tunnels', overlay: 'cabinet' })} dispatch={() => {}} routers={ROUTERS} isAdmin={false} />)
    expect(mocks.props.tunnels.cabinetOpen).toBe(true)
    cleanup(root)
  })
})

describe('свои серверы -- слои парка', () => {
  it('подпись возврата на список', () => {
    expect(returnLabel('selfhosted')).toBe('Свои серверы')
    expect(returnLabel('fleet')).toBe('Мои роутеры')
    expect(returnLabel(null)).toBe('Роутеры')
  })

  it('список из Парка на «Моих роутерах»: открыть сервер -- с возвратом на список и его возвратом', async () => {
    const h = host(nav({ routerID: 1, overlay: 'selfhosted', overlayParams: { returnTo: 'fleet' } }))
    const root = await mount(h.node)
    const p = mocks.props.list
    expect(p.backLabel).toBe('Мои роутеры')
    p.onOpenInstance('ams')
    expect(h.actions.pop()).toEqual({
      type: 'overlay',
      overlay: 'selfhostedinst',
      params: { instanceId: 'ams', returnTo: 'selfhosted', returnParams: { returnTo: 'fleet' } },
    })
    p.onOpenInstance('')
    expect(h.actions.pop().params.instanceId).toBe('')
    p.onClose()
    expect(h.actions.pop()).toEqual({ type: 'overlay', overlay: 'fleet' })
    cleanup(root)
  })

  it('экран сервера: id, подпись, лист; «назад» -- на список с прежним возвратом', async () => {
    const h = host(nav({ overlay: 'selfhostedinst', overlayParams: { instanceId: 'ams', returnTo: 'selfhosted', returnParams: { returnTo: null } } }))
    const root = await mount(h.node)
    const p = mocks.props.inst
    expect(p.instanceId).toBe('ams')
    expect(p.backLabel).toBe('Свои серверы')
    p.openSheet({ title: 'Удалить?' })
    expect(h.actions.pop()).toEqual({ type: 'sheet', sheet: { title: 'Удалить?' } })
    p.onClose()
    expect(h.actions.pop()).toEqual({ type: 'overlay', overlay: 'selfhosted', params: { returnTo: null } })
    cleanup(root)
  })

  it('не-админ по адресу: список -- слова; экран сервера -- ничего', async () => {
    let h = host(nav({ routerID: 1, overlay: 'selfhosted', overlayParams: { returnTo: 'admin' } }), { isAdmin: false })
    let root = await mount(h.node)
    expect(root.querySelector('.stub-selfhosted')).toBe(null)
    expect(root.textContent).toContain('Этот экран доступен только администратору.')
    cleanup(root)

    h = host(nav({ overlay: 'selfhostedinst', overlayParams: { instanceId: 'ams', returnTo: 'selfhosted' } }), { isAdmin: false })
    root = await mount(h.node)
    expect(root.innerHTML).toBe('')
    cleanup(root)
  })

  it('широкая без роутера: список в основной области вместо сводки, «Парк» подсвечен', async () => {
    const root = await mount(
      <WideLayout mode="web" nav={nav({ overlay: 'selfhosted', overlayParams: { returnTo: null } })} dispatch={() => {}} routers={ROUTERS} isAdmin />,
    )
    expect(root.querySelector('.main-content.main-content-narrow .stub-selfhosted')).toBeTruthy()
    expect(root.querySelector('.fleet-home')).toBe(null)
    expect(root.querySelector('.side-link.side-link-active').textContent).toBe('Парк')
    cleanup(root)
  })
})
