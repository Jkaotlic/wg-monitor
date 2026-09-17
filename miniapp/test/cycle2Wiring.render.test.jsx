// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ props: {}, session: null, routers: null, routerCalls: 0 }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchSession: () => Promise.resolve(mocks.session),
  createSession: () => Promise.resolve(mocks.session),
  fetchRouters: () => {
    mocks.routerCalls++
    return Promise.resolve(mocks.routers)
  },
}))

vi.mock('../src/screens/ProvisionWizard.jsx', () => ({
  ProvisionWizard: (p) => {
    mocks.props.provision = p
    return <div class="stub stub-provision">мастер</div>
  },
}))
vi.mock('../src/screens/JobProgress.jsx', () => ({
  JobProgress: (p) => {
    mocks.props.job = p
    return <div class="stub stub-job">ход {p.jobId}</div>
  },
}))
vi.mock('../src/screens/BackendDeployWait.jsx', () => ({
  BackendDeployWait: (p) => {
    mocks.props.deploy = p
    return <div class="stub stub-deploy">раскатка {p.targetVersion}</div>
  },
}))
vi.mock('../src/screens/AgentConnectionScreen.jsx', () => ({
  AgentConnectionScreen: (p) => {
    mocks.props.conn = p
    return <div class="stub stub-conn">подключение {p.routerID}</div>
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
vi.mock('../src/screens/TunnelsTab.jsx', () => ({ TunnelsTab: () => <div class="stub">туннели</div> }))
vi.mock('../src/screens/DiagTab.jsx', () => ({ DiagTab: () => <div class="stub">проверки</div> }))
vi.mock('../src/screens/EventsTab.jsx', () => ({ EventsTab: () => <div class="stub">события</div> }))

const { OverlayHost, returnLabel } = await import('../src/screens/OverlayHost.jsx')
const { WideLayout } = await import('../src/ui/WideLayout.jsx')
const { PhoneLayout } = await import('../src/ui/PhoneLayout.jsx')
const { App } = await import('../src/App.jsx')

const ROUTERS = [
  { id: 1, nickname: 'dom', status: 'online', last_seen_age_sec: 20 },
  { id: 2, nickname: 'dacha', status: 'online', last_seen_age_sec: 40 },
]
const nav = (over) => ({ routerID: null, tab: 'router', overlay: null, sheet: null, ...over })
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)

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
  const refreshes = { n: 0 }
  const node = (
    <OverlayHost
      nav={navState}
      dispatch={(a) => actions.push(a)}
      routers={ROUTERS}
      isAdmin={isAdmin}
      refreshRouters={() => {
        refreshes.n++
        return Promise.resolve()
      }}
    />
  )
  return { node, actions, refreshes }
}

beforeEach(() => {
  mocks.props = {}
  mocks.session = { ok: true, is_admin: true, via: 'web' }
  mocks.routers = { routers: ROUTERS }
  mocks.routerCalls = 0
})

afterEach(() => {
  delete window.matchMedia
})

describe('OverlayHost: слои парка', () => {
  it('подпись возврата', () => {
    expect(returnLabel('admin')).toBe('Обслуживание')
    expect(returnLabel(null)).toBe('Роутеры')
  })

  it('мастер из Обслуживания: запуск ведёт в «Ход работы» с тем же возвратом, пароли не передаются', async () => {
    const h = host(nav({ routerID: 1, overlay: 'provision', overlayParams: { returnTo: 'admin' } }))
    const root = await mount(h.node)
    expect(root.querySelector('.stub-provision')).toBeTruthy()
    const p = mocks.props.provision
    expect(p.backLabel).toBe('Обслуживание')
    p.onStarted({ jobId: 'j1', nickname: 'dacha-1' })
    expect(h.actions.pop()).toEqual({ type: 'overlay', overlay: 'job', params: { jobId: 'j1', title: 'Установка агента на «dacha-1»', returnTo: 'admin' } })
    p.onClose()
    expect(h.actions.pop()).toEqual({ type: 'overlay', overlay: 'admin' })
    p.onRegistered()
    expect(h.refreshes.n).toBe(1)
    cleanup(root)
  })

  it('«Ход работы»: «Открыть роутер» -- сперва свежий список, потом роутер', async () => {
    const h = host(nav({ overlay: 'job', overlayParams: { jobId: 'j7', title: 'Установка агента на «car»', returnTo: null } }))
    const root = await mount(h.node)
    const p = mocks.props.job
    expect(p.jobId).toBe('j7')
    expect(p.title).toBe('Установка агента на «car»')
    expect(p.backLabel).toBe('Роутеры')
    await act(async () => p.onOpenRouter(9))
    expect(h.refreshes.n).toBe(1)
    expect(h.actions.pop()).toEqual({ type: 'router', id: 9 })
    p.onDone({ state: 'success' })
    expect(h.refreshes.n).toBe(2)
    p.onClose()
    expect(h.actions.pop()).toEqual({ type: 'overlay', overlay: null })
    cleanup(root)
  })

  it('ожидание раскатки: версия из параметров, «Вернуться» -- туда, откуда пришли', async () => {
    const h = host(nav({ routerID: 1, overlay: 'backenddeploy', overlayParams: { targetVersion: 'v0.36.0', returnTo: 'admin' } }))
    const root = await mount(h.node)
    expect(root.querySelector('.stub-deploy').textContent).toBe('раскатка v0.36.0')
    mocks.props.deploy.onBack()
    expect(h.actions.pop()).toEqual({ type: 'overlay', overlay: 'admin' })
    cleanup(root)
  })

  it('не-админу слоёв парка нет', async () => {
    for (const overlay of ['provision', 'job', 'backenddeploy']) {
      const h = host(nav({ routerID: 1, overlay, overlayParams: { jobId: 'j', targetVersion: 'v1.0.0' } }), { isAdmin: false })
      const root = await mount(h.node)
      expect(root.innerHTML, overlay).toBe('')
      cleanup(root)
    }
  })

  it('подключение агента: админу экран, закрытие -- в Обслуживание; не-админу -- слова', async () => {
    let h = host(nav({ routerID: 2, overlay: 'agentconn' }))
    let root = await mount(h.node)
    expect(mocks.props.conn.routerID).toBe(2)
    expect(mocks.props.conn.routerName).toBe('dacha')
    mocks.props.conn.onClose()
    expect(h.actions.pop()).toEqual({ type: 'overlay', overlay: 'admin' })
    cleanup(root)

    h = host(nav({ routerID: 2, overlay: 'agentconn' }), { isAdmin: false })
    root = await mount(h.node)
    expect(root.textContent).toContain('Этот экран доступен только администратору.')
    cleanup(root)
  })

  it('Обслуживание: вход в подключение агента и openLayer в Парк с возвратом admin', async () => {
    const h = host(nav({ routerID: 1, overlay: 'admin' }))
    const root = await mount(h.node)
    expect(root.textContent).toContain('Подключение агента')
    await act(async () => button(root, 'Открыть подключение агента').click())
    expect(h.actions.pop()).toEqual({ type: 'overlay', overlay: 'agentconn' })
    mocks.props.park.openLayer('provision')
    expect(h.actions.pop()).toEqual({ type: 'overlay', overlay: 'provision', params: { returnTo: 'admin' } })
    mocks.props.park.openLayer('job', { jobId: 'j2', title: 't' })
    expect(h.actions.pop()).toEqual({ type: 'overlay', overlay: 'job', params: { jobId: 'j2', title: 't', returnTo: 'admin' } })
    cleanup(root)
  })

  it('Обслуживание не-админу: входа в подключение нет', async () => {
    const h = host(nav({ routerID: 1, overlay: 'admin' }), { isAdmin: false })
    const root = await mount(h.node)
    expect(button(root, 'Открыть подключение агента')).toBeFalsy()
    cleanup(root)
  })
})

describe('раскладки', () => {
  it('широкая без роутера: слой парка в основной области вместо сводки, «Парк» подсвечен', async () => {
    const actions = []
    const root = await mount(
      <WideLayout mode="web" nav={nav({ overlay: 'provision', overlayParams: { returnTo: null } })} dispatch={(a) => actions.push(a)} routers={ROUTERS} isAdmin />,
    )
    expect(root.querySelector('.main-content.main-content-narrow .stub-provision')).toBeTruthy()
    expect(root.querySelector('.fleet-home')).toBe(null)
    expect(root.querySelector('.side-link.side-link-active').textContent).toBe('Парк')
    cleanup(root)
  })

  it('широкая без роутера: Парк под сводкой открывает слои с возвратом null', async () => {
    const actions = []
    const root = await mount(<WideLayout mode="web" nav={nav({})} dispatch={(a) => actions.push(a)} routers={ROUTERS} isAdmin />)
    expect(root.querySelector('.fleet-home .stub-park')).toBeTruthy()
    mocks.props.park.openLayer('provision')
    expect(actions.pop()).toEqual({ type: 'overlay', overlay: 'provision', params: { returnTo: null } })
    cleanup(root)
  })

  it('широкая с роутером: слой парка в основной области под шапкой роутера', async () => {
    const root = await mount(
      <WideLayout mode="web" nav={nav({ routerID: 1, overlay: 'job', overlayParams: { jobId: 'j3', title: 't', returnTo: 'admin' } })} dispatch={() => {}} routers={ROUTERS} isAdmin />,
    )
    expect(root.querySelector('.main-content .stub-job').textContent).toBe('ход j3')
    expect(root.querySelector('.stub-router')).toBe(null)
    cleanup(root)
  })

  it('телефон: слой парка поверх экрана, refreshRouters доходит до экрана', async () => {
    let refreshed = 0
    const root = await mount(
      <PhoneLayout
        nav={nav({ routerID: 1, overlay: 'job', overlayParams: { jobId: 'j4', title: 't', returnTo: 'admin' } })}
        dispatch={() => {}}
        routers={ROUTERS}
        isAdmin
        refreshRouters={() => {
          refreshed++
          return Promise.resolve()
        }}
      />,
    )
    expect(root.querySelector('.stub-job').textContent).toBe('ход j4')
    mocks.props.job.onDone({ state: 'success' })
    expect(refreshed).toBe(1)
    cleanup(root)
  })

  it('App: новый роутер после установки открывается -- список переспрошен', async () => {
    window.matchMedia = () => ({ matches: true, addEventListener() {}, removeEventListener() {} })
    window.history.replaceState(null, '', '/dashboard/')
    const root = await mount(<App />)
    await flush()
    mocks.routers = { routers: [...ROUTERS, { id: 9, nickname: 'car', status: 'online', last_seen_age_sec: 5 }] }
    const before = mocks.routerCalls
    await act(async () => mocks.props.park.openLayer('job', { jobId: 'j9', title: 'Установка агента на «car»' }))
    await flush()
    expect(root.querySelector('.stub-job').textContent).toBe('ход j9')
    // Слой без адреса: адрес не меняется.
    expect(window.location.search).toBe('')
    await act(async () => mocks.props.job.onOpenRouter(9))
    await flush()
    expect(mocks.routerCalls).toBeGreaterThan(before)
    expect(root.querySelector('.stub-router').textContent).toBe('Сейчас 9')
    cleanup(root)
  })
})
