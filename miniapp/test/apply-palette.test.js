import { describe, it, expect } from 'vitest'
import { PALETTES, applyPalette } from '../src/theme.js'

function fakeRoot() {
  const vars = new Map()
  const attrs = new Map()
  return {
    style: { setProperty: (k, v) => vars.set(k, v) },
    setAttribute: (k, v) => attrs.set(k, v),
    vars,
    attrs,
  }
}

describe('applyPalette', () => {
  it('пишет все переменные тёмной палитры', () => {
    const root = fakeRoot()
    applyPalette('dark', root)
    expect(root.vars.get('--page')).toBe(PALETTES.dark.page)
    expect(root.vars.get('--accent-fill')).toBe(PALETTES.dark.accentFill)
    expect(root.vars.size).toBe(Object.keys(PALETTES.dark).length)
  })

  it('ставит data-theme, чтобы дефолты CSS совпадали с применённой темой', () => {
    const root = fakeRoot()
    applyPalette('dark', root)
    expect(root.attrs.get('data-theme')).toBe('dark')
  })

  it('на неизвестной схеме берёт светлую, а не падает', () => {
    const root = fakeRoot()
    applyPalette('нечто', root)
    expect(root.vars.get('--page')).toBe(PALETTES.light.page)
    expect(root.attrs.get('data-theme')).toBe('light')
  })

  it('возвращает применённую палитру -- ею красится хром Telegram', () => {
    expect(applyPalette('dark', fakeRoot())).toBe(PALETTES.dark)
  })
})
