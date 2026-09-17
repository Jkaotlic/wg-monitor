// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ conn: null, loadFail: false, puts: [], putReply: null }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchAgentConnection: (id) => (mocks.loadFail ? Promise.reject(new Error('net')) : Promise.resolve({ ...mocks.conn, _id: id })),
  saveAgentConnection: (id, body) => {
    mocks.puts.push({ id, body })
    return mocks.putReply instanceof Error ? Promise.reject(mocks.putReply) : Promise.resolve(null)
  },
}))

const { AgentConnectionScreen } = await import('../src/screens/AgentConnectionScreen.jsx')
const { ApiError } = await import('../src/api.js')

const CONN = {
  awgm_url: 'https://router.example.com', awgm_auth: 'web', ssh_host: '198.51.100.7', ssh_port: 222,
  ssh_user: 'root', deploy_mode: 'awgm', arch: 'arm64', ring: 'stable', expected_mac: 'aa:bb:cc:dd:ee:ff',
}

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)

async function fill(root, id, value) {
  const el = root.querySelector(`#${id}`)
  await act(async () => {
    el.value = value
    el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true }))
  })
}

async function mount() {
  const calls = { closed: 0 }
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(<AgentConnectionScreen routerID={7} routerName="dacha-1" onClose={() => calls.closed++} />, root)
  })
  await flush()
  return { root, calls }
}
const cleanup = (root) => { render(null, root); root.remove() }

async function save(root) {
  await act(async () => button(root, 'Сохранить').click())
  await flush()
}

beforeEach(() => {
  mocks.conn = { ...CONN }
  mocks.loadFail = false
  mocks.puts = []
  mocks.putReply = null
})

describe('«Подключение агента»', () => {
  it('группы спеки, поля заполнены с сервера, незнакомый режим выбран', async () => {
    const { root } = await mount()
    expect(root.querySelector('.overlay-title').textContent).toBe('Подключение агента «dacha-1»')
    expect([...root.querySelectorAll('.section-title')].map((h) => h.textContent)).toEqual(['Панель awg-manager', 'SSH', 'Раскатка', 'Проверка роутера'])
    expect(root.querySelector('#conn-awgm_url').value).toBe('https://router.example.com')
    expect(root.querySelector('#conn-ssh_port').value).toBe('222')
    expect(root.querySelector('#conn-awgm_auth').value).toBe('web')
    expect(root.querySelector('#conn-deploy_mode').value).toBe('awgm')
    expect(root.querySelector('#conn-ring').value).toBe('stable')
    expect(root.textContent).toContain('Пустое поле оставляет прежнее значение.')
    expect(root.querySelector('input[type="password"]')).toBe(null)
    cleanup(root)
  })

  it('ничего не меняли -- запроса нет', async () => {
    const { root } = await mount()
    await save(root)
    expect(mocks.puts).toEqual([])
    expect(root.querySelector('.connection-notice').textContent).toBe('Ничего не изменилось.')
    cleanup(root)
  })

  it('проверка до отправки', async () => {
    const { root } = await mount()
    await fill(root, 'conn-ssh_port', '70000')
    await save(root)
    expect(mocks.puts).toEqual([])
    expect(root.querySelector('.wizard-error').textContent).toBe('Порт SSH — число от 1 до 65535')
    cleanup(root)
  })

  it('сохранение: тело целиком, «Сохранено.», повтор без изменений не шлёт', async () => {
    const { root } = await mount()
    await fill(root, 'conn-awgm_url', ' https://panel.example.com ')
    await fill(root, 'conn-ring', 'rc')
    await save(root)
    expect(mocks.puts).toEqual([{ id: 7, body: { ...CONN, awgm_url: 'https://panel.example.com', ring: 'rc' } }])
    expect(root.querySelector('.connection-notice').textContent).toBe('Сохранено.')
    await save(root)
    expect(mocks.puts).toHaveLength(1)
    expect(root.querySelector('.connection-notice').textContent).toBe('Ничего не изменилось.')
    cleanup(root)
  })

  it('отказ сервера -- его фраза', async () => {
    mocks.putReply = new ApiError(400, 'invalid_arch', 'x', 'Архитектура не поддерживается')
    const { root } = await mount()
    await fill(root, 'conn-arch', 'mipsle')
    await save(root)
    expect(root.querySelector('.wizard-error').textContent).toBe('Архитектура не поддерживается')
    expect(root.querySelector('.connection-notice')).toBe(null)
    cleanup(root)
  })

  it('не прочиталось -- слова, а не пустая форма; «назад» закрывает', async () => {
    mocks.loadFail = true
    const { root, calls } = await mount()
    expect(root.textContent).toContain('Не удалось прочитать подключение агента.')
    expect(button(root, 'Сохранить')).toBeFalsy()
    await act(async () => root.querySelector('.overlay-back').click())
    expect(calls.closed).toBe(1)
    cleanup(root)
  })
})
