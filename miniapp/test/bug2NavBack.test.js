import { describe, it, expect } from 'vitest'
import { navReducer, backButtonVisible, escapeAction } from '../src/nav.js'

// Ревью v0.41 (Opus, 18.09).

describe('BUG 2: «назад» во вкладку', () => {
  const layer = { routerID: 3, tab: 'router', overlay: 'job', overlayParams: { jobId: 'j', returnTo: 'manage' }, sheet: null }

  it('returnTo manage -- вкладка «Управление», слой закрыт', () => {
    const back = navReducer(layer, { type: 'back' })
    expect(back).toEqual({ routerID: 3, tab: 'manage', overlay: null, sheet: null })
    expect(backButtonVisible(back)).toBe(false)
    expect(escapeAction(back, { wide: true })).toBe(null)
  })

  it('Esc ведёт туда же', () => {
    const esc = escapeAction(layer, { wide: true })
    expect(navReducer(layer, esc).overlay).toBe(null)
    expect(navReducer(layer, esc).tab).toBe('manage')
  })
})
