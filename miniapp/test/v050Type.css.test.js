import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// v0.50, общие правила спеки: смысловой текст не мельче 12 px, коды -- 11 px;
// один ряд действий на всё приложение; схема в герое не прижата к низу.
const raw = readFileSync(fileURLToPath(new URL('../src/style.css', import.meta.url)), 'utf8')
const css = raw.replace(/\/\*[\s\S]*?\*\//g, '')
const rootBlock = css.match(/:root\s*\{([^}]*)\}/)[1]
const tokens = Object.fromEntries([...rootBlock.matchAll(/(--fs-[\w-]+):\s*([\d.]+)rem/g)].map(([, k, v]) => [k, Number(v) * 16]))

function px(value) {
  const v = value.trim()
  let m = v.match(/^var\((--fs-[\w-]+)\)$/)
  if (m) return tokens[m[1]] ?? NaN
  m = v.match(/^([\d.]+)rem$/)
  if (m) return Number(m[1]) * 16
  m = v.match(/^([\d.]+)px$/)
  if (m) return Number(m[1])
  return null
}

// Машинные коды (11 px) и подписи нижней панели (шесть вкладок на 360 px,
// см. отклонение в плане) -- единственные исключения.
const CODE_OK = ['.data-row-code', '.tunnel-id', '.ev-code', '.raw-dump', '.tabbar-item']

function rules(text) {
  return [...text.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map(([, sel, body]) => ({ sel: sel.trim(), body }))
}

describe('шкала шрифтов', () => {
  it('токены: мелкий смысловой -- 12 px, код -- 11 px, 10 px больше нет', () => {
    expect(tokens['--fs-xs']).toBe(12)
    expect(tokens['--fs-code']).toBe(11)
    expect(css).not.toMatch(/--fs-xxs/)
  })

  it('ни одного смыслового текста мельче 12 px', () => {
    const small = []
    for (const { sel, body } of rules(css)) {
      const fs = body.match(/font-size:\s*([^;]+);/)
      if (!fs) continue
      const size = px(fs[1])
      if (size == null || size >= 12) continue
      const allowed = sel.split(',').every((part) => CODE_OK.some((c) => part.includes(c)))
      if (!allowed) small.push(`${sel} → ${size}px`)
    }
    expect(small).toEqual([])
  })
})

describe('ряд действий', () => {
  const body = (sel) => rules(css).find((r) => r.sel === sel)?.body ?? ''

  it('.action-row -- сетка до двух колонок, .action-row-pair -- всегда две', () => {
    expect(body('.action-row')).toMatch(/display:\s*grid/)
    expect(body('.action-row')).toMatch(/grid-template-columns:\s*repeat\(auto-fit/)
    expect(body('.action-row-pair')).toMatch(/grid-template-columns:\s*repeat\(2,\s*minmax\(0,\s*1fr\)\)/)
  })

  it('кнопки ряда -- во всю ячейку и одной высоты', () => {
    expect(body('.action-row > .btn,\n.action-row > .restart-block > .btn')).toMatch(/width:\s*100%/)
  })

  it('старых рядов пар больше нет', () => {
    expect(css).not.toMatch(/\.incident-pair|\.settings-actions/)
  })

  it('схема в герое не прижата к низу', () => {
    expect(body('.hero .traffic-path')).toMatch(/margin-bottom:\s*var\(--sp-4\)/)
  })
})
