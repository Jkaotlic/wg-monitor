// @vitest-environment jsdom
import { expect, it, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { useTunnelFreshness } from '../src/useTunnelFreshness.js'
import { tunnelEventFresh } from '../src/tunnelFreshness.js'

it('проверка на открытом экране устаревает без повторного запроса', () => {
  vi.useFakeTimers()
  const offset = 120_000
  const event = { ts: new Date(Date.now() + offset - 298_000).toISOString() }
  function Row() {
    const { nowMs } = useTunnelFreshness(offset)
    return <span>{tunnelEventFresh(event, nowMs) ? 'свежая' : 'устарела'}</span>
  }
  const root = document.createElement('div')
  try {
    act(() => render(<Row />, root))
    expect(root.textContent).toBe('свежая')
    act(() => vi.advanceTimersByTime(10_000))
    expect(root.textContent).toBe('устарела')
  } finally {
    act(() => render(null, root))
    vi.useRealTimers()
  }
})
