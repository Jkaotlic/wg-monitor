// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// «Откроется ли сайт» на экране маршрутов: то же поле, что у «Куда пойдёт
// сайт», своя кнопка и свой канал команд.
const mocks = vi.hoisted(() => ({ calls: [], answers: {} }))

vi.mock('../src/useCommand.js', async () => {
  const { useState } = await import('preact/hooks')
  return {
    useCommand: () => {
      const [state, setState] = useState({ busy: false, result: null, error: null, errorCode: null })
      return {
        ...state,
        run: (action, args) => {
          mocks.calls.push({ action, args })
          const res = mocks.answers[action] ?? null
          setState({ busy: false, result: res, error: null, errorCode: null })
          return Promise.resolve(res)
        },
      }
    },
  }
})

const { RoutesTab } = await import('../src/screens/RoutesTab.jsx')

async function mount() {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(<RoutesTab routerID={2} openSheet={() => {}} />, root)
  })
  return root
}

async function typeAndOpen(root, text) {
  const input = root.querySelector('#route-lookup-site')
  await act(async () => {
    input.value = text
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  const btn = [...root.querySelectorAll('button')].find((b) => b.textContent === 'Откроется ли')
  expect(btn, 'кнопки «Откроется ли» нет').toBeTruthy()
  await act(async () => {
    btn.click()
  })
}

describe('«Откроется ли сайт»', () => {
  it('спрашивает dns_open с чистым именем и показывает ответ', async () => {
    mocks.calls = []
    mocks.answers = { dns_open: { status: 'partial', output: '…' } }
    const root = await mount()
    await typeAndOpen(root, 'https://Example.COM/path')

    expect(mocks.calls.filter((c) => c.action === 'dns_open')).toEqual([{ action: 'dns_open', args: { domain: 'example.com' } }])
    expect(root.textContent).toContain('Адрес «example.com» есть, но сайт не отвечает')
    // Вопрос «куда пойдёт» при этом не задавался.
    expect(mocks.calls.some((c) => c.action === 'route_lookup')).toBe(false)
    render(null, root)
    root.remove()
  })

  it('не имя сайта -- не отправляется', async () => {
    mocks.calls = []
    mocks.answers = {}
    const root = await mount()
    await typeAndOpen(root, 'localhost')

    expect(mocks.calls.some((c) => c.action === 'dns_open')).toBe(false)
    expect(root.textContent).toContain('Это не похоже на адрес сайта — нужно имя вроде claude.ai')
    render(null, root)
    root.remove()
  })
})
