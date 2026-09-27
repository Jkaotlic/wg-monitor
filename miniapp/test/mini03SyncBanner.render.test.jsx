// @vitest-environment jsdom
// MINI-03, проводка: метка сбоя опроса видна в оболочке, а не только в хуке.
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ fail: false, wide: true }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchSession: () => Promise.resolve({ ok: true, is_admin: false, via: 'web' }),
  fetchRouters: () =>
    mocks.fail
      ? Promise.reject(Object.assign(new Error('network'), { code: 'network' }))
      : Promise.resolve({ routers: [{ id: 1, nickname: 'vymysel', status: 'online', last_seen_age_sec: 20 }] }),
}))
vi.mock('../src/useWide.js', () => ({ useWide: () => mocks.wide }))
vi.mock('../src/screens/RouterDetail.jsx', () => ({ RouterDetail: () => <div class="stub">Сейчас</div> }))

const { App } = await import('../src/App.jsx')
const { PULSE_MS } = await import('../src/pulse.js')

describe('MINI-03: метка в оболочке', () => {
  it('после сбоя пульса списка -- «нет связи с сервером», после удачи -- нет', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    window.history.replaceState(null, '', '/dashboard/')
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<App />, root))
    await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
    expect(root.textContent).not.toContain('нет связи с сервером')

    mocks.fail = true
    await act(async () => { vi.advanceTimersByTime(PULSE_MS) })
    await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
    expect(root.querySelector('.sync-lost')?.textContent).toMatch(/^нет связи с сервером, данные на \d\d:\d\d$/)

    mocks.fail = false
    await act(async () => { vi.advanceTimersByTime(PULSE_MS) })
    await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
    expect(root.querySelector('.sync-lost')).toBe(null)
    vi.useRealTimers()
    render(null, root)
    root.remove()
  })
  it('телефон: список тоже опрашивается, метка ставится и снимается', async () => {
    mocks.wide = false
    mocks.fail = false
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    window.history.replaceState(null, '', '/dashboard/')
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<App />, root))
    await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
    expect(root.textContent).not.toContain('нет связи с сервером')

    mocks.fail = true
    await act(async () => { vi.advanceTimersByTime(PULSE_MS) })
    await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
    expect(root.querySelector('.sync-lost')?.textContent).toMatch(/^нет связи с сервером, данные на \d\d:\d\d$/)

    mocks.fail = false
    await act(async () => { vi.advanceTimersByTime(PULSE_MS) })
    await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
    expect(root.querySelector('.sync-lost')).toBe(null)
    vi.useRealTimers()
    mocks.wide = true
    render(null, root)
    root.remove()
  })
})
