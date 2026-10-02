// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import SNAP from './fixtures/split_reserve_dead.json'

// Два места, где сырой ответ агента ещё мог выйти на экран: выпуск конфига
// («Роутер не принял конфиг: …») и подробность пробы адреса в «Сравнить».
const mocks = vi.hoisted(() => ({ result: null, router: null, checks: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  issueVPNConfig: () => Promise.resolve({ cmd_id: 7, tunnel_name: 'vpn-x' }),
  fetchCommandResult: () => Promise.resolve(mocks.result),
  fetchRouter: () => Promise.resolve(mocks.router),
  fetchRouterChecks: () => Promise.resolve(mocks.checks),
}))
vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({ busy: false, result: mocks.result, error: null, errorCode: null, sleepNote: '', run: () => Promise.resolve(null) }),
}))

const { CabinetIssue } = await import('../src/screens/CabinetIssue.jsx')
const { ExitCompareSection } = await import('../src/screens/ExitCompare.jsx')
const { AppContext } = await import('../src/appContext.js')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const PHRASE = 'Роутер не принял конфиг — попробуйте ещё раз через минуту.'

async function issue() {
  const root = document.createElement('div')
  document.body.appendChild(root)
  const pending = { provider: 'hidemy', title: 'HideMy', option: { id: 'nl', label: 'Нидерланды' } }
  await act(async () => render(<CabinetIssue routerID={2} asleep={false} pending={pending} perms={{}} openSheet={() => {}} />, root))
  await act(async () => [...root.querySelectorAll('button')].find((b) => b.textContent.includes('Выпустить')).click())
  await flush()
  await flush()
  return root
}

beforeEach(() => { mocks.result = null })

describe('CabinetIssue: отказ роутера', () => {
  it('английский вывод и голый статус -- фраза экрана', async () => {
    mocks.result = { status: 'error', output: 'awgmgr POST /api/import: HTTP 400' }
    const root = await issue()
    expect(root.querySelector('.cabinet-outcome').textContent).toContain(PHRASE)
    expect(root.textContent).not.toMatch(/awgmgr|HTTP|timeout/)
    render(null, root)
  })
  it('пустой вывод -- фраза, без «timeout»', async () => {
    mocks.result = { status: 'timeout', output: '' }
    const root = await issue()
    expect(root.querySelector('.cabinet-outcome').textContent).toContain(PHRASE)
    expect(root.textContent).not.toContain('timeout')
    render(null, root)
  })
  it('русский вывод сохраняется', async () => {
    mocks.result = { status: 'error', output: 'Ключ уже занят.' }
    const root = await issue()
    expect(root.querySelector('.cabinet-outcome').textContent).toContain(`${PHRASE} Ключ уже занят.`)
    render(null, root)
  })
})

async function detail() {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<AppContext.Provider value={{ mode: 'miniapp', wide: false }}><ExitCompareSection routerID={56} traffic={null} asleep={false} /></AppContext.Provider>, root))
  await flush()
  return root
}

describe('«Сравнить адреса»: подробность пробы', () => {
  it('английский вывод агента на экран не выходит', async () => {
    mocks.result = { status: 'error', output: 'curl: (7) Failed to connect to ifconfig.me port 443' }
    const root = await detail()
    expect(root.textContent).not.toContain('Failed to connect')
    render(null, root)
  })
  it('русский вывод остаётся', async () => {
    mocks.result = { status: 'error', output: 'Адрес не удалось определить: нет ответа.' }
    const root = await detail()
    expect(root.querySelector('.compare-probe-detail').textContent).toContain('Адрес не удалось определить: нет ответа.')
    render(null, root)
  })
})
