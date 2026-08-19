import { describe, it, expect } from 'vitest'
import { PALETTES, cssVarName } from '../src/theme.js'

const hex = (h) => {
  let s = h.replace('#', '')
  if (s.length === 3) s = s.split('').map((c) => c + c).join('')
  return [0, 2, 4].map((i) => parseInt(s.slice(i, i + 2), 16) / 255)
}
const lin = (c) => (c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4))
const luminance = (h) => {
  const [r, g, b] = hex(h).map(lin)
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
const ratio = (a, b) => {
  const l1 = luminance(a)
  const l2 = luminance(b)
  const [hi, lo] = l1 > l2 ? [l1, l2] : [l2, l1]
  return (hi + 0.05) / (lo + 0.05)
}

// Каждый токен, которым красят ТЕКСТ, проверяется на обоих фонах своей темы.
// accentFill проверяется наоборот: это фон под фиксированно белым текстом.
const TEXT_TOKENS = ['text', 'muted', 'accent', 'ok', 'warn', 'danger']

describe('контраст палитры', () => {
  for (const scheme of ['light', 'dark']) {
    for (const token of TEXT_TOKENS) {
      for (const bg of ['page', 'surface']) {
        it(`${scheme}: ${token} на ${bg} проходит WCAG AA`, () => {
          const p = PALETTES[scheme]
          expect(ratio(p[token], p[bg])).toBeGreaterThanOrEqual(4.5)
        })
      }
    }
    it(`${scheme}: белый текст на accentFill проходит WCAG AA`, () => {
      expect(ratio('#ffffff', PALETTES[scheme].accentFill)).toBeGreaterThanOrEqual(4.5)
    })
  }
})

describe('имена CSS-переменных', () => {
  it('переводит camelCase в kebab-case с префиксом', () => {
    expect(cssVarName('accentFill')).toBe('--accent-fill')
    expect(cssVarName('page')).toBe('--page')
  })
})
