// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { SheetHost } from '../src/ui/Sheet.jsx'
import { navReducer } from '../src/nav.js'
import { localSheet } from '../src/sheet.js'

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

describe('занятый лист и «назад»', () => {
  it('пока запрос листа в пути, «назад» его не закрывает; ответ -- закрывает сам лист', async () => {
    const root = document.createElement('div')
    document.body.appendChild(root)
    let finish
    let nav = { routerID: 1, tab: 'router', overlay: 'cabinet', sheet: null }
    const draw = () => act(async () => { render(<SheetHost nav={nav} dispatch={dispatch} />, root) })
    const dispatch = (action) => {
      nav = navReducer(nav, action)
      draw()
    }
    nav = navReducer(nav, {
      type: 'sheet',
      sheet: localSheet({ title: 'Удалить ключ?', body: 'b', buttonLabel: 'Удалить', perform: () => new Promise((r) => { finish = r }) }),
    })
    await draw()
    await act(async () => [...root.querySelectorAll('.sheet-actions button')].pop().click())
    await flush()
    expect(nav.sheetBusy).toBe(true)
    await act(async () => dispatch({ type: 'back' }))
    expect(nav.sheet).not.toBe(null)
    expect(nav.overlay).toBe('cabinet')
    await act(async () => finish({}))
    await flush()
    await flush()
    expect(nav.sheet).toBe(null)
    expect('sheetBusy' in nav).toBe(false)
    expect(nav.overlay).toBe('cabinet')
    render(null, root)
    root.remove()
  })
})
