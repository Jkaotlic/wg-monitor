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

describe('п. 6: текущий чип остаётся в кадре, красный не закрывает его начало', () => {
  const chip = (id, name, current = false, alert = false) => ({ id, name, state: 'ok', tone: 'ok', current, alert })
  // Геометрия, которой нет в jsdom: ширины по имени, чипы друг за другом через
  // 8 px после отступа полосы 16 px; кадр полосы -- 360.
  const W = (n) => (n.includes('wide') ? 200 : n.includes('red') ? 150 : 90)
  async function withLayout(run) {
    const proto = HTMLElement.prototype
    const keys = ['offsetLeft', 'offsetWidth', 'clientWidth']
    const saved = keys.map((k) => [k, Object.getOwnPropertyDescriptor(proto, k)])
    const isChip = (el) => el.classList?.contains('strip-chip')
    Object.defineProperty(proto, 'offsetLeft', {
      configurable: true,
      get() {
        if (!isChip(this)) return 0
        let x = 16
        for (const c of this.parentNode.children) {
          if (c === this) break
          x += W(c.textContent) + 8
        }
        return x
      },
    })
    Object.defineProperty(proto, 'offsetWidth', { configurable: true, get() { return isChip(this) ? W(this.textContent) : 0 } })
    Object.defineProperty(proto, 'clientWidth', { configurable: true, get() { return this.classList?.contains('router-strip') ? 360 : 0 } })
    const root = document.createElement('div')
    document.body.appendChild(root)
    try {
      await run(root)
    } finally {
      render(null, root)
      root.remove()
      for (const [k, d] of saved) {
        if (d) Object.defineProperty(proto, k, d)
        else delete proto[k]
      }
    }
  }
  const strip = (root) => root.querySelector('.router-strip')
  const show = (root, chips) => act(async () => render(<RouterStrip chips={chips} onPick={() => {}} />, root))

  it('порядок сменился при том же числе чипов и том же текущем -- полоса перематывается', async () => {
    await withLayout(async (root) => {
      await show(root, [chip(1, 'a'), chip(2, 'b'), chip(3, 'c'), chip(4, 'd', true)])
      expect(strip(root).scrollLeft).toBeGreaterThan(0)
      await show(root, [chip(4, 'd', true), chip(1, 'a'), chip(2, 'b'), chip(3, 'c')])
      expect(strip(root).scrollLeft).toBe(0)
    })
  })

  it('правая ветка: текущий правее кадра -- прокрутка ровно до его правого края', async () => {
    await withLayout(async (root) => {
      // red 16..166, a 174..264, b 272..362, cur 370..460 (+16 поле) -> 460+16-360 = 116
      await show(root, [chip(1, 'red', false, true), chip(2, 'a'), chip(3, 'b'), chip(4, 'cur', true)])
      expect(strip(root).scrollLeft).toBe(116)
    })
  })

  it('левая ветка: красный закрывает кадр -- текущий начинается правее него', async () => {
    await withLayout(async (root) => {
      const chips = [chip(1, 'red', false, true), chip(2, 'a'), chip(3, 'cur', true)]
      await show(root, chips)
      strip(root).scrollLeft = 500
      // тот же порядок, другой текущий -- эффект сработает от смены currentID
      await show(root, [chip(1, 'red', false, true), chip(2, 'a', true), chip(3, 'cur')])
      // a: left 174, hi = 174 - 16 - (150 + 8) = 0
      expect(strip(root).scrollLeft).toBe(0)
    })
  })

  it('худший случай: красный 150, текущий шире кадра -- побеждает начало имени', async () => {
    await withLayout(async (root) => {
      // cur-wide: left 174; hi = 0; lo = 174+200+16-360 = 30 > hi -> 0
      await show(root, [chip(1, 'red', false, true), chip(2, 'cur-wide', true)])
      expect(strip(root).scrollLeft).toBe(0)
      // красный тот же, но текущий третий: left 16+158+98+... начало не под красным
      await show(root, [chip(1, 'red', false, true), chip(2, 'a'), chip(3, 'cur-wide', true)])
      const left = 16 + 150 + 8 + 90 + 8
      const sl = strip(root).scrollLeft
      expect(left - sl).toBeGreaterThanOrEqual(16 + 150 + 8)
    })
  })
})
