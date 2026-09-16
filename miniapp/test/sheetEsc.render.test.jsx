// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  sendCommand: () => Promise.resolve({ cmd_id: 'c1' }),
  // Ответа нет никогда -- лист остаётся в фазе выполнения.
  fetchCommandResult: () => new Promise(() => {}),
}))

const { Sheet } = await import('../src/ui/Sheet.jsx')
const { confirmSheet, localSheet } = await import('../src/sheet.js')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const esc = () => act(async () => window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })))

async function mount(sheet, onClose) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<Sheet sheet={sheet} asleep={false} onClose={onClose} />, root))
  return root
}

describe('Esc на листе', () => {
  it('до запуска закрывает лист', async () => {
    const onClose = vi.fn()
    const root = await mount(localSheet({ title: 'Точно?', body: '', perform: () => Promise.resolve() }), onClose)
    await esc()
    expect(onClose).toHaveBeenCalledTimes(1)
    render(null, root)
    root.remove()
  })

  it('во время выполнения не обрывает наблюдение', async () => {
    const onClose = vi.fn()
    const root = await mount(confirmSheet({ routerID: 2, title: 'Перезапустить?', body: '', action: 'force_recheck' }), onClose)
    await act(async () => [...root.querySelectorAll('.sheet-actions button')].pop().click())
    await flush()
    expect(root.textContent).toContain('выполняем на роутере…')
    await esc()
    expect(onClose).not.toHaveBeenCalled()
    render(null, root)
    root.remove()
  })

  it('после ухода листа Esc его не трогает', async () => {
    const onClose = vi.fn()
    const root = await mount(localSheet({ title: 'Точно?', body: '', perform: () => Promise.resolve() }), onClose)
    render(null, root)
    root.remove()
    await esc()
    expect(onClose).not.toHaveBeenCalled()
  })
})
