import { describe, it, expect } from 'vitest'
import { keepTogether, installKeepTogether } from '../src/text.js'

// Строка переносится по дефису: «через VPN-» / «туннель «vpn-nl»», ««vpn-» /
// «nl»». Термин и имя в «ёлочках» -- по одному слову для глаза, и разрыв
// посередине читается как два разных слова. Неразрывный дефис U+2011
// выглядит как обычный, но строку по нему браузер не рвёт.
const NBH = '‑'

describe('keepTogether', () => {
  it('склеивает «VPN-туннель» во всех формах', () => {
    expect(keepTogether('через VPN-туннель')).toBe(`через VPN${NBH}туннель`)
    expect(keepTogether('Напрямую, мимо VPN-туннеля')).toBe(`Напрямую, мимо VPN${NBH}туннеля`)
    expect(keepTogether('VPN-туннели и VPN-туннелей')).toBe(`VPN${NBH}туннели и VPN${NBH}туннелей`)
  })

  it('склеивает имя в «ёлочках», сколько бы дефисов в нём ни было', () => {
    expect(keepTogether('через VPN-туннель «vpn-nl»')).toBe(`через VPN${NBH}туннель «vpn${NBH}nl»`)
    expect(keepTogether('«home-vpn-2» и «vpn-de»')).toBe(`«home${NBH}vpn${NBH}2» и «vpn${NBH}de»`)
  })

  it('не трогает дефисы вне термина и имён', () => {
    expect(keepTogether('по-русски, из-за сбоя — «Дача»')).toBe('по-русски, из-за сбоя — «Дача»')
  })

  it('незакрытая кавычка не склеивает остаток строки', () => {
    expect(keepTogether('«vpn-nl и дальше по-русски')).toBe('«vpn-nl и дальше по-русски')
  })

  it('строку без дефисов и не-строки возвращает как есть', () => {
    expect(keepTogether('Проверить сейчас')).toBe('Проверить сейчас')
    expect(keepTogether(42)).toBe(42)
    expect(keepTogether(null)).toBe(null)
  })
})

// Строк с «VPN-туннелем» в мини-аппе под двести, в тридцати файлах: чинить
// каждую -- значит пропустить следующую. Поэтому склейка ставится один раз
// на рендер: хук Preact options.vnode видит каждый текстовый узел.
describe('installKeepTogether', () => {
  it('склеивает текстовый узел', () => {
    const options = {}
    installKeepTogether(options)
    const text = { type: null, props: 'через VPN-туннель «vpn-nl»' }
    options.vnode(text)
    expect(text.props).toBe(`через VPN${NBH}туннель «vpn${NBH}nl»`)
  })

  it('элементы и числа не трогает', () => {
    const options = {}
    installKeepTogether(options)
    const props = { children: 'VPN-туннель', placeholder: 'vpn-nl' }
    const el = { type: 'p', props }
    options.vnode(el)
    expect(el.props).toBe(props)
    expect(el.props.children).toBe('VPN-туннель')
    const num = { type: null, props: 16 }
    options.vnode(num)
    expect(num.props).toBe(16)
  })

  it('прежний хук вызывается по-прежнему', () => {
    const seen = []
    const options = { vnode: (v) => seen.push(v.props) }
    installKeepTogether(options)
    options.vnode({ type: null, props: 'VPN-туннель' })
    expect(seen).toEqual([`VPN${NBH}туннель`])
  })
})
