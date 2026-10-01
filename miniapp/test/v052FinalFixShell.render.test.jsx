// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Финальное ревью v0.52: п. 6 (полоса роутеров) и п. 7 (кнопка «назад» Telegram
// гаснет, пока слой закреплён).
const H = vi.hoisted(() => ({ visible: [], dispatch: null }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchSession: () => Promise.resolve({ ok: true, is_admin: true, via: 'telegram' }),
  createSession: () => Promise.resolve({ ok: true, is_admin: true, via: 'telegram' }),
  fetchRouters: () => Promise.resolve({ routers: [{ id: 2, nickname: 'Дача', status: 'online', reach: 'online', last_seen_age_sec: 30 }] }),
}))
vi.mock('../src/telegram.js', async (importOriginal) => ({
  ...(await importOriginal()),
  setBackButtonVisible: (v) => H.visible.push(v),
}))
vi.mock('../src/ui/PhoneLayout.jsx', () => ({
  PhoneLayout: ({ dispatch }) => {
    H.dispatch = dispatch
    return <div class="stub">phone</div>
  },
}))

const { App } = await import('../src/App.jsx')
const { RouterStrip } = await import('../src/ui/RouterStrip.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

beforeEach(() => {
  delete window.matchMedia
  H.visible.length = 0
})

describe('п. 7: BackButton Telegram и закреплённый слой', () => {
  it('закрепили слой -- кнопка прячется, открепили -- возвращается', async () => {
    window.history.replaceState(null, '', '/miniapp/?router=2')
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<App />, root))
    await flush()
    await flush()
    await act(async () => H.dispatch({ type: 'overlay', overlay: 'provision' }))
    expect(H.visible.at(-1)).toBe(true)
    await act(async () => H.dispatch({ type: 'pin', pinned: true }))
    expect(H.visible.at(-1)).toBe(false)
    await act(async () => H.dispatch({ type: 'pin', pinned: false }))
    expect(H.visible.at(-1)).toBe(true)
    render(null, root)
    root.remove()
  })
})

describe('п. 6: текущий чип остаётся в кадре после перестановки', () => {
  const chip = (id, current = false, alert = false) => ({ id, name: `r${id}`, state: 'ok', tone: 'ok', current, alert })
  it('порядок сменился при том же числе чипов и том же текущем -- полоса перематывается', async () => {
    const proto = HTMLElement.prototype
    const saved = ['offsetLeft', 'offsetWidth', 'clientWidth'].map((k) => [k, Object.getOwnPropertyDescriptor(proto, k)])
    Object.defineProperty(proto, 'offsetLeft', { configurable: true, get() { return this.classList?.contains('strip-chip') ? [...this.parentNode.children].indexOf(this) * 100 : 0 } })
    Object.defineProperty(proto, 'offsetWidth', { configurable: true, get() { return this.classList?.contains('strip-chip') ? 90 : 0 } })
    Object.defineProperty(proto, 'clientWidth', { configurable: true, get() { return this.classList?.contains('router-strip') ? 300 : 0 } })
    const root = document.createElement('div')
    document.body.appendChild(root)
    const strip = () => root.querySelector('.router-strip')
    await act(async () => render(<RouterStrip chips={[chip(1), chip(2), chip(3), chip(4, true)]} onPick={() => {}} />, root))
    expect(strip().scrollLeft).toBeGreaterThan(0)
    await act(async () => render(<RouterStrip chips={[chip(4, true), chip(1), chip(2), chip(3)]} onPick={() => {}} />, root))
    expect(strip().scrollLeft).toBe(0)
    render(null, root)
    root.remove()
    for (const [k, d] of saved) {
      if (d) Object.defineProperty(proto, k, d)
      else delete proto[k]
    }
  })
})
