// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ logins: [], redeems: [], loginReply: null, redeemReply: null, order: [] }))

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  const reply = (v) => (v instanceof Error ? Promise.reject(v) : Promise.resolve(v))
  return {
    ...real,
    dashboardLogin: (t) => {
      mocks.logins.push(t)
      return reply(mocks.loginReply)
    },
    redeemWebLink: (t) => {
      mocks.order.push(['redeem', window.location.hash])
      mocks.redeems.push(t)
      return reply(mocks.redeemReply)
    },
  }
})

const { ApiError } = await import('../src/api.js')
const { LoginScreen } = await import('../src/screens/LoginScreen.jsx')
const { ServerDown } = await import('../src/ui/ServerDown.jsx')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function mount(vnode) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(vnode, root))
  await flush()
  return root
}

function cleanup(root) {
  render(null, root)
  root.remove()
}

async function typeToken(root, value) {
  const input = root.querySelector('#login-token')
  await act(async () => {
    input.value = value
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function submit(root) {
  await act(async () => root.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
  await flush()
}

beforeEach(() => {
  mocks.logins = []
  mocks.redeems = []
  mocks.order = []
  mocks.loginReply = { ok: true }
  mocks.redeemReply = { ok: true }
  window.history.replaceState(null, '', '/dashboard/login')
})

describe('экран входа', () => {
  it('форма: токен уходит, успех зовёт onSuccess', async () => {
    const onSuccess = vi.fn()
    const root = await mount(<LoginScreen onSuccess={onSuccess} />)
    expect(root.textContent).toContain('Токен доступа')
    await typeToken(root, '  secret  ')
    await submit(root)
    expect(mocks.logins).toEqual(['secret'])
    expect(onSuccess).toHaveBeenCalledTimes(1)
    cleanup(root)
  })

  it('неверный токен: «Токен не подошёл», поле не очищается', async () => {
    mocks.loginReply = new ApiError(401, 'unauthorized', 'x', 'unauthorized')
    const onSuccess = vi.fn()
    const root = await mount(<LoginScreen onSuccess={onSuccess} />)
    await typeToken(root, 'wrong')
    await submit(root)
    expect(root.querySelector('.login-error').textContent).toBe('Токен не подошёл')
    expect(root.querySelector('#login-token').value).toBe('wrong')
    expect(root.textContent).not.toContain('unauthorized')
    expect(onSuccess).not.toHaveBeenCalled()
    cleanup(root)
  })

  it('429: фраза сервера как есть', async () => {
    mocks.loginReply = new ApiError(429, 'rate_limited', 'x', 'Слишком много попыток. Попробуйте через 40 с.')
    const root = await mount(<LoginScreen onSuccess={() => {}} />)
    await typeToken(root, 'wrong')
    await submit(root)
    expect(root.querySelector('.login-error').textContent).toBe('Слишком много попыток. Попробуйте через 40 с.')
    cleanup(root)
  })

  it('пустой токен не отправляется', async () => {
    const root = await mount(<LoginScreen onSuccess={() => {}} />)
    await typeToken(root, '   ')
    await submit(root)
    expect(mocks.logins).toEqual([])
    cleanup(root)
  })

  it('ссылка: токен приходит свойством, обмен сразу, успех зовёт onSuccess без формы', async () => {
    const onSuccess = vi.fn()
    const onLinkUsed = vi.fn()
    const root = await mount(<LoginScreen linkToken="raw-1" onLinkUsed={onLinkUsed} onSuccess={onSuccess} />)
    expect(mocks.redeems).toEqual(['raw-1'])
    expect(onLinkUsed).toHaveBeenCalledTimes(1)
    expect(onSuccess).toHaveBeenCalledTimes(1)
    cleanup(root)
  })

  it('без токена обмена нет, даже если в адресе остался хэш', async () => {
    window.history.replaceState(null, '', '/dashboard/login#token=stale')
    const root = await mount(<LoginScreen onSuccess={() => {}} />)
    expect(mocks.redeems).toEqual([])
    expect(root.querySelector('#login-token')).toBeTruthy()
    cleanup(root)
  })

  it('мёртвая ссылка: фраза сервера и форма для токена', async () => {
    mocks.redeemReply = new ApiError(401, 'unauthorized', 'x', 'Ссылка больше не действует — попросите новую.')
    const root = await mount(<LoginScreen linkToken="old" onSuccess={() => {}} />)
    expect(root.querySelector('.login-error').textContent).toBe('Ссылка больше не действует — попросите новую.')
    expect(root.querySelector('#login-token')).toBeTruthy()
    cleanup(root)
  })

  it('заметка оболочки видна над формой', async () => {
    const root = await mount(<LoginScreen notice="Сессия закончилась — войдите снова" onSuccess={() => {}} />)
    expect(root.querySelector('.login-notice').textContent).toBe('Сессия закончилась — войдите снова')
    cleanup(root)
  })
})

describe('сервер не отвечает', () => {
  it('текст и «Повторить»', async () => {
    const onRetry = vi.fn()
    const root = await mount(<ServerDown onRetry={onRetry} />)
    expect(root.textContent).toContain('Сервер не отвечает')
    expect(root.textContent).not.toContain('Telegram')
    await act(async () => [...root.querySelectorAll('button')].find((b) => b.textContent === 'Повторить').click())
    expect(onRetry).toHaveBeenCalledTimes(1)
    cleanup(root)
  })
})
