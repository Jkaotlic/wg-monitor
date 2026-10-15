// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ result: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  sendCommand: () => Promise.resolve({ cmd_id: 'c1' }),
  fetchCommandResult: () => Promise.resolve(mocks.result),
}))

const { Sheet } = await import('../src/ui/Sheet.jsx')
const { confirmSheet } = await import('../src/sheet.js')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function runSheet(action, result) {
  mocks.result = result
  const onDone = vi.fn()
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<Sheet sheet={confirmSheet({ routerID: 2, title: 't', body: 'b', action, args: {}, onDone })} asleep={false} onClose={() => {}} />, root))
  await act(async () => [...root.querySelectorAll('.sheet-actions button')].pop().click())
  await flush()
  await flush()
  const text = root.textContent
  render(null, root)
  root.remove()
  return { onDone, text }
}

describe('лист: partial у tunnel_power', () => {
  it('onDone зовётся, текст -- «Не подтвердилось», не «Готово»', async () => {
    const { onDone, text } = await runSheet('tunnel_power', { status: 'partial', output: 'туннель ещё поднимается' })
    expect(onDone).toHaveBeenCalledTimes(1)
    expect(text).toContain('Не подтвердилось: туннель ещё поднимается')
    expect(text).not.toContain('Готово')
  })
  it('у другой команды partial onDone не зовёт', async () => {
    const { onDone } = await runSheet('dns_reset', { status: 'partial', output: '' })
    expect(onDone).not.toHaveBeenCalled()
  })
})
