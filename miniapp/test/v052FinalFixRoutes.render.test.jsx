// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Финальное ревью v0.52, п. 2: «Перезапустить HydraRoute Neo» не зависит от
// снимка маршрутов -- до переезда кнопка в «Настройках» была безусловной.
const mocks = vi.hoisted(() => ({ calls: [], answers: {}, role: 'owner' }))

vi.mock('../src/useCommand.js', async () => {
  const { useState } = await import('preact/hooks')
  return {
    useCommand: () => {
      const [state, setState] = useState({ busy: false, result: null, error: null, errorCode: null, sleepNote: '' })
      return {
        ...state,
        run: (action, args) => {
          mocks.calls.push({ action, args })
          const res = mocks.answers[action] ?? null
          setState({ busy: false, result: res, error: null, errorCode: null, sleepNote: '' })
          return Promise.resolve(res)
        },
      }
    },
  }
})
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve({ role: mocks.role }),
}))

const { RoutesTab } = await import('../src/screens/RoutesTab.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const restart = (root) => [...root.querySelectorAll('.hrneo-actions button')].find((b) => b.textContent.trim() === 'Перезапустить')

async function mount() {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<RoutesTab routerID={7} asleep={false} openSheet={() => {}} layer="routes" layerParams={{}} openLayer={() => {}} closeLayer={() => {}} />, root))
  await flush()
  await flush()
  return root
}
const cleanup = (root) => {
  render(null, root)
  root.remove()
}

beforeEach(() => {
  mocks.calls.length = 0
  mocks.answers = {}
  mocks.role = 'owner'
})

describe('v0.52 финал п. 2: перезапуск HydraRoute Neo без снимка маршрутов', () => {
  it('снимок ещё грузится -- кнопка есть', async () => {
    const root = await mount()
    expect(root.querySelector('.hrneo-status')).toBeTruthy()
    expect(restart(root)).toBeTruthy()
    cleanup(root)
  })
  it('снимок не пришёл (отказ роутера) -- кнопка есть', async () => {
    mocks.answers.route_status = { status: 'error', output: 'boom' }
    const root = await mount()
    expect(restart(root)).toBeTruthy()
    cleanup(root)
  })
  it('роль без права обслуживания -- кнопки нет', async () => {
    mocks.role = 'viewer'
    const root = await mount()
    expect(restart(root)).toBeFalsy()
    cleanup(root)
  })
})
