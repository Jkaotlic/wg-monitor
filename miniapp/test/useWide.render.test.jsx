// @vitest-environment jsdom
import { describe, it, expect, afterEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { useWide } from '../src/useWide.js'

function stubMatchMedia(initial) {
  const listeners = new Set()
  const mql = {
    matches: initial,
    media: '',
    addEventListener: (_t, fn) => listeners.add(fn),
    removeEventListener: (_t, fn) => listeners.delete(fn),
  }
  const queries = []
  window.matchMedia = (q) => {
    queries.push(q)
    mql.media = q
    return mql
  }
  return {
    queries,
    listeners,
    set(value) {
      mql.matches = value
      for (const fn of [...listeners]) fn({ matches: value })
    },
  }
}

function Probe() {
  return <span id="probe">{useWide() ? 'wide' : 'phone'}</span>
}

afterEach(() => {
  delete window.matchMedia
})

describe('useWide', () => {
  it('без matchMedia -- телефонная раскладка', async () => {
    delete window.matchMedia
    const root = document.createElement('div')
    await act(async () => render(<Probe />, root))
    expect(root.textContent).toBe('phone')
    render(null, root)
  })

  it('спрашивает порог 1024 px и следит за сменой ширины', async () => {
    const mm = stubMatchMedia(true)
    const root = document.createElement('div')
    await act(async () => render(<Probe />, root))
    expect(mm.queries).toContain('(min-width: 1024px)')
    expect(root.textContent).toBe('wide')
    await act(async () => mm.set(false))
    expect(root.textContent).toBe('phone')
    render(null, root)
    // Отписка при уходе компонента.
    expect(mm.listeners.size).toBe(0)
  })
})
