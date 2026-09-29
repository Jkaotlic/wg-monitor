import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const css = readFileSync(fileURLToPath(new URL('../src/style.css', import.meta.url)), 'utf8')

describe('CSS awg3-панелей', () => {
  it('классы экранов есть на любой ширине', () => {
    for (const sel of [
      '.awg3-add {',
      '.list-row-warn .list-row-sub {',
      '.awg3-file input {',
      '.awg3-check-ok {',
      '.awg3-check-bad {',
      '.data-row-dot-muted {',
      '.awg3-peer-tag {',
      '.awg3-peer-tag .pill {',
      '.awg3-peer-tag .pill-text {',
      '.awg3-file-btn {',
      '.awg3-actions {',
      '.awg3-qr {',
      '.awg3-banner {',
      '.awg3-outcome-ok {',
      '.awg3-outcome-error {',
      '.awg3-settings {',
    ]) {
      expect(css.includes(sel), sel).toBe(true)
    }
  })
})
