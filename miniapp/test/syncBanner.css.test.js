import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// review v0.46, п. 7: метка «нет связи с сервером» стоит поверх экрана, а не
// в потоке над .wide-shell (min-height: 100dvh) -- иначе страница получала
// прокрутку на высоту метки.
const css = readFileSync(fileURLToPath(new URL('../src/style.css', import.meta.url)), 'utf8')

describe('метка сбоя опроса', () => {
  it('вне потока: position fixed', () => {
    const m = css.match(/\.sync-lost\s*\{([^}]*)\}/)
    expect(m).toBeTruthy()
    expect(m[1]).toMatch(/position:\s*fixed/)
  })
})
