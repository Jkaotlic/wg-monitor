// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ props: null }))
vi.mock('../src/screens/TunnelsTab.jsx', () => ({
  TunnelsTab: (p) => {
    mocks.props = p
    return <div class="stub" />
  },
}))
vi.mock('../src/screens/RouterDetail.jsx', () => ({ RouterDetail: () => <div class="stub" /> }))
vi.mock('../src/screens/DiagTab.jsx', () => ({ DiagTab: () => <div class="stub" /> }))
vi.mock('../src/screens/EventsTab.jsx', () => ({ EventsTab: () => <div class="stub" /> }))

const { TabBody } = await import('../src/screens/TabBody.jsx')
const ROUTERS = [{ id: 7, nickname: 'home', status: 'online' }]

async function flags(overlay) {
  const root = document.createElement('div')
  const nav = { routerID: 7, tab: 'tunnels', overlay, sheet: null }
  await act(async () => render(<TabBody nav={nav} dispatch={() => {}} routers={ROUTERS} isAdmin={false} />, root))
  render(null, root)
  return { cabinetOpen: mocks.props.cabinetOpen, routesOpen: mocks.props.routesOpen }
}

describe('v0.52 fix 1: слой в слое -- не закрытие родителя', () => {
  it('cabinetOpen держится на cabinet и его дочерних слоях', async () => {
    for (const o of ['cabinet', 'cabinetissue']) expect(await flags(o)).toEqual({ cabinetOpen: true, routesOpen: false })
    expect(await flags(null)).toEqual({ cabinetOpen: false, routesOpen: false })
  })
  it('routesOpen держится на routes и его дочерних слоях', async () => {
    for (const o of ['routes', 'routepick', 'routeadd']) expect(await flags(o)).toEqual({ cabinetOpen: false, routesOpen: true })
  })
})
