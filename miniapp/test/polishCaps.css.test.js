import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const css = readFileSync(fileURLToPath(new URL('../src/style.css', import.meta.url)), 'utf8')

// Тело правила по точному селектору (первое вхождение в начале строки).
function rule(selector) {
  const m = css.match(new RegExp(`^${selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')} \\{([^}]*)\\}`, 'm'))
  expect(m, `правила ${selector} нет`).toBeTruthy()
  return m[1]
}

// A1.5 (v0.55): капс убран с подписей разделов и полей -- подписи читаются
// как слова. Оставлен у марки приложения и входа (это логотип, не подпись).
describe('капс в подписях', () => {
  it.each(['.section-title', '.field label', '.compare-probe-label', '.stat-label'])('%s без капса', (sel) => {
    expect(rule(sel)).not.toContain('uppercase')
  })
  it.each(['.app-header-brand', '.login-brand'])('%s -- марка, капс остаётся', (sel) => {
    expect(rule(sel)).toContain('uppercase')
  })
})
