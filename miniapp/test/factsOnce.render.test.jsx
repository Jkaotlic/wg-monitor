// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// A1.3 (v0.55): факты роутера -- один запрос на экран, сколько бы секций их ни читало.
const mocks = vi.hoisted(() => ({ calls: [] }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterFacts: (id) => {
    mocks.calls.push(id)
    return Promise.resolve({ supported: true, exit: { stale: false, tunnels: {} }, wan: { stale: false, links: [] }, ping_fails_24h: {} })
  },
}))

const { ExitIPSection, WANSection } = await import('../src/screens/SignalSections.jsx')

const tick = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

describe('факты роутера: один запрос на экран', () => {
  it('две секции «Проверок» читают /facts одним запросом', async () => {
    mocks.calls = []
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () =>
      render(
        <>
          <ExitIPSection routerID={2} tunnels={[]} deadline={{ deadlineMs: 1000 }} />
          <WANSection routerID={2} />
        </>,
        root,
      ),
    )
    await tick()
    expect(mocks.calls).toEqual([2])
    render(null, root)
  })

  it('другой роутер -- свой запрос; повторный вход на экран читает заново', async () => {
    mocks.calls = []
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<WANSection routerID={3} />, root))
    await tick()
    render(null, root)
    await act(async () => render(<WANSection routerID={3} />, root))
    await tick()
    expect(mocks.calls).toEqual([3, 3])
    render(null, root)
  })
})
