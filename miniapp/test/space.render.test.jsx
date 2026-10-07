// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Экран «Свободное место» целиком: отчёт на входе (не у спящего), крупнейшие
// каталоги, «Почистить сейчас» с итогом по месту до и после, карточка
// расписания очистки, гейт версии.
const mocks = vi.hoisted(() => ({ settings: null, calls: [], answers: {} }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRouterSettings: () => Promise.resolve(mocks.settings),
}))

vi.mock('../src/useCommand.js', async () => {
  const { useState } = await import('preact/hooks')
  return {
    useCommand: () => {
      const [state, setState] = useState({ busy: false, result: null, error: null, errorCode: null, sleepNote: '' })
      return {
        ...state,
        run: (action, args, opts) => {
          mocks.calls.push({ action, args, deadlineMs: opts?.deadlineMs })
          const pick = mocks.answers[action]
          const res = typeof pick === 'function' ? pick(args) : (pick ?? null)
          setState({ busy: false, result: res, error: null, errorCode: null, sleepNote: '' })
          return Promise.resolve(res)
        },
      }
    },
  }
})

const { SpaceScreen } = await import('../src/screens/SpaceScreen.jsx')

const ok = (v) => ({ status: 'ok', output: JSON.stringify(v) })
const CLEAN = { installed: true, schedule: '15 5 * * *', script_path: '/opt/etc/wg-monitor/entware-cleanup.sh', log_path: '/opt/var/log/wg-monitor/entware-cleanup.log', last_status: 'ok' }
let free

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
async function mount(props = {}) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<SpaceScreen routerID={2} routerName="home" asleep={false} onClose={() => {}} {...props} />, root))
  await flush()
  await flush()
  return root
}
const cleanup = (root) => { render(null, root); root.remove() }
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)
const click = async (el) => {
  await act(async () => el.click())
  await flush()
  await flush()
}

beforeEach(() => {
  free = 100 * 1024
  mocks.settings = { role: 'admin', agent_version: 'v0.57.0' }
  mocks.calls = []
  mocks.answers = {
    space_report: () => ok({ free_kb: free, total_kb: 1024 * 1024, top: [{ path: '/opt/var/log', kb: 51200 }, { path: '/opt/tmp', kb: 30720 }] }),
    entware_clean_run: () => {
      free += 30 * 1024
      return ok({ ...CLEAN, last_freed_kb: 4096 })
    },
    entware_clean_status: ok(CLEAN),
  }
})

describe('«Свободное место»', () => {
  it('на входе -- отчёт о месте и состояние расписания очистки', async () => {
    const root = await mount()
    expect(root.querySelector('h1').textContent).toBe('Свободное место')
    expect(mocks.calls.map((c) => c.action).sort()).toEqual(['entware_clean_status', 'space_report'])
    expect(root.textContent).toContain('100 МБ из 1,0 ГБ (10%)')
    expect(root.textContent).toContain('Больше всего занимают')
    expect(root.textContent).toContain('/opt/var/log')
    expect(root.textContent).toContain('50 МБ')
    expect(root.textContent).toContain('Очистка по расписанию')
    expect(root.textContent).toContain('каждый день в 05:15')
    // Своя кнопка очистки -- одна: у карточки расписания «Запустить сейчас» нет.
    expect(button(root, 'Запустить сейчас')).toBeUndefined()
    cleanup(root)
  })

  it('«Почистить сейчас» -- очистка, затем замер; итог по месту до и после', async () => {
    const root = await mount()
    await click(button(root, 'Почистить сейчас'))
    const actions = mocks.calls.map((c) => c.action)
    expect(actions.slice(-2)).toEqual(['entware_clean_run', 'space_report'])
    expect(root.textContent).toContain('Очистка выполнена, освобождено 30 МБ.')
    expect(root.textContent).toContain('130 МБ из 1,0 ГБ')
    cleanup(root)
  })

  it('du не отработал -- «не удалось узнать, чем занято место»', async () => {
    mocks.answers.space_report = ok({ free_kb: 2048, total_kb: 1024 * 1024, top: [] })
    const root = await mount()
    expect(root.querySelector('.space-top-unknown').textContent).toBe('Не удалось узнать, чем занято место.')
    cleanup(root)
  })

  it('спящему роутеру на входе ничего не шлём', async () => {
    const root = await mount({ asleep: true })
    expect(mocks.calls).toEqual([])
    expect(root.textContent).toContain('Место ещё не проверено.')
    expect(root.textContent).toContain('Роутер сейчас не на связи')
    cleanup(root)
  })

  it('старый агент: отчёта и кнопки нет, расписание очистки -- со своей «Запустить сейчас»', async () => {
    mocks.settings = { role: 'admin', agent_version: 'v0.56.0' }
    const root = await mount()
    expect(root.textContent).toContain('появятся после обновления агента на роутере до v0.57')
    expect(button(root, 'Почистить сейчас')).toBeUndefined()
    expect(mocks.calls.map((c) => c.action)).toEqual(['entware_clean_status'])
    expect(button(root, 'Запустить сейчас')).toBeDefined()
    cleanup(root)
  })

  it('не админу -- слова и ни одной команды', async () => {
    mocks.settings = { role: 'owner', agent_version: 'v0.57.0' }
    const root = await mount()
    expect(root.textContent).toContain('смотрит админ бота')
    expect(mocks.calls).toEqual([])
    cleanup(root)
  })

  it('ошибка очистки -- словами и подробности под раскрытием', async () => {
    mocks.answers.entware_clean_run = { status: 'err', output: 'write cleanup script: no space left on device' }
    const root = await mount()
    await click(button(root, 'Почистить сейчас'))
    expect(root.textContent).toContain('Роутер ответил ошибкой — подробности ниже.')
    expect(root.querySelector('.space-screen details pre').textContent).toContain('no space left')
    cleanup(root)
  })
})
