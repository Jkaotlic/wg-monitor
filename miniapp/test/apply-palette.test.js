import { describe, it, expect } from 'vitest'
import { PALETTE, applyPalette } from '../src/theme.js'

function fakeRoot() {
  const vars = new Map()
  const attrs = new Map()
  return {
    style: { setProperty: (k, v) => vars.set(k, v) },
    setAttribute: (k, v) => attrs.set(k, v),
    vars,
    attrs,
  }
}

describe('applyPalette', () => {
  it('пишет все переменные палитры', () => {
    const root = fakeRoot()
    applyPalette(root)
    expect(root.vars.get('--bg')).toBe(PALETTE.bg)
    expect(root.vars.get('--led-off')).toBe(PALETTE.ledOff)
    expect(root.vars.size).toBe(Object.keys(PALETTE).length)
  })

  // data-theme стоит в разметке index.html; applyPalette его подтверждает,
  // чтобы дефолты CSS и применённая палитра не могли разойтись.
  it('ставит data-theme', () => {
    const root = fakeRoot()
    applyPalette(root)
    expect(root.attrs.get('data-theme')).toBe('dark')
  })

  // Схема больше не параметр: тема одна. Лишний аргумент не должен ни на что
  // влиять -- иначе останется след прежнего переключателя, который однажды
  // кто-нибудь снова начнёт передавать.
  it('игнорирует любой переданный аргумент схемы', () => {
    const root = fakeRoot()
    applyPalette(root, 'light')
    expect(root.vars.get('--bg')).toBe(PALETTE.bg)
    expect(root.attrs.get('data-theme')).toBe('dark')
  })

  it('возвращает применённую палитру -- ею красится хром Telegram', () => {
    expect(applyPalette(fakeRoot())).toBe(PALETTE)
  })
})
