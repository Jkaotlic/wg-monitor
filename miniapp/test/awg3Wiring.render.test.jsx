// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ props: {} }))

vi.mock('../src/screens/SelfhostedScreen.jsx', () => ({
  SelfhostedScreen: (p) => {
    mocks.props.list = p
    return <div class="stub stub-list">список</div>
  },
}))
vi.mock('../src/screens/Awg3PanelScreen.jsx', () => ({
  Awg3PanelScreen: (p) => {
    mocks.props.panel = p
    return <div class="stub stub-panel">панель {p.panelId}</div>
  },
}))
vi.mock('../src/screens/Awg3PanelFormScreen.jsx', () => ({
  Awg3PanelFormScreen: (p) => {
    mocks.props.form = p
    return <div class="stub stub-form">форма {p.panelId}</div>
  },
}))

const { OverlayHost } = await import('../src/screens/OverlayHost.jsx')

const ROUTERS = [{ id: 1, nickname: 'dom', status: 'online' }]
const nav = (over) => ({ routerID: null, tab: 'park', overlay: null, sheet: null, ...over })
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function host(navState, isAdmin = true) {
  const actions = []
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<OverlayHost nav={navState} dispatch={(a) => actions.push(a)} routers={ROUTERS} isAdmin={isAdmin} refreshRouters={() => Promise.resolve()} />, root))
  await flush()
  return { root, actions }
}

beforeEach(() => {
  mocks.props = {}
})

describe('OverlayHost: awg3-панели', () => {
  it('список: строка панели -- экран панели, «Добавить» -- форма; возврат списка сохраняется', async () => {
    const { actions } = await host(nav({ overlay: 'selfhosted', overlayParams: { returnTo: 'park' } }))
    mocks.props.list.onOpenAwg3('main')
    mocks.props.list.onAddAwg3()
    expect(actions).toEqual([
      { type: 'overlay', overlay: 'awg3panel', params: { panelId: 'main', returnTo: 'selfhosted', returnParams: { returnTo: 'park' } } },
      { type: 'overlay', overlay: 'awg3form', params: { panelId: '', returnTo: 'selfhosted', returnParams: { returnTo: 'park' } } },
    ])
  })

  it('экран панели: роутеры парка, «Настройки» -- форма с возвратом на экран, «назад» -- на список', async () => {
    const params = { panelId: 'main', returnTo: 'selfhosted', returnParams: { returnTo: 'park' } }
    const { root, actions } = await host(nav({ overlay: 'awg3panel', overlayParams: params }))
    expect(root.textContent).toContain('панель main')
    expect(mocks.props.panel.routers).toBe(ROUTERS)
    expect(mocks.props.panel.backLabel).toBe('Свои серверы')
    mocks.props.panel.onEdit('main')
    mocks.props.panel.onClose()
    expect(actions).toEqual([
      { type: 'overlay', overlay: 'awg3form', params: { panelId: 'main', returnTo: 'awg3panel', returnParams: params } },
      { type: 'overlay', overlay: 'selfhosted', params: { returnTo: 'park' } },
    ])
  })

  it('форма с экрана панели: «назад» -- на экран, удаление -- на список', async () => {
    const panelParams = { panelId: 'main', returnTo: 'selfhosted', returnParams: { returnTo: 'park' } }
    const { actions } = await host(nav({ overlay: 'awg3form', overlayParams: { panelId: 'main', returnTo: 'awg3panel', returnParams: panelParams } }))
    expect(mocks.props.form.backLabel).toBe('Панель')
    mocks.props.form.onClose()
    mocks.props.form.onDeleted()
    expect(actions).toEqual([
      { type: 'overlay', overlay: 'awg3panel', params: panelParams },
      { type: 'overlay', overlay: 'selfhosted', params: { returnTo: 'park' } },
    ])
  })

  it('не-админу слоёв панелей нет', async () => {
    for (const overlay of ['awg3panel', 'awg3form']) {
      const { root } = await host(nav({ overlay, overlayParams: { panelId: 'main' } }), false)
      expect(root.textContent).toBe('')
    }
  })
})
