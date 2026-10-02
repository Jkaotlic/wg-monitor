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

// п. 6 (полоса роутеров) переехал в polishStrip.render.test.jsx: красный чип
// теперь вне прокрутки, геометрия другая.
