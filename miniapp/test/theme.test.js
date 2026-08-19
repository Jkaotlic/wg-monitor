import { describe, it, expect } from 'vitest'
import { PALETTE, cssVarName } from '../src/theme.js'

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

// Каждый токен, которым красят ТЕКСТ, проверяется на всех трёх поверхностях:
// фон экрана, карточка и вложенная плитка. Тема одна, поэтому и перебор один.
const TEXT_TOKENS = ['ink', 'dim', 'sig', 'ok', 'warn', 'bad']
const SURFACES = ['bg', 'surf', 'surf2']

describe('контраст палитры', () => {
  for (const token of TEXT_TOKENS) {
    for (const bg of SURFACES) {
      it(`${token} на ${bg} проходит WCAG AA`, () => {
        expect(ratio(PALETTE[token], PALETTE[bg])).toBeGreaterThanOrEqual(4.5)
      })
    }
  }

  // onSig проверяется наоборот: это текст на заливке лаймом (кнопка b-fill),
  // и здесь фоном служит сам сигнальный цвет.
  it('текст на заливке лаймом проходит WCAG AA', () => {
    expect(ratio(PALETTE.onSig, PALETTE.sig)).toBeGreaterThanOrEqual(4.5)
  })

  // Погашенный светодиод -- графика на корпусе, а не текст: ему хватает 3:1.
  // Планка ниже названа явно, чтобы никто не «починил» её до 4.5 по привычке.
  it('погашенный светодиод отличим от корпуса', () => {
    expect(ratio(PALETTE.ledOff, PALETTE.surf)).toBeGreaterThanOrEqual(1.4)
  })
})

describe('имена CSS-переменных', () => {
  it('переводит camelCase в kebab-case с префиксом', () => {
    expect(cssVarName('ledOff')).toBe('--led-off')
    expect(cssVarName('bg')).toBe('--bg')
  })
})
