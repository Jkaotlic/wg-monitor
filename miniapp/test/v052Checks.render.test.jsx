// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

vi.mock('../src/screens/DiagTab.jsx', () => ({ DiagTab: () => <div class="stub-now">сейчас</div> }))
vi.mock('../src/screens/EventsTab.jsx', () => ({ EventsTab: () => <div class="stub-history">что было</div> }))
const { ChecksTab } = await import('../src/screens/ChecksTab.jsx')

async function mount(view, onView) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<ChecksTab routerID={2} routerName="home" view={view} onView={onView} openSheet={() => {}} />, root))
  return root
}

describe('v0.52: сегмент «Сейчас | Что было»', () => {
  it('сверху сегмент, вид «Сейчас» по умолчанию', async () => {
    const root = await mount(undefined, () => {})
    const tabs = [...root.querySelectorAll('.segment-tab')]
    expect(tabs.map((t) => t.textContent)).toEqual(['Сейчас', 'Что было'])
    expect(tabs[0].getAttribute('aria-selected')).toBe('true')
    expect(root.querySelector('.stub-now')).toBeTruthy()
    render(null, root)
    root.remove()
  })
  it('«Что было» показывает ленту, нажатие зовёт onView', async () => {
    const onView = vi.fn()
    const root = await mount('history', onView)
    expect(root.querySelector('.stub-history')).toBeTruthy()
    await act(async () => root.querySelectorAll('.segment-tab')[0].click())
    expect(onView).toHaveBeenCalledWith('now')
    render(null, root)
    root.remove()
  })
})
