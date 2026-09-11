import { describe, it, expect } from 'vitest'
import { Fragment } from 'preact'
import { keepTogether, quoteParts, plainHyphens, installKeepTogether } from '../src/text.js'

// Строка переносится по дефису: «через VPN-» / «туннель». Термин «VPN-туннель»
// -- одно слово для глаза, и склеивается он неразрывным дефисом U+2011.
// Имена в «ёлочках» так не склеиваются: их копируют и вставляют в роутер, и
// U+2011 там не примет никто. Имя держит вместе вёрстка (<Q>), а не символ.
const NBH = '‑'

describe('keepTogether', () => {
  it('склеивает «VPN-туннель» во всех формах', () => {
    expect(keepTogether('через VPN-туннель')).toBe(`через VPN${NBH}туннель`)
    expect(keepTogether('Напрямую, мимо VPN-туннеля')).toBe(`Напрямую, мимо VPN${NBH}туннеля`)
    expect(keepTogether('VPN-туннели и VPN-туннелей')).toBe(`VPN${NBH}туннели и VPN${NBH}туннелей`)
  })

  it('внутри «ёлочек» не меняет ничего -- это имя, а не термин', () => {
    expect(keepTogether('через VPN-туннель «vpn-nl»')).toBe(`через VPN${NBH}туннель «vpn-nl»`)
    expect(keepTogether('«VPN-туннель на даче» и VPN-туннель')).toBe(`«VPN-туннель на даче» и VPN${NBH}туннель`)
  })

  it('не трогает прочие дефисы', () => {
    expect(keepTogether('по-русски, из-за сбоя — «Дача»')).toBe('по-русски, из-за сбоя — «Дача»')
  })

  it('строку без термина и не-строки возвращает как есть', () => {
    expect(keepTogether('Проверить сейчас')).toBe('Проверить сейчас')
    expect(keepTogether(42)).toBe(42)
    expect(keepTogether(null)).toBe(null)
  })
})

// Готовая строка с именем внутри («Роутер «home-1» скачает…») собирается в
// labels/sheet/routeLookup. Экран режет её на текст и имена, чтобы имя легло
// в <Q> с настоящим дефисом.
describe('quoteParts', () => {
  it('режет строку на текст и имена', () => {
    expect(quoteParts('«claude.ai» пойдёт через VPN-туннель «vpn-nl»')).toEqual([
      { q: 'claude.ai' },
      ' пойдёт через VPN-туннель ',
      { q: 'vpn-nl' },
    ])
  })

  it('строка без имён -- одна часть', () => {
    expect(quoteParts('напрямую через провайдера')).toEqual(['напрямую через провайдера'])
  })

  it('незакрытая кавычка -- не имя', () => {
    expect(quoteParts('«vpn-nl и дальше')).toEqual(['«vpn-nl и дальше'])
  })

  it('пустое и не-строка -- без частей', () => {
    expect(quoteParts('')).toEqual([])
    expect(quoteParts(null)).toEqual([])
  })
})

// Скопированное из приложения возвращается во ввод: U+2011 и U+2010 там --
// обычный дефис, иначе имя сайта или роутера не совпадёт ни с чем.
describe('plainHyphens', () => {
  it('неразрывный и типографский дефис становятся обычным', () => {
    expect(plainHyphens(`vpn${NBH}nl и дача‐1`)).toBe('vpn-nl и дача-1')
  })

  it('не-строку приводит к строке', () => {
    expect(plainHyphens(null)).toBe('')
  })
})

// Склейка ставится один раз на рендер: хук Preact options.vnode видит каждый
// элемент и склеивает строки среди его детей. Дословное -- <pre>, .raw-dump и
// имя в <Q> (.q) -- он пропускает целиком.
describe('installKeepTogether', () => {
  const hooked = () => {
    const options = {}
    installKeepTogether(options)
    return options
  }

  it('склеивает термин в детях элемента, имя не трогает', () => {
    const options = hooked()
    const el = { type: 'p', props: { children: 'через VPN-туннель «vpn-nl»' } }
    options.vnode(el)
    expect(el.props.children).toBe(`через VPN${NBH}туннель «vpn-nl»`)
  })

  it('доходит до строк во вложенных массивах и во фрагменте', () => {
    const options = hooked()
    const inner = { type: 'b', props: { children: 'x' } }
    const el = { type: 'p', props: { children: ['мимо VPN-туннеля', ['VPN-туннели'], inner, 3] } }
    options.vnode(el)
    expect(el.props.children).toEqual([`мимо VPN${NBH}туннеля`, [`VPN${NBH}туннели`], inner, 3])
    const frag = { type: Fragment, props: { children: ['VPN-туннель'] } }
    options.vnode(frag)
    expect(frag.props.children).toEqual([`VPN${NBH}туннель`])
  })

  it('дословное не трогает: <pre>, .raw-dump, имя в .q', () => {
    const options = hooked()
    const pre = { type: 'pre', props: { children: 'VPN-туннель' } }
    const dump = { type: 'div', props: { class: 'card raw-dump', children: 'VPN-туннель' } }
    const name = { type: 'span', props: { class: 'q', children: ['«', 'VPN-туннель X', '»'] } }
    for (const v of [pre, dump, name]) options.vnode(v)
    expect(pre.props.children).toBe('VPN-туннель')
    expect(dump.props.children).toBe('VPN-туннель')
    expect(name.props.children).toEqual(['«', 'VPN-туннель X', '»'])
  })

  it('компоненты, текстовые узлы и атрибуты не трогает', () => {
    const options = hooked()
    const Comp = () => null
    const comp = { type: Comp, props: { children: 'VPN-туннель' } }
    const text = { type: null, props: 'VPN-туннель' }
    const input = { type: 'input', props: { value: 'VPN-туннель', placeholder: 'VPN-туннель' } }
    for (const v of [comp, text, input]) options.vnode(v)
    expect(comp.props.children).toBe('VPN-туннель')
    expect(text.props).toBe('VPN-туннель')
    expect(input.props).toEqual({ value: 'VPN-туннель', placeholder: 'VPN-туннель' })
  })

  it('прежний хук вызывается по-прежнему', () => {
    const seen = []
    const options = { vnode: (v) => seen.push(v.props.children) }
    installKeepTogether(options)
    options.vnode({ type: 'p', props: { children: 'VPN-туннель' } })
    expect(seen).toEqual([`VPN${NBH}туннель`])
  })
})
