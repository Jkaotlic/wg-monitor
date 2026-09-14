// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Экран «Сброс DNS» целиком: вторая преграда (экран не рисуется старому
// агенту), предпросмотр перед кнопкой сброса и полный ответ сброса на экране.
const PREVIEW = 'Предпросмотр сброса DNS — ничего не изменено\n\nУберём (1):\n  − tls upstream 8.8.8.8 sni dns.google\n\nЗаменим на эталонные (9):\n  + tls upstream 9.9.9.9 sni dns.quad9.net\n'
const RESET = 'DNS reset → reference DoT\n\nснимок «до»: /opt/etc/wg-monitor/dns-before-1757600000.txt\n'

const mocks = vi.hoisted(() => ({ settings: null, checks: { checks: [], tunnels: [] }, calls: [], answers: {} }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve(mocks.settings),
  fetchRouterChecks: () => Promise.resolve(mocks.checks),
}))

vi.mock('../src/useCommand.js', async () => {
  const { useState } = await import('preact/hooks')
  return {
    useCommand: () => {
      const [state, setState] = useState({ busy: false, result: null, error: null, errorCode: null })
      return {
        ...state,
        run: (action, args) => {
          mocks.calls.push({ action, args })
          const pick = mocks.answers[action]
          const res = typeof pick === 'function' ? pick(args) : (pick ?? null)
          setState({ busy: false, result: res, error: null, errorCode: null })
          return Promise.resolve(res)
        },
      }
    },
  }
})

const { DNSResetScreen } = await import('../src/screens/DNSResetScreen.jsx')
const { Sheet } = await import('../src/ui/Sheet.jsx')

async function flush() {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
}

async function mount(vnode) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(vnode, root)
  })
  await flush()
  return root
}

const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent === text)

describe('«Сброс DNS»', () => {
  it('старому агенту экран не рисует ни предпросмотра, ни сброса', async () => {
    mocks.settings = { role: 'admin', agent_version: 'v0.30.1' }
    mocks.calls = []
    const root = await mount(<DNSResetScreen routerID={2} routerName="home" openSheet={() => {}} onClose={() => {}} />)
    expect(root.textContent).toContain('Сброс DNS появится после обновления агента на роутере.')
    expect(button(root, 'Посмотреть, что изменится')).toBeUndefined()
    expect(button(root, 'Сбросить DNS')).toBeUndefined()
    expect(mocks.calls).toEqual([])
    render(null, root)
    root.remove()
  })

  it('сброс неактивен до предпросмотра; после -- открывает шит с набором имени и dry_run=false', async () => {
    mocks.settings = { role: 'admin', agent_version: 'v0.31.0' }
    mocks.calls = []
    mocks.answers = { dns_reset: (args) => (args.dry_run ? { status: 'ok', output: PREVIEW } : { status: 'ok', output: RESET }) }
    let sheet = null
    const root = await mount(<DNSResetScreen routerID={2} routerName="home" openSheet={(s) => (sheet = s)} onClose={() => {}} />)

    expect(button(root, 'Сбросить DNS').disabled).toBe(true)
    await act(async () => button(root, 'Посмотреть, что изменится').click())
    await flush()
    expect(mocks.calls).toEqual([{ action: 'dns_reset', args: { dry_run: true } }])
    expect(root.textContent).toContain('Сейчас на роутере 1 строка DNS. Заменим на эталонные: 9.')
    expect(button(root, 'Сбросить DNS').disabled).toBe(false)

    await act(async () => button(root, 'Сбросить DNS').click())
    expect(sheet.action).toBe('dns_reset')
    expect(sheet.args).toEqual({ dry_run: false })
    expect(sheet.confirmPhrase).toBe('home')
    expect(sheet.danger).toBe(true)

    // Результат сброса доезжает до экрана любым исходом, а не только «ok».
    await act(async () => sheet.onResult({ status: 'partial', output: RESET }))
    await flush()
    expect(root.textContent).toContain('Сброс прошёл не целиком')
    expect(root.textContent).toContain('/opt/etc/wg-monitor/dns-before-1757600000.txt')
    // Второй сброс -- только после нового предпросмотра.
    expect(button(root, 'Сбросить DNS').disabled).toBe(true)
    render(null, root)
    root.remove()
  })

  // Ответа на сброс может не быть вовсе (таймаут, спящий роутер), а команда
  // тем временем уже в очереди. Кнопка закрывается в момент отправки, а не по
  // приходу ответа: иначе второй сброс уходил бы без нового предпросмотра.
  it('без ответа на сброс кнопка всё равно закрыта', async () => {
    mocks.settings = { role: 'admin', agent_version: 'v0.31.0' }
    mocks.calls = []
    mocks.answers = { dns_reset: { status: 'ok', output: PREVIEW } }
    let sheet = null
    const root = await mount(<DNSResetScreen routerID={2} routerName="home" openSheet={(s) => (sheet = s)} onClose={() => {}} />)
    await act(async () => button(root, 'Посмотреть, что изменится').click())
    await flush()
    await act(async () => button(root, 'Сбросить DNS').click())
    expect(sheet).toBeTruthy()
    // onResult так и не пришёл.
    expect(button(root, 'Сбросить DNS').disabled).toBe(true)
    render(null, root)
    root.remove()
  })

  it('без имени роутера сброс не открывается: подтверждению нечего набирать', async () => {
    mocks.settings = { role: 'admin', agent_version: 'v0.31.0' }
    mocks.calls = []
    mocks.answers = { dns_reset: { status: 'ok', output: PREVIEW } }
    let opened = false
    const root = await mount(<DNSResetScreen routerID={2} routerName="" openSheet={() => (opened = true)} onClose={() => {}} />)
    await act(async () => button(root, 'Посмотреть, что изменится').click())
    await flush()
    expect(button(root, 'Сбросить DNS').disabled).toBe(true)
    expect(opened).toBe(false)
    render(null, root)
    root.remove()
  })

  it('ответ не предпросмотром -- громкое предупреждение и сброс закрыт', async () => {
    mocks.settings = { role: 'admin', agent_version: 'v0.31.0' }
    mocks.calls = []
    mocks.answers = { dns_reset: { status: 'ok', output: RESET } }
    const root = await mount(<DNSResetScreen routerID={2} routerName="home" openSheet={() => {}} onClose={() => {}} />)
    await act(async () => button(root, 'Посмотреть, что изменится').click())
    await flush()
    expect(root.textContent).toContain('Роутер ответил не предпросмотром')
    expect(button(root, 'Сбросить DNS').disabled).toBe(true)
    render(null, root)
    root.remove()
  })
})

describe('шит передаёт результат экрану', () => {
  it('onResult получает и частичный исход, onDone -- только ok', async () => {
    mocks.answers = { dns_reset: { status: 'partial', output: RESET } }
    const got = []
    let done = 0
    const sheet = { routerID: 2, title: 't', body: 'b', action: 'dns_reset', args: { dry_run: false }, buttonLabel: 'Сбросить', onResult: (r) => got.push(r), onDone: () => done++ }
    const root = await mount(<Sheet sheet={sheet} onClose={() => {}} />)
    await act(async () => button(root, 'Сбросить').click())
    await flush()
    expect(got).toEqual([{ status: 'partial', output: RESET }])
    expect(done).toBe(0)
    render(null, root)
    root.remove()
  })
})
