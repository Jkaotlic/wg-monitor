// @vitest-environment jsdom
import { describe, it, expect, beforeAll } from 'vitest'
import { render, options } from 'preact'
import { installKeepTogether } from '../src/text.js'
import { Q, Quoted } from '../src/ui/Q.jsx'

// Настоящий рендер Preact с хуком, как в main.jsx: проверяется то, что
// попадёт в DOM, а не поддельные vnode.
const NBH = '‑'

beforeAll(() => installKeepTogether(options))

function mount(vnode) {
  const root = document.createElement('div')
  render(vnode, root)
  return root
}

describe('склейка в DOM', () => {
  it('термин -- с U+2011, имя в готовой строке -- с обычным дефисом', () => {
    const root = mount(<p>{'«claude.ai» пойдёт через VPN-туннель «vpn-nl»'}</p>)
    expect(root.textContent).toBe(`«claude.ai» пойдёт через VPN${NBH}туннель «vpn-nl»`)
  })

  it('имя в <Q> -- в .q, с «ёлочками» и обычным дефисом', () => {
    const root = mount(<p>Пойдёт в <Q>vpn-nl</Q>, мимо VPN-туннеля</p>)
    expect(root.querySelector('.q').textContent).toBe('«vpn-nl»')
    expect(root.textContent).toBe(`Пойдёт в «vpn-nl», мимо VPN${NBH}туннеля`)
  })

  it('<Quoted> кладёт имена готовой строки в .q, термин склеивает', () => {
    const root = mount(<p><Quoted text="Роутер «home-1» скачает прошивку, VPN-туннели упадут" /></p>)
    expect([...root.querySelectorAll('.q')].map((e) => e.textContent)).toEqual(['«home-1»'])
    expect(root.textContent).toBe(`Роутер «home-1» скачает прошивку, VPN${NBH}туннели упадут`)
  })

  it('имя, похожее на термин, в <Q> не меняется', () => {
    const root = mount(<p><Q>VPN-туннель на даче</Q></p>)
    expect(root.textContent).toBe('«VPN-туннель на даче»')
  })

  it('дословный вывод агента (.raw-dump) не меняется', () => {
    const raw = '{"name":"VPN-туннель «a-b»","id":"awg-10"}'
    const root = mount(<pre class="raw-dump">{raw}</pre>)
    expect(root.textContent).toBe(raw)
  })
})
