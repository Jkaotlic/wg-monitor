// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ sent: [], sendReply: null, result: null }))

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  return {
    ...real,
    sendCommand: (routerID, action, args, confirm) => {
      mocks.sent.push({ routerID, action, args, confirm })
      return mocks.sendReply instanceof Error ? Promise.reject(mocks.sendReply) : Promise.resolve(mocks.sendReply)
    },
    fetchCommandResult: () => (mocks.result ? Promise.resolve(mocks.result) : new Promise(() => {})),
  }
})

const { Sheet } = await import('../src/ui/Sheet.jsx')
const { ApiError } = await import('../src/api.js')
const { confirmSheet } = await import('../src/sheet.js')

async function mount(sheet) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(<Sheet sheet={sheet} asleep={false} onClose={() => {}} />, root)
  })
  return root
}

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const primary = (root) => [...root.querySelectorAll('.sheet-actions button')].pop()

describe('лист команды', () => {
  it('набранное имя уходит на сервер, а команда названа словами', async () => {
    mocks.sent = []
    mocks.sendReply = { cmd_id: 'c1' }
    mocks.result = null
    const root = await mount(confirmSheet({ routerID: 2, title: 't', body: 'b', action: 'service_restart', args: { name: 'router' }, confirmPhrase: 'home', commandLabel: 'перезагрузка роутера' }))
    expect(root.querySelector('.sheet-command-value').textContent).toBe('перезагрузка роутера')
    const input = root.querySelector('#sheet-confirm-input')
    await act(async () => {
      input.value = 'HOME'
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => primary(root).click())
    await flush()
    expect(mocks.sent[0]).toEqual({ routerID: 2, action: 'service_restart', args: { name: 'router' }, confirm: 'HOME' })
    render(null, root)
    root.remove()
  })

  it('роутер спит -- во время ожидания лист называет окно', async () => {
    mocks.sent = []
    mocks.sendReply = { cmd_id: 'c2', router_asleep: true, wake_window_min: 10 }
    mocks.result = null
    const root = await mount(confirmSheet({ routerID: 2, title: 't', body: 'b', action: 'hrneo_update' }))
    await act(async () => primary(root).click())
    await flush()
    expect(root.textContent).toContain('Роутер спит — команда выполнится, если он проснётся в течение 10 минут.')
    render(null, root)
    root.remove()
  })

  it('роутер не на связи -- окно называет ту же паузу другими словами', async () => {
    mocks.sent = []
    mocks.sendReply = { cmd_id: 'c2b', router_asleep: true, router_status: 'offline', wake_window_min: 10 }
    mocks.result = null
    const root = await mount(confirmSheet({ routerID: 2, title: 't', body: 'b', action: 'hrneo_update' }))
    await act(async () => primary(root).click())
    await flush()
    expect(root.textContent).toContain('Роутер не на связи — команда выполнится, если он появится в течение 10 минут.')
    render(null, root)
    root.remove()
  })

  it('итог обновления awg-manager пересказан словами', async () => {
    mocks.sendReply = { cmd_id: 'c3' }
    mocks.result = { id: 'c3', status: 'ok', output: '{"updated":true,"from":"2.19.0+r2","to":"2.19.1","kmod_installed":"3.2","kmod_loaded":"3.1","reboot_needed":true}' }
    const got = []
    const root = await mount(confirmSheet({ routerID: 2, title: 't', body: 'b', action: 'awgm_update', onResult: (r) => got.push(r) }))
    await act(async () => primary(root).click())
    await flush()
    await flush()
    expect(root.textContent).toContain('awg-manager обновлён: 2.19.0+r2 → 2.19.1. Сменился модуль ядра — нужна перезагрузка роутера.')
    expect(got).toHaveLength(1)
    render(null, root)
    root.remove()
  })

  it('отказ сервера по коду -- словами, а не путём запроса', async () => {
    mocks.sendReply = new ApiError(429, 'reboot_cooldown', '/routers/2/commands failed: 429')
    mocks.result = null
    const root = await mount(confirmSheet({ routerID: 2, title: 't', body: 'b', action: 'service_restart', args: { name: 'hrneo' } }))
    await act(async () => primary(root).click())
    await flush()
    expect(root.textContent).toContain('Роутер уже перезагружается — повторить можно через пять минут.')
    expect(root.textContent).not.toContain('failed: 429')
    render(null, root)
    root.remove()
  })
})
