// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { RouterStrip } from '../src/ui/RouterStrip.jsx'

// Полоса «Мои роутеры» после доводки v0.52: красный чип стоит ВНЕ прокрутки --
// отдельной ячейкой слева, остальные чипы ездят в своей прокрутке рядом. Под
// красный уехать нечему: наложение невозможно по построению, а не по расчёту.
const chip = (id, name, current = false, alert = false) => ({ id, name, state: alert ? 'тревога' : 'в порядке', tone: alert ? 'danger' : 'ok', current, alert })

// Геометрия, которой нет в jsdom: ширины по имени; чипы прокрутки идут друг за
// другом через 8 px от её левого поля (16 без красного, 4 рядом с ним).
const W = (n) => (n.includes('wide') ? 200 : n.includes('red') ? 150 : 90)
async function withLayout(run, { frame = 360 } = {}) {
  const proto = HTMLElement.prototype
  const keys = ['offsetLeft', 'offsetWidth', 'clientWidth']
  const saved = keys.map((k) => [k, Object.getOwnPropertyDescriptor(proto, k)])
  const inScroll = (el) => el.classList?.contains('strip-chip') && el.parentNode?.classList?.contains('strip-scroll')
  Object.defineProperty(proto, 'offsetLeft', {
    configurable: true,
    get() {
      if (!inScroll(this)) return 0
      let x = this.parentNode.previousElementSibling ? 4 : 16
      for (const c of this.parentNode.children) {
        if (c === this) break
        x += W(c.textContent) + 8
      }
      return x
    },
  })
  Object.defineProperty(proto, 'offsetWidth', { configurable: true, get() { return this.classList?.contains('strip-chip') ? W(this.textContent) : 0 } })
  Object.defineProperty(proto, 'clientWidth', { configurable: true, get() { return this.classList?.contains('strip-scroll') ? frame : 0 } })
  const root = document.createElement('div')
  document.body.appendChild(root)
  try {
    await run(root)
  } finally {
    render(null, root)
    root.remove()
    for (const [k, d] of saved) {
      if (d) Object.defineProperty(proto, k, d)
      else delete proto[k]
    }
  }
}
const show = (root, chips, onPick = () => {}) => act(async () => render(<RouterStrip chips={chips} onPick={onPick} />, root))
const scroller = (root) => root.querySelector('.router-strip .strip-scroll')
const names = (els) => [...els].map((c) => c.textContent.trim())

describe('полоса роутеров: красный чип вне прокрутки', () => {
  it('красный -- отдельной ячейкой слева, остальные -- в прокрутке; порядок в DOM прежний', async () => {
    await withLayout(async (root) => {
      await show(root, [chip(1, 'red', false, true), chip(2, 'a', true), chip(3, 'b')])
      const strip = root.querySelector('.router-strip')
      expect(strip.getAttribute('aria-label')).toBe('Мои роутеры')
      expect(names(strip.querySelectorAll('.strip-lead .strip-chip'))).toEqual(['red'])
      expect(names(scroller(root).querySelectorAll('.strip-chip'))).toEqual(['a', 'b'])
      expect(scroller(root).querySelector('.strip-chip-alert')).toBe(null)
      // Ячейка -- сосед прокрутки, а не её ребёнок: прокрутка её не двигает.
      expect(strip.querySelector('.strip-lead').parentNode).toBe(strip)
      expect(strip.querySelector('.strip-lead').nextElementSibling).toBe(scroller(root))
      expect(names(strip.querySelectorAll('.strip-chip'))).toEqual(['red', 'a', 'b'])
    })
  })

  it('красного нет -- одна прокрутка, ячейки нет', async () => {
    await withLayout(async (root) => {
      await show(root, [chip(1, 'a', true), chip(2, 'b')])
      expect(root.querySelector('.strip-lead')).toBe(null)
      expect(names(scroller(root).querySelectorAll('.strip-chip'))).toEqual(['a', 'b'])
    })
  })

  it('красных два: слева стоит первый, второй -- в прокрутке и окрашен', async () => {
    await withLayout(async (root) => {
      await show(root, [chip(1, 'red', false, true), chip(2, 'red2', false, true), chip(3, 'a', true)])
      expect(names(root.querySelectorAll('.strip-lead .strip-chip'))).toEqual(['red'])
      expect(names(scroller(root).querySelectorAll('.strip-chip-alert'))).toEqual(['red2'])
    })
  })

  it('нажатие: красный и чип прокрутки выбирают роутер, текущий -- нет', async () => {
    await withLayout(async (root) => {
      const picked = []
      await show(root, [chip(1, 'red', false, true), chip(2, 'a', true), chip(3, 'b')], (id) => picked.push(id))
      for (const c of root.querySelectorAll('.strip-chip')) await act(async () => c.click())
      expect(picked).toEqual([1, 3])
      expect(root.querySelector('[aria-current=page]').textContent.trim()).toBe('a')
      expect(root.querySelector('.strip-lead .strip-chip').getAttribute('aria-label')).toBe('red: тревога')
    })
  })
})

describe('полоса роутеров: текущий чип в кадре своей прокрутки', () => {
  it('порядок сменился при том же числе чипов и том же текущем -- полоса перематывается', async () => {
    await withLayout(async (root) => {
      await show(root, [chip(1, 'a'), chip(2, 'b'), chip(3, 'c'), chip(4, 'd', true)])
      expect(scroller(root).scrollLeft).toBeGreaterThan(0)
      await show(root, [chip(4, 'd', true), chip(1, 'a'), chip(2, 'b'), chip(3, 'c')])
      expect(scroller(root).scrollLeft).toBe(0)
    })
  })

  it('текущий правее кадра -- прокрутка ровно до его правого края с полем', async () => {
    await withLayout(async (root) => {
      // рядом с красным кадр прокрутки 190: a 4..94, b 102..192, cur 200..290 -> 290 + 16 - 190 = 116
      await show(root, [chip(1, 'red', false, true), chip(2, 'a'), chip(3, 'b'), chip(4, 'cur', true)])
      expect(scroller(root).scrollLeft).toBe(116)
    }, { frame: 190 })
  })

  it('текущий левее кадра -- прокрутка до его начала, не дальше', async () => {
    await withLayout(async (root) => {
      await show(root, [chip(1, 'red', false, true), chip(2, 'a'), chip(3, 'b'), chip(4, 'cur', true)])
      scroller(root).scrollLeft = 500
      // b: left 102, поле слева 4 -> 98
      await show(root, [chip(1, 'red', false, true), chip(2, 'a'), chip(3, 'b', true), chip(4, 'cur')])
      expect(scroller(root).scrollLeft).toBe(98)
    }, { frame: 190 })
  })

  it('имя шире кадра -- побеждает начало имени', async () => {
    await withLayout(async (root) => {
      // cur-wide: left 102, ширина 200 > кадра 190: hi = 98, lo = 102+200+16-190 = 128 -> 98
      await show(root, [chip(1, 'red', false, true), chip(2, 'a'), chip(3, 'cur-wide', true)])
      expect(scroller(root).scrollLeft).toBe(98)
    }, { frame: 190 })
  })

  it('текущий -- сам красный: прокрутку остальных не трогаем', async () => {
    await withLayout(async (root) => {
      await show(root, [chip(1, 'red', false, true), chip(2, 'a', true), chip(3, 'b'), chip(4, 'c')])
      scroller(root).scrollLeft = 40
      await show(root, [chip(1, 'red', true, true), chip(2, 'a'), chip(3, 'b'), chip(4, 'c')])
      expect(scroller(root).scrollLeft).toBe(40)
    }, { frame: 190 })
  })
})
