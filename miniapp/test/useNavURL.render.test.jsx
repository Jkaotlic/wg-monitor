// @vitest-environment jsdom
import { describe, it, expect, beforeEach } from 'vitest'
import { render } from 'preact'
import { useReducer } from 'preact/hooks'
import { act } from 'preact/test-utils'
import { navReducer } from '../src/nav.js'
import { navFromURL } from '../src/navUrl.js'
import { useNavURL } from '../src/useNavURL.js'

const IDS = [3, 7]
let api = null

function Probe({ enabled }) {
  const [nav, dispatch] = useReducer(navReducer, navFromURL(window.location.search, IDS))
  useNavURL({ enabled, nav, dispatch, routerIDs: IDS })
  api = { nav, dispatch }
  return null
}

async function mount(enabled = true) {
  const root = document.createElement('div')
  await act(async () => render(<Probe enabled={enabled} />, root))
  return root
}

beforeEach(() => {
  window.history.replaceState(null, '', '/dashboard/?router=7&tab=diag')
})

describe('useNavURL', () => {
  it('первая синхронизация не добавляет запись истории', async () => {
    const before = window.history.length
    const root = await mount()
    expect(window.location.pathname + window.location.search).toBe('/dashboard/?router=7&tab=diag')
    expect(window.history.length).toBe(before)
    render(null, root)
  })

  it('смена вкладки и оверлея -- pushState, лист адрес не меняет', async () => {
    const root = await mount()
    const before = window.history.length
    await act(async () => api.dispatch({ type: 'tab', tab: 'events' }))
    expect(window.location.search).toBe('?router=7&tab=events')
    await act(async () => api.dispatch({ type: 'overlay', overlay: 'settings' }))
    expect(window.location.search).toBe('?router=7&tab=events&open=settings')
    expect(window.history.length).toBe(before + 2)
    await act(async () => api.dispatch({ type: 'sheet', sheet: { title: 'Точно?' } }))
    expect(window.location.search).toBe('?router=7&tab=events&open=settings')
    expect(window.history.length).toBe(before + 2)
    render(null, root)
  })

  it('/dashboard/login после входа заменяется на /dashboard/ с тем же местом', async () => {
    window.history.replaceState(null, '', '/dashboard/login?router=3')
    const root = await mount()
    expect(window.location.pathname + window.location.search).toBe('/dashboard/?router=3')
    render(null, root)
  })

  it('popstate перечитывает навигацию из адреса', async () => {
    const root = await mount()
    window.history.replaceState(null, '', '/dashboard/?router=3&open=admin')
    await act(async () => window.dispatchEvent(new PopStateEvent('popstate')))
    expect(api.nav.routerID).toBe(3)
    expect(api.nav.overlay).toBe('admin')
    render(null, root)
  })

  it('выключенный (Telegram) адрес не трогает', async () => {
    window.history.replaceState(null, '', '/miniapp/?router=7')
    const root = await mount(false)
    await act(async () => api.dispatch({ type: 'tab', tab: 'events' }))
    expect(window.location.pathname + window.location.search).toBe('/miniapp/?router=7')
    render(null, root)
  })
})
