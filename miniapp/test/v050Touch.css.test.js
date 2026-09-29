import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const css = readFileSync(fileURLToPath(new URL('../src/style.css', import.meta.url)), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
const bodies = (sel) => [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].filter(([, s]) => s.trim() === sel).map(([, , b]) => b)
const minHeight = (sel) => Math.max(0, ...bodies(sel).map((b) => Number(b.match(/min-height:\s*(\d+)px/)?.[1] ?? 0)))

describe('цели касания ≥ 40 px (спека п. 3.8)', () => {
  for (const sel of ['.app-header-fleet', '.overlay-back', '.segment-tab', '.filter-chip', '.manage-anchor', '.panel-line']) {
    it(sel, () => expect(minHeight(sel)).toBeGreaterThanOrEqual(40))
  }

  it('.panel-line внутри строки списка -- текст, не цель: без 40 px', () => {
    expect(bodies('.panel-line-text').join('')).toMatch(/min-height:\s*0/)
  })
})

describe('единый отступ между соседними карточками', () => {
  it(':where(.section, .screen) > .card + .card', () => {
    expect(bodies(':where(.section, .screen) > .card + .card').join('')).toMatch(/margin-top:\s*var\(--sp-3\)/)
  })
})

describe('чипы «Управления» липнут под шапкой', () => {
  it('sticky и top = высота шапки', () => {
    const b = bodies('.manage-anchors').join('')
    expect(b).toMatch(/position:\s*sticky/)
    expect(b).toMatch(/top:\s*var\(--app-header-h\)/)
  })
})
