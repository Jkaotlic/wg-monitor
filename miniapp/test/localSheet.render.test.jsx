// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { Sheet } from '../src/ui/Sheet.jsx'
import { localSheet } from '../src/sheet.js'
import { ApiError } from '../src/api.js'

async function mount(sheet, onClose = () => {}) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(<Sheet sheet={sheet} asleep={false} onClose={onClose} />, root)
  })
  return root
}

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const primary = (root) => [...root.querySelectorAll('.sheet-actions button')].pop()

async function type(root, text) {
  const input = root.querySelector('#sheet-confirm-input')
  await act(async () => {
    input.value = text
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

describe('локальный лист с набором имени', () => {
  it('кнопка горит только после набора, набранное уходит в perform, ответ -- в onDone', async () => {
    const typedSeen = []
    const done = []
    let closed = 0
    const root = await mount(
      localSheet({
        title: 'Обновить агент на «bronya»?',
        body: 'b',
        buttonLabel: 'Обновить',
        confirmPhrase: 'bronya',
        perform: (typed) => {
          typedSeen.push(typed)
          return Promise.resolve({ queued: true, deferred: true, target_version: 'v0.33.0' })
        },
        onDone: (resp) => done.push(resp),
      }),
      () => closed++,
    )
    expect(root.querySelector('.sheet-command')).toBeNull()
    expect(primary(root).disabled).toBe(true)
    await type(root, 'Bronya')
    expect(primary(root).disabled).toBe(false)
    await act(async () => primary(root).click())
    await flush()
    expect(typedSeen).toEqual(['Bronya'])
    expect(done).toEqual([{ queued: true, deferred: true, target_version: 'v0.33.0' }])
    expect(closed).toBe(1)
    render(null, root)
    root.remove()
  })

  it('отказ по коду -- фраза экрана, лист остаётся открытым', async () => {
    let closed = 0
    const root = await mount(
      localSheet({
        title: 't',
        body: 'b',
        errorText: (err) => (err?.code === 'deploy_pending' ? 'Обновление этого роутера уже ждёт своей очереди.' : ''),
        perform: () => Promise.reject(new ApiError(409, 'deploy_pending', '/routers/7/agent/update failed: 409')),
      }),
      () => closed++,
    )
    await act(async () => primary(root).click())
    await flush()
    expect(root.textContent).toContain('Обновление этого роутера уже ждёт своей очереди.')
    expect(root.textContent).not.toContain('failed: 409')
    expect(closed).toBe(0)
    render(null, root)
    root.remove()
  })

  it('без errorText остаётся прежний текст', async () => {
    const root = await mount(localSheet({ title: 't', body: 'b', perform: () => Promise.reject(new Error('x')) }))
    await act(async () => primary(root).click())
    await flush()
    expect(root.textContent).toContain('Не получилось. Попробуйте ещё раз.')
    render(null, root)
    root.remove()
  })
})
