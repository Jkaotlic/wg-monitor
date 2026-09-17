import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const css = readFileSync(fileURLToPath(new URL('../src/style.css', import.meta.url)), 'utf8')
const OPEN = '@media (min-width: 1024px) {'

function block(text, open) {
  const start = text.indexOf(open)
  let depth = 0
  for (let i = start + open.length - 1; i < text.length; i++) {
    if (text[i] === '{') depth++
    else if (text[i] === '}') {
      depth--
      if (depth === 0) return { start, end: i + 1, body: text.slice(start, i + 1) }
    }
  }
  return null
}

const wide = block(css, OPEN)
const outside = css.slice(0, wide.start) + css.slice(wide.end)

describe('CSS VPN-туннелей и маршрутов (цикл 4)', () => {
  it('новые экраны оформлены на любой ширине', () => {
    for (const sel of [
      '.tunnel-block {', '.tunnel-outcome {', '.conf-pick {', '.conf-pick-input {', '.conf-pick:focus-within {', '.conf-pick-off {',
      '.conf-file-name {', '.conf-problems {', '.conf-problem {', '.conf-problem:first-child {', '.conf-problem-error {', '.conf-problem-warn {',
      '.hrneo-status {', '.hrneo-actions {', '.hrneo-actions .btn {', '.hrneo-rules {', '.routes-other .row-title {', '.replace-leftover {',
    ]) {
      expect(outside.includes(sel), sel).toBe(true)
    }
  })

  it('поле файла лежит прозрачным слоем поверх кнопки', () => {
    const i = outside.indexOf('.conf-pick-input {')
    const rule = outside.slice(i, outside.indexOf('}', i))
    expect(rule).toMatch(/position:\s*absolute/)
    expect(rule).toMatch(/inset:\s*0/)
    expect(rule).toMatch(/opacity:\s*0/)
    const pick = outside.slice(outside.indexOf('.conf-pick {'), outside.indexOf('}', outside.indexOf('.conf-pick {')))
    expect(pick).toMatch(/position:\s*relative/)
  })

  it('кнопки HydraRoute Neo держат 44 px', () => {
    const i = outside.indexOf('.hrneo-actions .btn {')
    expect(outside.slice(i, outside.indexOf('}', i))).toMatch(/min-height:\s*44px/)
  })

  it('широкая раскладка -- в том же единственном блоке', () => {
    expect(css.split(OPEN).length - 1).toBe(1)
    for (const sel of ['.wide-shell .hrneo-actions .btn {', '.wide-shell .tunnel-delete,', '.wide-shell .conf-import .btn-wide {']) {
      expect(wide.body.includes(sel), sel).toBe(true)
      expect(outside.includes(sel), sel).toBe(false)
    }
  })

  it('цвета -- только токенами', () => {
    const start = outside.indexOf('VPN-туннели и маршруты (цикл 4')
    expect(start).toBeGreaterThan(-1)
    const end = outside.indexOf('.replace-leftover {', start)
    expect(end).toBeGreaterThan(start)
    expect(outside.slice(start, end)).not.toMatch(/#[0-9a-f]{3,8}\b/i)
  })
})
