// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// MINI-07 (аудит 26.10): один сбой «Обновить» не стирает вкладку «Проверки».
const mocks = vi.hoisted(() => ({ fail: false }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouter: () =>
    mocks.fail
      ? Promise.reject(new Error('boom'))
      : Promise.resolve({ router: { id: 9, nickname: 'home', kind: 'static', status: 'ok', stale: false, last_seen_age_sec: 30 }, incidents: [] }),
  fetchRouterChecks: () => Promise.resolve({ checks: [], tunnels: [], traffic: null }),
}))
vi.mock('../src/useCommand.js', () => ({
  useCommand: () => ({ busy: false, result: null, error: null, errorCode: null, run: () => Promise.resolve(null) }),
}))

const { DiagTab } = await import('../src/screens/DiagTab.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)

beforeEach(() => { mocks.fail = false })

describe('MINI-07: ошибка обновления вкладки «Проверки»', () => {
  it('данные есть -- ошибка строкой над ними с повтором, вкладка на месте', async () => {
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<DiagTab routerID={9} asleep={false} />, root))
    await flush()
    expect(root.querySelector('h1').textContent).toBe('Проверки')
    mocks.fail = true
    await act(async () => button(root, 'Обновить').click())
    await flush()
    expect(root.querySelector('h1').textContent).toBe('Проверки')
    expect(root.querySelector('.error-line').textContent).toContain('Не удалось обновить')
    // «Обновить» рядом уже есть -- второй кнопки повтора у строки нет.
    expect(button(root, 'Повторить')).toBeUndefined()
    mocks.fail = false
    await act(async () => button(root, 'Обновить').click())
    await flush()
    expect(root.querySelector('.error-line')).toBe(null)
    render(null, root)
  })

  it('данных нет -- полноэкранная ошибка с повтором', async () => {
    mocks.fail = true
    const root = document.createElement('div')
    document.body.appendChild(root)
    await act(async () => render(<DiagTab routerID={9} asleep={false} />, root))
    await flush()
    expect(root.querySelector('h1')).toBe(null)
    expect(root.querySelector('.error-line').textContent).toContain('Не удалось прочитать состояние проверок')
    mocks.fail = false
    await act(async () => button(root, 'Повторить').click())
    await flush()
    expect(root.querySelector('h1').textContent).toBe('Проверки')
    render(null, root)
  })
})
