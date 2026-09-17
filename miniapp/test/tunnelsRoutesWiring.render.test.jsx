// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ props: {} }))

vi.mock('../src/screens/TunnelsTab.jsx', () => ({
  TunnelsTab: (p) => {
    mocks.props.tunnels = p
    return <div class="stub">туннели</div>
  },
}))
vi.mock('../src/screens/RoutesTab.jsx', () => ({
  RoutesTab: (p) => {
    mocks.props.routes = p
    return <div class="stub">маршруты</div>
  },
}))
vi.mock('../src/screens/RouterDetail.jsx', () => ({ RouterDetail: () => <div class="stub" /> }))
vi.mock('../src/screens/DiagTab.jsx', () => ({ DiagTab: () => <div class="stub" /> }))
vi.mock('../src/screens/EventsTab.jsx', () => ({ EventsTab: () => <div class="stub" /> }))

const { TabBody } = await import('../src/screens/TabBody.jsx')
const { OverlayHost } = await import('../src/screens/OverlayHost.jsx')
const { navReducer } = await import('../src/nav.js')
const { urlFromNav } = await import('../src/navUrl.js')

const ROUTERS = [{ id: 7, nickname: 'home', status: 'online' }]
const base = { routerID: 7, tab: 'tunnels', overlay: null, sheet: null }

async function draw(vnode) {
  const root = document.createElement('div')
  await act(async () => render(vnode, root))
  return root
}

describe('VPN-туннели → перенос в «Маршрутах»', () => {
  it('вкладка открывает «Маршруты» с VPN-туннелем для переноса; id в адрес не пишется', async () => {
    const sent = []
    const root = await draw(<TabBody nav={base} dispatch={(a) => sent.push(a)} routers={ROUTERS} isAdmin={false} />)
    expect(mocks.props.tunnels.routesOpen).toBe(false)
    mocks.props.tunnels.onOpenRebind('nwg1')
    expect(sent).toEqual([{ type: 'overlay', overlay: 'routes', params: { rebindFrom: 'nwg1' } }])
    const next = navReducer(base, sent[0])
    expect(next).toEqual({ ...base, overlay: 'routes', overlayParams: { rebindFrom: 'nwg1' } })
    expect(urlFromNav(next)).toBe('?router=7&tab=tunnels&open=routes')
    await act(async () => render(<TabBody nav={next} dispatch={() => {}} routers={ROUTERS} isAdmin={false} />, root))
    expect(mocks.props.tunnels.routesOpen).toBe(true)
    render(null, root)
  })

  it('слой «Маршруты» передаёт VPN-туннель; без параметров -- пусто', async () => {
    let root = await draw(<OverlayHost nav={{ ...base, overlay: 'routes', overlayParams: { rebindFrom: 'nwg1' } }} dispatch={() => {}} routers={ROUTERS} isAdmin={false} />)
    expect(mocks.props.routes.rebindFrom).toBe('nwg1')
    render(null, root)
    root = await draw(<OverlayHost nav={{ ...base, overlay: 'routes' }} dispatch={() => {}} routers={ROUTERS} isAdmin={false} />)
    expect(mocks.props.routes.rebindFrom).toBe('')
    render(null, root)
  })
})
