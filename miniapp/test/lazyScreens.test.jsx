// @vitest-environment jsdom
import { expect, it } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'
import { lazy } from 'preact/compat'
import { LazyScreen } from '../src/ui/LazyScreen.jsx'

it('показывает спокойную загрузку lazy-экрана', async () => {
  const Never = lazy(() => new Promise(() => {}))
  const root = document.createElement('div')
  await act(async () => {
    render(<LazyScreen><Never /></LazyScreen>, root)
    await Promise.resolve()
  })
  expect(root.textContent).toBe('Загрузка экрана…')
})

it('ловит сбой lazy chunk и предлагает перезагрузить приложение', async () => {
  const Broken = lazy(() => Promise.reject(new Error('chunk missing')))
  const root = document.createElement('div')
  await act(async () => {
    render(<LazyScreen><Broken /></LazyScreen>, root)
    await Promise.resolve()
  })
  expect(root.textContent).toContain('Не удалось загрузить экран')
  expect(root.querySelector('button').textContent).toBe('Перезагрузить')
})
