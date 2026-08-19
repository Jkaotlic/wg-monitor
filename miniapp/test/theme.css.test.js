import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, it, expect } from 'vitest'
import { PALETTES, cssVarName } from '../src/theme.js'

const css = readFileSync(fileURLToPath(new URL('../src/style.css', import.meta.url)), 'utf8')

// Значение переменной внутри конкретного селекторного блока.
function declared(selector, varName) {
  const at = css.indexOf(selector)
  if (at < 0) return null
  const block = css.slice(at)
  const body = block.slice(block.indexOf('{'), block.indexOf('}'))
  const m = body.match(new RegExp(`${varName}\\s*:\\s*([^;]+);`))
  return m ? m[1].trim() : null
}

describe('style.css повторяет палитру из theme.js', () => {
  // JS применяет переменные после загрузки; до этого момента страница
  // красится дефолтами из CSS. Разъехавшиеся значения дают вспышку чужого
  // цвета при каждом открытии -- поэтому они сверяются тестом.
  for (const [key, value] of Object.entries(PALETTES.light)) {
    it(`:root задаёт ${cssVarName(key)} как в светлой палитре`, () => {
      expect(declared(':root {', cssVarName(key))).toBe(value)
    })
  }
  for (const [key, value] of Object.entries(PALETTES.dark)) {
    it(`тёмный блок задаёт ${cssVarName(key)} как в тёмной палитре`, () => {
      expect(declared('[data-theme="dark"]', cssVarName(key))).toBe(value)
    })
  }
})

// --- Пилюли статусов -------------------------------------------------
//
// Токен на собственном тинте контраст теряет, поэтому badge-правила
// подтягивают текст к --text. Здесь проверяется результат этой арифметики,
// а не намерение: проценты читаются из самого style.css, так что правка
// стиля без пересчёта контраста уронит тест.

const rgb = (h) => {
  let s = h.replace('#', '')
  if (s.length === 3) s = s.split('').map((c) => c + c).join('')
  return [0, 2, 4].map((i) => parseInt(s.slice(i, i + 2), 16))
}
const toHex = (a) => '#' + a.map((v) => Math.round(v).toString(16).padStart(2, '0')).join('')
const mix = (a, b, p) => toHex(rgb(a).map((v, i) => v * p + rgb(b)[i] * (1 - p)))
const lin = (c) => (c / 255 <= 0.04045 ? c / 255 / 12.92 : Math.pow((c / 255 + 0.055) / 1.055, 2.4))
const luminance = (h) => {
  const [r, g, b] = rgb(h).map(lin)
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
const ratio = (a, b) => {
  const l1 = luminance(a)
  const l2 = luminance(b)
  const [hi, lo] = l1 > l2 ? [l1, l2] : [l2, l1]
  return (hi + 0.05) / (lo + 0.05)
}

const BADGES = [
  ['.badge-online', 'ok'],
  ['.badge-sleeping', 'warn'],
  ['.badge-offline', 'muted'],
  ['.badge-alert', 'danger'],
]

// Достаёт проценты из правила: color-mix(... var(--ok) 80%, var(--text) 20%).
function badgePercents(selector) {
  const at = css.indexOf(selector + ' {')
  const body = css.slice(at, css.indexOf('}', at))
  const ink = body.match(/color:\s*color-mix\(in srgb,\s*var\(--[a-z]+\)\s*(\d+)%,\s*var\(--text\)/)
  const tint = body.match(/background:\s*color-mix\(in srgb,\s*var\(--[a-z]+\)\s*(\d+)%,\s*var\(--surface\)/)
  return { ink: Number(ink?.[1]) / 100, tint: Number(tint?.[1]) / 100 }
}

describe('контраст пилюль статуса', () => {
  for (const scheme of ['light', 'dark']) {
    for (const [selector, token] of BADGES) {
      it(`${scheme}: ${selector} читается на собственном тинте`, () => {
        const p = PALETTES[scheme]
        const { ink, tint } = badgePercents(selector)
        expect(ink).toBeGreaterThan(0)
        expect(tint).toBeGreaterThan(0)
        const fg = mix(p[token], p.text, ink)
        const bg = mix(p[token], p.surface, tint)
        expect(ratio(fg, bg)).toBeGreaterThanOrEqual(4.5)
      })
    }
  }
})
