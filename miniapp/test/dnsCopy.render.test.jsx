// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ settings: null, copied: [], copyReply: true }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve(mocks.settings),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [] }),
}))
vi.mock('../src/clipboard.js', () => ({
  copyText: (text) => {
    mocks.copied.push(text)
    return Promise.resolve(mocks.copyReply)
  },
}))

const { DNSResetScreen } = await import('../src/screens/DNSResetScreen.jsx')
const { dnsReferenceCommands } = await import('../src/dnsReset.js')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
async function mount() {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<DNSResetScreen routerID={2} routerName="home" openSheet={() => {}} onClose={() => {}} />, root))
  await flush()
  return root
}
const cleanup = (root) => { render(null, root); root.remove() }
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent === text)

beforeEach(() => {
  mocks.copied = []
  mocks.copyReply = true
})

describe('«Скопировать команды» на экране сброса DNS', () => {
  it('админ видит эталонные команды и копирует их целиком', async () => {
    mocks.settings = { role: 'admin', agent_version: 'v0.31.0' }
    const root = await mount()
    expect(root.querySelector('pre.dns-commands').textContent).toBe(dnsReferenceCommands())
    await act(async () => button(root, 'Скопировать команды').click())
    await flush()
    expect(mocks.copied).toEqual([dnsReferenceCommands()])
    expect(root.textContent).toContain('Команды скопированы.')
    cleanup(root)
  })

  it('старому агенту кнопки сброса нет, а ручные команды -- есть', async () => {
    mocks.settings = { role: 'admin', agent_version: 'v0.30.1' }
    const root = await mount()
    expect(button(root, 'Сбросить DNS')).toBeUndefined()
    expect(button(root, 'Скопировать команды')).toBeTruthy()
    cleanup(root)
  })

  it('не удалось скопировать -- сказано, что делать', async () => {
    mocks.settings = { role: 'admin', agent_version: 'v0.31.0' }
    mocks.copyReply = false
    const root = await mount()
    await act(async () => button(root, 'Скопировать команды').click())
    await flush()
    expect(root.textContent).toContain('Не удалось скопировать — выделите текст вручную.')
    cleanup(root)
  })

  it('не админу блока нет', async () => {
    mocks.settings = { role: 'owner', agent_version: 'v0.31.0' }
    const root = await mount()
    expect(button(root, 'Скопировать команды')).toBeUndefined()
    cleanup(root)
  })
})
