import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const css = readFileSync(fileURLToPath(new URL('../src/style.css', import.meta.url)), 'utf8')
const OPEN = '@media (min-width: 1024px) {'

// Блок от «@media … {» до парной скобки.
function mediaBlock(text) {
  const start = text.indexOf(OPEN)
  if (start < 0) return null
  let depth = 0
  for (let i = start + OPEN.length - 1; i < text.length; i++) {
    if (text[i] === '{') depth++
    else if (text[i] === '}') {
      depth--
      if (depth === 0) return { start, end: i + 1, body: text.slice(start, i + 1) }
    }
  }
  return null
}

describe('широкая раскладка в style.css', () => {
  const block = mediaBlock(css)

  it('ровно один блок ≥1024px', () => {
    expect(css.split(OPEN).length - 1).toBe(1)
    expect(block).not.toBe(null)
  })

  it('размеры из спеки', () => {
    expect(block.body).toMatch(/grid-template-columns:\s*288px/)
    expect(block.body).toMatch(/max-width:\s*1180px/)
    expect(block.body).toMatch(/max-width:\s*820px/)
    expect(block.body).toMatch(/width:\s*520px/)
    expect(block.body).toMatch(/\.sheet-grip\s*\{\s*display:\s*none/)
    expect(block.body).toMatch(/calc\(var\(--fs-xl\) \+ 4px\)/)
  })

  it('наведение и фокус с клавиатуры', () => {
    expect(block.body).toMatch(/:hover/)
    expect(block.body).toMatch(/:focus-visible/)
  })

  it('правила широкой раскладки не протекают в телефонную', () => {
    const outside = css.slice(0, block.start) + css.slice(block.end)
    for (const sel of ['.wide-shell', '.side-', '.side {', '.main-', '.now-grid', '.fleet-home', '.fleet-count', '.fleet-card']) {
      expect(outside.includes(sel), sel).toBe(false)
    }
  })

  it('экран входа оформлен на любой ширине', () => {
    const outside = css.slice(0, block.start) + css.slice(block.end)
    for (const sel of ['.login {', '.login-card {', '.login-notice {', '.park-classic {']) {
      expect(outside.includes(sel), sel).toBe(true)
    }
  })
})
