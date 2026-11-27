// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ requests: [] }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchTimeline: (routerID, days, opts) => new Promise((resolve, reject) => {
    mocks.requests.push({ routerID, days, opts, resolve, reject })
  }),
}))

const { EventsTab } = await import('../src/screens/EventsTab.jsx')

describe('лента событий', () => {
  it('не применяет ответ прежнего роутера после смены роутера', async () => {
    mocks.requests = []
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<EventsTab routerID={1} routerName="one" />, root))
    expect(mocks.requests).toHaveLength(1)

    await act(async () => render(<EventsTab routerID={2} routerName="two" />, root))
    expect(mocks.requests).toHaveLength(2)
    await act(async () => mocks.requests[1].resolve({ incidents: [], events: [] }))
    expect(root.textContent).toContain('Роутер «two»')
    await act(async () => mocks.requests[0].reject(new Error('late failure')))
    expect(root.textContent).toContain('Роутер «two»')
    expect(root.textContent).not.toContain('Не удалось загрузить')

    render(null, root)
    root.remove()
  })
})
