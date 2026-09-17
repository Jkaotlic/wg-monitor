// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { useOnClose } from '../src/useOnClose.js'

function Probe({ open, onClosed }) {
  useOnClose(open, onClosed)
  return null
}

describe('useOnClose', () => {
  it('только переход открыт → закрыт, и каждый раз свежая функция', async () => {
    const seen = []
    const root = document.createElement('div')
    const draw = (open, tag) => act(async () => render(<Probe open={open} onClosed={() => seen.push(tag)} />, root))
    await draw(false, 'a')
    expect(seen).toEqual([])
    await draw(false, 'b')
    expect(seen).toEqual([])
    await draw(true, 'c')
    expect(seen).toEqual([])
    await draw(true, 'd')
    expect(seen).toEqual([])
    await draw(false, 'e')
    expect(seen).toEqual(['e'])
    await draw(false, 'f')
    expect(seen).toEqual(['e'])
    render(null, root)
  })
})
