import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const css = readFileSync(fileURLToPath(new URL('../src/style.css', import.meta.url)), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
const body = (sel) => {
  const m = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].find(([, s]) => s.trim() === sel)
  return m ? m[2] : ''
}

describe('v0.50 Парк и шапка: CSS', () => {
  it('переключатель режет длинный ник (Review Focus 3) и держит 44 px', () => {
    expect(body('.router-switch')).toMatch(/min-width:\s*0/)
    expect(body('.router-switch')).toMatch(/min-height:\s*44px/)
    expect(body('.router-switch-name')).toMatch(/text-overflow:\s*ellipsis/)
    expect(body('.router-switch-name')).toMatch(/white-space:\s*nowrap/)
    expect(body('.router-switch-name')).toMatch(/overflow:\s*hidden/)
  })

  it('шапка фиксированной высоты', () => {
    expect(css).toMatch(/--app-header-h:\s*64px/)
    expect(body('.app-header')).toMatch(/height:\s*var\(--app-header-h\)/)
  })

  it('оверлей: заголовок полосы прячется, когда в теле есть H1 и в полосе есть «назад» (корневой слой без «назад» остаётся с заголовком)', () => {
    expect(body('.overlay:has(.overlay-back):has(.overlay-body .screen-title) .overlay-title')).toMatch(/display:\s*none/)
    expect(body('.overlay:has(.overlay-body .screen-title) .overlay-title')).toBe('')
    // На широком экране H1 прячет своё правило .wide-shell -- здесь только телефон.
    expect(css).toMatch(/@media \(max-width: 1023\.98px\)\s*\{\s*\.overlay:has\(\.overlay-back\):has\(\.overlay-body \.screen-title\) \.overlay-title/)
  })

  it('карточки Парка -- две колонки от 1100 px', () => {
    expect(css).toMatch(/@media \(min-width: 1100px\)\s*\{\s*\.park-cards\s*\{\s*grid-template-columns:\s*repeat\(2,\s*minmax\(0,\s*1fr\)\)/)
  })

  it('поле поиска в листе -- в теме приложения, а не белое системное', () => {
    expect(body('.sheet-search')).toMatch(/background:\s*var\(--page\)/)
    expect(body('.sheet-search')).toMatch(/min-height:\s*44px/)
  })

  it('заголовок QR -- не капслоком', () => {
    expect(body('.awg3-qr-title')).toMatch(/text-transform:\s*none/)
  })
})
