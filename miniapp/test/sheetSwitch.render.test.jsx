// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { SheetHost } from '../src/ui/Sheet.jsx'
import { navReducer } from '../src/nav.js'
import { localSheet } from '../src/sheet.js'
import { reviveFields, reviveReady } from '../src/revive.js'

// Лист, сменённый другим без закрытия (второй тап поверх, возврат из
// Telegram), не должен унести набранное: пароль root, введённый для роутера
// A, не имеет права оказаться в листе роутера B.
const SECRET = 'root-Пароль-A-9f3kq'

function reviveSheetFor(router) {
  return localSheet({
    title: `Оживить агент на «${router.nickname}»?`,
    body: 'b',
    confirmPhrase: router.nickname,
    fields: reviveFields(router),
    fieldsReady: reviveReady(router),
    perform: () => Promise.resolve({}),
  })
}

const A = { id: 1, nickname: 'router-a', panel_address_known: true }
const B = { id: 2, nickname: 'router-b', panel_address_known: true }

describe('смена листа без закрытия', () => {
  it('номер листа растёт на каждый новый лист и не растёт на закрытие', () => {
    const base = { routerID: 1, tab: 'router', overlay: null, sheet: null }
    const a = navReducer(base, { type: 'sheet', sheet: reviveSheetFor(A) })
    const b = navReducer(a, { type: 'sheet', sheet: reviveSheetFor(B) })
    expect(b.sheetSeq).toBeGreaterThan(a.sheetSeq)
    expect(navReducer(b, { type: 'sheet', sheet: b.sheet }).sheetSeq).toBe(b.sheetSeq)
    expect(navReducer(b, { type: 'sheet', sheet: null }).sheetSeq).toBe(b.sheetSeq)
  })

  it('пароль, набранный для A, не виден в листе B', async () => {
    const root = document.createElement('div')
    document.body.appendChild(root)
    let nav = { routerID: 1, tab: 'router', overlay: null, sheet: null }
    const dispatch = (action) => { nav = navReducer(nav, action) }
    const draw = () => act(async () => { render(<SheetHost nav={nav} dispatch={dispatch} />, root) })

    dispatch({ type: 'sheet', sheet: reviveSheetFor(A) })
    await draw()
    const passA = root.querySelector('#sheet-field-root_password')
    await act(async () => {
      passA.value = SECRET
      passA.dispatchEvent(new Event('input', { bubbles: true }))
    })
    expect(root.querySelector('#sheet-field-root_password').value).toBe(SECRET)

    dispatch({ type: 'sheet', sheet: reviveSheetFor(B) })
    await draw()
    expect(root.textContent).toContain('router-b')
    expect(root.querySelector('#sheet-field-root_password').value).toBe('')
    expect(root.innerHTML).not.toContain(SECRET)
    const primary = [...root.querySelectorAll('.sheet-actions button')].pop()
    const confirm = root.querySelector('#sheet-confirm-input')
    expect(confirm.value).toBe('')
    expect(primary.disabled).toBe(true)

    render(null, root)
    root.remove()
  })

  it('уход листа со страницы стирает значения полей, а не только забывает их', async () => {
    const root = document.createElement('div')
    document.body.appendChild(root)
    let live = null
    const sheet = localSheet({
      title: 't', body: 'b',
      fields: reviveFields(A),
      fieldsReady: (values) => { live = values; return true },
      perform: () => Promise.resolve({}),
    })
    let nav = { routerID: 1, tab: 'router', overlay: null, sheet: null }
    const dispatch = (action) => { nav = navReducer(nav, action) }
    dispatch({ type: 'sheet', sheet })
    await act(async () => { render(<SheetHost nav={nav} dispatch={dispatch} />, root) })
    const pass = root.querySelector('#sheet-field-root_password')
    await act(async () => {
      pass.value = SECRET
      pass.dispatchEvent(new Event('input', { bubbles: true }))
    })
    expect(live.root_password).toBe(SECRET)
    dispatch({ type: 'sheet', sheet: null })
    await act(async () => { render(<SheetHost nav={nav} dispatch={dispatch} />, root) })
    expect(root.querySelector('#sheet-field-root_password')).toBeNull()
    expect(live.root_password).toBe('')
    render(null, root)
    root.remove()
  })
})
