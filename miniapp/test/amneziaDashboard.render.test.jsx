// @vitest-environment jsdom
import { render } from 'preact'
import { afterEach, expect, it, vi } from 'vitest'
import { AmneziaDashboard } from '../src/screens/AmneziaDashboard.jsx'
import { openExternal } from '../src/telegram.js'

vi.mock('../src/telegram.js', () => ({ openExternal: vi.fn() }))
const root = document.createElement('div')
afterEach(() => render(null, root))

it('открывает официальный кабинет в отдельном окне, сохраняя экран роутера', () => {
  render(<AmneziaDashboard />, root)
  const button = root.querySelector('button')
  expect(button.textContent).toMatch(/открыть личный кабинет/i)
  button.click()
  expect(openExternal).toHaveBeenCalledWith('https://cp.amnezia.org/ru')
  expect(root.querySelector('iframe, input')).toBeNull()
})
