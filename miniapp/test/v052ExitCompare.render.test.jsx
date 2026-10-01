// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Два зонда читают свой вывод из useCommand; различаем по порядку вызова
// (via, direct) -- как в ExitCompareSection.
const OUT = vi.hoisted(() => ({ via: '', direct: '', n: 0 }))
vi.mock('../src/useCommand.js', () => ({
  useCommand: () => {
    const which = OUT.n++ % 2 === 0 ? 'via' : 'direct'
    return { busy: false, result: OUT[which] ? { status: 'ok', output: OUT[which] } : null, error: null, errorCode: null, run: () => Promise.resolve(null) }
  },
}))
const { ExitCompareSection } = await import('../src/screens/ExitCompare.jsx')

async function mount(via, direct) {
  OUT.via = via
  OUT.direct = direct
  OUT.n = 0
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<ExitCompareSection routerID={1} traffic={null} asleep={false} />, root))
  return root
}

describe('сравнение адресов выхода: вывод', () => {
  it('одинаковый адрес -- «Адреса совпадают»', async () => {
    const root = await mount('Exit IP: 203.0.113.7', 'Exit IP: 203.0.113.7')
    expect(root.textContent).toContain('Адреса совпадают — трафик идёт мимо VPN-туннеля.')
    expect(root.textContent).not.toContain('Адреса разные')
    render(null, root)
    root.remove()
  })
  it('разные адреса -- «Адреса разные»', async () => {
    const root = await mount('Exit IP: 203.0.113.19', 'Exit IP: 198.51.100.4')
    expect(root.textContent).toContain('Адреса разные — трафик действительно идёт через VPN-туннель.')
    expect(root.textContent).not.toContain('Адреса совпадают')
    render(null, root)
    root.remove()
  })
})
