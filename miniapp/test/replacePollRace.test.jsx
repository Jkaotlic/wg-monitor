// @vitest-environment jsdom
import { it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// BUG-02: опрос статуса замены ходил setInterval'ом, запросы наслаивались, и
// запоздалый ответ (ушедший ДО запуска замены, вернувшийся после) откатывал
// экран: setJob(null) возвращал форму поверх идущей операции.
const mocks = vi.hoisted(() => ({ statusCalls: [] }))

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  return {
    ...real,
    fetchReplaceStatus: () => new Promise((resolve) => mocks.statusCalls.push({ resolve })),
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
    startReplace: () => Promise.resolve({ job_id: 'j-new', state: 'running', running: true, steps: [] }),
  }
})

const { ReplaceScreen } = await import('../src/screens/ReplaceScreen.jsx')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

beforeEach(() => {
  mocks.statusCalls = []
})

it('запоздалый ответ опроса не возвращает форму поверх запущенной замены', async () => {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () =>
    render(
      <ReplaceScreen routerID={7} tunnel={{ id: 'Wireguard0', name: 'старый' }} policyName="VPN" onClose={() => {}} />,
      root,
    ),
  )
  await flush()
  await flush()

  const option = [...root.querySelectorAll('.list-row')].find((r) => r.textContent.includes('Нидерланды'))
  await act(async () => option.click())
  await flush()
  const startBtn = [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Заменить конфиг')
  await act(async () => startBtn.click())
  await flush()
  await flush()
  expect(root.textContent).toContain('Идёт замена')

  // Опрос, ушедший до запуска, вернулся только сейчас — с устаревшим «пусто».
  expect(mocks.statusCalls.length).toBeGreaterThan(0)
  await act(async () => mocks.statusCalls[0].resolve(null))
  await flush()

  expect(root.textContent).toContain('Идёт замена')
  render(null, root)
})
