import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// Цикл 2 (часть 3): новые классы есть, телефонные -- вне блока ≥1024px,
// правила колонки -- внутри него. Задачи 2–6 дописывают сюда свои describe.
export const css = readFileSync(fileURLToPath(new URL('../src/style.css', import.meta.url)), 'utf8')
const OPEN = '@media (min-width: 1024px) {'

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

const block = mediaBlock(css)
const outside = css.slice(0, block.start) + css.slice(block.end)

// Тело правила по точному селектору ("\n.filter-chips {" или "\n  .side-filter {").
function rule(text, selector) {
  const at = text.indexOf(`${selector} {`)
  if (at < 0) return null
  return text.slice(at, text.indexOf('}', at) + 1)
}

describe('поиск и фильтры: CSS', () => {
  it('телефонные классы -- вне блока широкой раскладки', () => {
    for (const sel of ['.filter-bar', '.filter-search', '.filter-chips', '.filter-chip', '.filter-chip-active', '.filter-chip-count', '.filter-empty']) {
      expect(rule(outside, `\n${sel}`), sel).not.toBe(null)
    }
  })

  it('чипы переносятся, а не прокручиваются вбок', () => {
    const chips = rule(outside, '\n.filter-chips')
    expect(chips).toMatch(/flex-wrap:\s*wrap/)
    expect(chips).not.toMatch(/overflow-x/)
  })

  it('поиск в колонке -- внутри блока ≥1024px', () => {
    expect(rule(block.body, '\n  .side-filter')).not.toBe(null)
    expect(block.body).toMatch(/\.side-filter \.filter-chip\s*\{/)
  })
})
