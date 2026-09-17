// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { Sheet } from '../src/ui/Sheet.jsx'
import { localSheet, initialFieldValues } from '../src/sheet.js'

const FIELDS = [
  { name: 'code', label: 'Код', type: 'text', hint: (v) => (v.code ? `набрано: ${v.code}` : '') },
  { name: 'sure', label: 'Точно', type: 'toggle', initial: false, showIf: (v) => v.code === 'x' },
]

async function mount(sheet) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<Sheet sheet={sheet} asleep={false} onClose={() => {}} />, root))
  return root
}
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const primary = (root) => [...root.querySelectorAll('.sheet-actions button')].pop()

describe('поле-переключатель, showIf и подсказка', () => {
  it('начальное значение переключателя -- false, текста -- пустая строка', () => {
    expect(initialFieldValues(FIELDS)).toEqual({ code: '', sure: false })
  })

  it('переключатель появляется по условию, подсказка следует вводу, значение уходит булевым', async () => {
    const got = []
    const sheet = localSheet({
      title: 't',
      body: 'b',
      fields: FIELDS,
      fieldsReady: (v) => v.code === 'x' && v.sure === true,
      perform: (_typed, values) => {
        got.push(values)
        return Promise.resolve({})
      },
    })
    const root = await mount(sheet)
    expect(root.querySelector('#sheet-field-sure')).toBe(null)
    expect(root.querySelector('.sheet-field-hint')).toBe(null)

    const code = root.querySelector('#sheet-field-code')
    await act(async () => {
      code.value = 'x'
      code.dispatchEvent(new Event('input', { bubbles: true }))
    })
    expect(root.querySelector('.sheet-field-hint').textContent).toBe('набрано: x')
    const sure = root.querySelector('#sheet-field-sure')
    expect(sure.type).toBe('checkbox')
    expect(sure.closest('label').textContent).toContain('Точно')
    expect(primary(root).disabled).toBe(true)

    await act(async () => {
      sure.checked = true
      sure.dispatchEvent(new Event('change', { bubbles: true }))
    })
    expect(primary(root).disabled).toBe(false)
    await act(async () => primary(root).click())
    await flush()
    expect(got).toEqual([{ code: 'x', sure: true }])
    render(null, root)
    root.remove()
  })
})

describe('поле keep переживает отправку', () => {
  it('после отказа значение keep на месте, остальные стёрты', async () => {
    const fields = [
      { name: 'version', label: 'Версия', type: 'text', keep: true },
      { name: 'pw', label: 'Пароль', type: 'password' },
    ]
    const sheet = localSheet({ title: 't', body: 'b', fields, perform: () => Promise.reject(new Error('x')) })
    const root = await mount(sheet)
    for (const [id, v] of [['version', 'v1.2.3'], ['pw', 'secret']]) {
      const el = root.querySelector(`#sheet-field-${id}`)
      await act(async () => {
        el.value = v
        el.dispatchEvent(new Event('input', { bubbles: true }))
      })
    }
    await act(async () => primary(root).click())
    await flush()
    expect(root.querySelector('#sheet-field-version').value).toBe('v1.2.3')
    expect(root.querySelector('#sheet-field-pw').value).toBe('')
    render(null, root)
    root.remove()
  })
})
