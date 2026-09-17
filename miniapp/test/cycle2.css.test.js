import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const css = readFileSync(fileURLToPath(new URL('../src/style.css', import.meta.url)), 'utf8')

// Все блоки «open … {» до парной скобки.
function blocks(text, open) {
  const out = []
  let from = 0
  for (;;) {
    const start = text.indexOf(open, from)
    if (start < 0) return out
    let depth = 0
    for (let i = start + open.length - 1; i < text.length; i++) {
      if (text[i] === '{') depth++
      else if (text[i] === '}') {
        depth--
        if (depth === 0) {
          out.push({ start, end: i + 1, body: text.slice(start, i + 1) })
          from = i + 1
          break
        }
      }
    }
  }
}

function ruleBody(text, selector) {
  const i = text.indexOf(selector)
  if (i < 0) return ''
  return text.slice(i, text.indexOf('}', i))
}

const wide = blocks(css, '@media (min-width: 1024px) {')[0]
const outside = css.slice(0, wide.start) + css.slice(wide.end)

describe('CSS цикла 2', () => {
  it('новые экраны оформлены на любой ширине', () => {
    for (const sel of [
      '.field-hint {', '.wizard-progress {', '.choice {', '.choice-on {', '.choice-mark {', '.wizard-note', '.wizard-warn',
      '.wizard-summary {', '.wizard-error {', '.wizard-actions {', '.token-block {', '.copy {', '.copy-fail {',
      '.job-status {', '.job-steps {', '.job-step {', '.job-step-active .job-step-mark {', '.job-step-failed .job-step-mark {',
      '.job-hint {', '.job-tail {', '.job-actions {', '.deploy-wait {', '.deploy-wait-card {', '.deploy-wait-title {',
      '.park-backend-actions {', '.park-add {', '.connection-notice {',
    ]) {
      expect(outside.includes(sel), sel).toBe(true)
    }
  })

  it('ожидание раскатки -- на весь экран, поверх листа (z-index 30) и колонки', () => {
    const body = ruleBody(outside, '.deploy-wait {')
    expect(body).toMatch(/position:\s*fixed/)
    expect(body).toMatch(/inset:\s*0/)
    const z = Number(body.match(/z-index:\s*(\d+)/)?.[1])
    expect(z).toBeGreaterThan(30)
    expect(body).toMatch(/background:\s*var\(--bg\)/)
  })

  it('пульс текущего шага -- только без просьбы убрать движение', () => {
    const calm = blocks(css, '@media (prefers-reduced-motion: no-preference) {')
    const inCalm = calm.reduce((n, b) => n + (b.body.match(/animation:\s*job-pulse/g) ?? []).length, 0)
    const total = (css.match(/animation:\s*job-pulse/g) ?? []).length
    expect(total).toBeGreaterThan(0)
    expect(inCalm).toBe(total)
    expect(css).toMatch(/@keyframes job-pulse/)
  })

  it('широкая раскладка дополняет -- в том же единственном блоке', () => {
    expect(css.split('@media (min-width: 1024px) {').length - 1).toBe(1)
    expect(wide.body).toMatch(/\.wide-shell \.choice-list\s*\{\s*grid-template-columns:\s*repeat\(2/)
    expect(wide.body).toMatch(/\.wide-shell \.choice:hover/)
    expect(wide.body).toMatch(/\.wide-shell \.connection \.form-group\s*\{/)
    expect(wide.body).toMatch(/\.wide-shell \.token-block\s*\{/)
    expect(wide.body).toMatch(/\.wide-shell \.wizard-actions\s*\{/)
    for (const sel of ['.wide-shell .choice', '.wide-shell .connection', '.wide-shell .token-block', '.wide-shell .job-actions']) {
      expect(outside.includes(sel), sel).toBe(false)
    }
  })

  it('мостика в классическое веб-управление больше нет', () => {
    expect(css.includes('.park-classic')).toBe(false)
  })
})
