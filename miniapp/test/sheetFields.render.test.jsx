// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { Sheet } from '../src/ui/Sheet.jsx'
import { localSheet, initialFieldValues, fieldsReady } from '../src/sheet.js'
import { ApiError } from '../src/api.js'

const SECRET = 'root-Пароль-9f3kq'

const FIELDS = [
  { name: 'root_password', label: 'Пароль root', type: 'password' },
  { name: 'awgm_login', label: 'Логин панели роутера', type: 'text' },
  { name: 'expires_days', label: 'Ждать роутер', type: 'select', options: [{ value: '7', label: '7 дней' }, { value: '30', label: '30 дней' }], initial: '30' },
]

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
const cancel = (root) => root.querySelector('.sheet-actions .btn-ghost')
const cleanup = (root) => { render(null, root); root.remove() }

async function fill(root, id, value) {
  const el = root.querySelector(`#${id}`)
  await act(async () => {
    el.value = value
    el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true }))
  })
}

function reviveLike(overrides = {}) {
  return localSheet({
    title: 'Оживить агент на «bronya»?',
    body: 'b',
    buttonLabel: 'Оживить',
    confirmPhrase: 'bronya',
    fields: FIELDS,
    fieldsReady: (v) => (v.root_password ?? '') !== '',
    note: 'Пароль хранится на сервере зашифрованным до оживления, потом стирается.',
    perform: () => Promise.resolve({}),
    ...overrides,
  })
}

describe('поля листа: чистые функции', () => {
  it('начальные значения -- initial или пустая строка', () => {
    expect(initialFieldValues(FIELDS)).toEqual({ root_password: '', awgm_login: '', expires_days: '30' })
    expect(initialFieldValues(undefined)).toEqual({})
  })

  it('готовность -- по функции листа, без неё готово', () => {
    expect(fieldsReady(localSheet({ title: 't', body: 'b' }), {})).toBe(true)
    expect(fieldsReady(reviveLike(), { root_password: '' })).toBe(false)
    expect(fieldsReady(reviveLike(), { root_password: 'x' })).toBe(true)
  })
})

describe('поля листа: форма', () => {
  it('пароль -- type=password без автозаполнения, срок -- список с 30 по умолчанию, предупреждение видно', async () => {
    const root = await mount(reviveLike())
    const pass = root.querySelector('#sheet-field-root_password')
    expect(pass.type).toBe('password')
    expect(pass.getAttribute('autocomplete')).toBe('new-password')
    expect(root.querySelector('#sheet-field-awgm_login').type).toBe('text')
    expect(root.querySelector('#sheet-field-expires_days').tagName).toBe('SELECT')
    expect(root.querySelector('#sheet-field-expires_days').value).toBe('30')
    expect(root.querySelector('label[for="sheet-field-root_password"]').textContent).toBe('Пароль root')
    expect(root.querySelector('.sheet-note').textContent).toBe('Пароль хранится на сервере зашифрованным до оживления, потом стирается.')
    cleanup(root)
  })

  it('кнопка горит только когда готовы и поля, и набор имени', async () => {
    const root = await mount(reviveLike())
    expect(primary(root).disabled).toBe(true)
    await fill(root, 'sheet-confirm-input', 'bronya')
    expect(primary(root).disabled).toBe(true)
    await fill(root, 'sheet-field-root_password', SECRET)
    expect(primary(root).disabled).toBe(false)
    cleanup(root)
  })

  it('perform получает набранное и снимок полей; поля стёрты сразу после нажатия, до ответа', async () => {
    const seen = []
    let resolve
    const done = []
    let closed = 0
    const sheet = reviveLike({
      perform: (typed, values) => {
        seen.push({ typed, values })
        return new Promise((r) => { resolve = r })
      },
      onDone: (resp) => done.push(resp),
    })
    const root = await mount(sheet, () => closed++)
    await fill(root, 'sheet-field-root_password', SECRET)
    await fill(root, 'sheet-field-awgm_login', 'admin')
    await fill(root, 'sheet-field-expires_days', '7')
    await fill(root, 'sheet-confirm-input', 'Bronya')
    await act(async () => primary(root).click())
    await flush()
    expect(seen).toEqual([{ typed: 'Bronya', values: { root_password: SECRET, awgm_login: 'admin', expires_days: '7' } }])
    // Запрос ещё в пути, а пароля уже нет ни в поле, ни в разметке.
    expect(root.querySelector('#sheet-field-root_password').value).toBe('')
    expect(root.querySelector('#sheet-field-awgm_login').value).toBe('')
    expect(root.querySelector('#sheet-field-expires_days').value).toBe('30')
    expect(root.innerHTML).not.toContain(SECRET)
    await act(async () => resolve({ status: 'waiting' }))
    await flush()
    expect(done).toEqual([{ status: 'waiting' }])
    expect(closed).toBe(1)
    cleanup(root)
  })

  it('отказ сервера: фраза на листе, поля пустые, кнопка погасла до нового ввода', async () => {
    const root = await mount(reviveLike({
      perform: () => Promise.reject(new ApiError(409, 'agent_alive', 'x')),
      errorText: (err) => (err.code === 'agent_alive' ? 'Агент на роутере отвечает — оживлять нечего.' : ''),
    }))
    await fill(root, 'sheet-field-root_password', SECRET)
    await fill(root, 'sheet-confirm-input', 'bronya')
    await act(async () => primary(root).click())
    await flush()
    await flush()
    expect(root.textContent).toContain('Агент на роутере отвечает — оживлять нечего.')
    expect(root.querySelector('#sheet-field-root_password').value).toBe('')
    expect(primary(root).disabled).toBe(true)
    expect(root.innerHTML).not.toContain(SECRET)
    cleanup(root)
  })

  it('«Отмена» стирает поля и закрывает; описание листа введённого не получило', async () => {
    let closed = 0
    const sheet = reviveLike()
    const before = JSON.stringify(sheet)
    const root = await mount(sheet, () => closed++)
    await fill(root, 'sheet-field-root_password', SECRET)
    expect(JSON.stringify(sheet)).toBe(before)
    expect(JSON.stringify(sheet)).not.toContain(SECRET)
    await act(async () => cancel(root).click())
    expect(closed).toBe(1)
    expect(root.querySelector('#sheet-field-root_password').value).toBe('')
    cleanup(root)
  })

  it('лист без полей работает как раньше: perform(typed), второго блока нет', async () => {
    const seen = []
    const root = await mount(localSheet({
      title: 't', body: 'b', confirmPhrase: 'bronya',
      perform: (...args) => { seen.push(args); return Promise.resolve({}) },
    }))
    expect(root.querySelector('[id^="sheet-field-"]')).toBeNull()
    expect(root.querySelector('.sheet-note')).toBeNull()
    await fill(root, 'sheet-confirm-input', 'bronya')
    await act(async () => primary(root).click())
    await flush()
    expect(seen).toEqual([['bronya', {}]])
    cleanup(root)
  })
})
