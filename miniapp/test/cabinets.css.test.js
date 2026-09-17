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

describe('CSS кабинетов и своих серверов', () => {
  it('новые экраны оформлены на любой ширине', () => {
    for (const sel of [
      '.segment-tabs {', '.segment-tab {', '.segment-tab-on {', '.segment-tab:focus-visible {',
      '.cabinet-notice {', '.cabinet-secret-active .row-title {', '.cabinet-secret-actions {', '.cabinet-danger {',
      '.cabinet-option-main {', '.cabinet-option-main:disabled {', '.cabinet-outcome {', '.cabinet-send {',
      '.selfhosted-actions {', '.selfhosted-check {', '.selfhosted-check-ok {', '.selfhosted-check-bad {', '.park-selfhosted {',
      '.field-error input {', '.field-error-text {', '.field-warn {',
    ]) {
      expect(outside.includes(sel), sel).toBe(true)
    }
  })

  it('строки кабинета держат кнопки рядом и переносят их на узком экране', () => {
    const i = outside.indexOf('.cabinet-secret,\n.cabinet-option {')
    expect(i).toBeGreaterThan(-1)
    const rule = outside.slice(i, outside.indexOf('}', i))
    expect(rule).toMatch(/display:\s*flex/)
    expect(rule).toMatch(/flex-wrap:\s*wrap/)
    const main = outside.slice(outside.indexOf('.cabinet-option-main {'), outside.indexOf('}', outside.indexOf('.cabinet-option-main {')))
    expect(main).toMatch(/min-height:\s*44px/)
  })

  it('широкая раскладка -- в том же единственном блоке', () => {
    expect(css.split(OPEN).length - 1).toBe(1)
    for (const sel of ['.wide-shell .segment-tabs {', '.wide-shell .segment-tab:hover', '.wide-shell .cabinet-option-main:hover', '.wide-shell .selfhosted-actions .btn {']) {
      expect(wide.body.includes(sel), sel).toBe(true)
      expect(outside.includes(sel), sel).toBe(false)
    }
  })

  it('цвета -- только токенами', () => {
    const start = outside.indexOf('Кабинеты VPN и свои серверы')
    expect(start).toBeGreaterThan(-1)
    const section = outside.slice(start, outside.indexOf('.park-selfhosted {', start))
    expect(section).not.toMatch(/#[0-9a-f]{3,8}\b/i)
  })
})
