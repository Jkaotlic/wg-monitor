// @vitest-environment jsdom
import { it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// BUG-02: опрос статуса замены ходил setInterval'ом, запросы наслаивались, и
// запоздалый ответ (ушедший ДО запуска замены, вернувшийся после) откатывал
// экран: setJob(null) возвращал форму поверх идущей операции.
const mocks = vi.hoisted(() => ({ statusCalls: [], startCalls: [] }))

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  return {
    ...real,
    fetchReplaceStatus: (routerID) => new Promise((resolve) => mocks.statusCalls.push({ routerID, resolve })),
    fetchVPNAccounts: () =>
      Promise.resolve({
        accounts: [
          {
            provider: 'amnezia',
            label: 'Amnezia Premium',
            connected: true,
            devices_used: 1,
            devices_max: 3,
            options: [{ id: 'nl', label: 'Нидерланды' }],
          },
        ],
      }),
    fetchRouterSettings: () => Promise.resolve({ role: 'owner' }),
    startReplace: (routerID) => new Promise((resolve) => mocks.startCalls.push({ routerID, resolve })),
  }
})

const { ReplaceScreen } = await import('../src/screens/ReplaceScreen.jsx')

const started = { job_id: 'j-new', state: 'running', running: true, steps: [] }
let root

const open = async (routerID) => {
  await act(async () => render(
    <ReplaceScreen routerID={routerID} tunnel={{ id: 'Wireguard0', name: 'старый' }} policyName="VPN" onClose={() => {}} />,
    root,
  ))
  await act(async () => {})
}
const clickText = (text) => act(async () => {
  const button = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)
  expect(button).toBeDefined()
  button.click()
})
const start = async () => {
  await clickText('Нидерланды')
  await clickText('Заменить конфиг')
}

beforeEach(() => {
  mocks.statusCalls = []
  mocks.startCalls = []
  vi.useFakeTimers()
  root = document.createElement('div')
  document.body.appendChild(root)
})

afterEach(async () => {
  await act(async () => render(null, root))
  root.remove()
  vi.useRealTimers()
})

it('запоздалый ответ опроса не возвращает форму поверх запущенной замены', async () => {
  await open(7)
  await start()
  await act(async () => mocks.startCalls[0].resolve(started))
  expect(root.textContent).toContain('Идёт замена')

  // Опрос, ушедший до запуска, вернулся только сейчас — с устаревшим «пусто».
  await act(async () => mocks.statusCalls[0].resolve(null))
  expect(root.textContent).toContain('Идёт замена')
})

it('опрос, начатый до подтверждения POST, не подавляет успешный запуск', async () => {
  await open(7)
  await act(async () => mocks.statusCalls[0].resolve(null))
  await start()
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  // Если опрос успел уйти, сервер мог ответить «пусто» до фиксации запуска.
  if (mocks.statusCalls[1]) await act(async () => mocks.statusCalls[1].resolve(null))
  await act(async () => mocks.startCalls[0].resolve(started))
  expect(root.textContent).toContain('Идёт замена')

  const pollsBefore = mocks.statusCalls.length
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(mocks.statusCalls).toHaveLength(pollsBefore + 1)
  await act(async () => mocks.statusCalls.at(-1).resolve({ ...started, state: 'done', running: false }))
  expect(root.textContent).toContain('Чем кончилось')
})

it('ответ запуска для прежнего роутера не меняет новый экран', async () => {
  await open(7)
  await start()
  await open(8)
  await act(async () => mocks.startCalls[0].resolve(started))

  expect(root.textContent).not.toContain('Идёт замена')
  expect(root.textContent).toContain('Нидерланды')
  expect(mocks.statusCalls.at(-1).routerID).toBe(8)
})
