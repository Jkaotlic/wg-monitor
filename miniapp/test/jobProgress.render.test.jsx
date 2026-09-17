// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// Очередь ответов: пустая очередь -- запрос, который не отвечает никогда.
// Так тест останавливает опрос в нужной фазе без поддельных таймеров.
const mocks = vi.hoisted(() => ({ replies: [], asked: [] }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchJob: (id) => {
    mocks.asked.push(id)
    if (mocks.replies.length === 0) return new Promise(() => {})
    const r = mocks.replies.shift()
    return r instanceof Error ? Promise.reject(r) : Promise.resolve(r)
  },
}))

const { JobProgress } = await import('../src/screens/JobProgress.jsx')
const { ApiError } = await import('../src/api.js')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)
const instant = () => Promise.resolve()

const STEPS = (statuses) =>
  ['terminal_connected', 'arch_detected', 'downloading'].map((name, i) => ({ name, status: statuses[i], detail: i === 1 && statuses[i] === 'done' ? 'arm64' : '' }))
const job = (over) => ({ id: 'j1', kind: 'provision', nickname: 'dacha-1', router_id: null, state: 'running', steps: STEPS(['done', 'active', 'pending']), version: '', hint: '', tail: '', ...over })

async function mount(props = {}) {
  const calls = { closed: 0, opened: [], done: [] }
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(
      <JobProgress
        jobId="j1"
        title="Установка агента на «dacha-1»"
        sleep={instant}
        onClose={() => calls.closed++}
        onOpenRouter={(id) => calls.opened.push(id)}
        onDone={(j) => calls.done.push(j.state)}
        {...props}
      />,
      root,
    )
  })
  await flush()
  await flush()
  return { root, calls }
}
const cleanup = (root) => { render(null, root); root.remove() }

beforeEach(() => {
  mocks.replies = []
  mocks.asked = []
})

describe('«Ход работы»', () => {
  it('идёт: шаги по-русски, текущий отмечен, уход не отменяет', async () => {
    mocks.replies = [job()]
    const { root, calls } = await mount()
    expect(mocks.asked[0]).toBe('j1')
    expect(root.querySelector('.overlay-title').textContent).toBe('Установка агента на «dacha-1»')
    expect(root.querySelector('.job-status').textContent).toBe('Установка агента идёт')
    const items = [...root.querySelectorAll('.job-step')]
    expect(items.map((li) => li.querySelector('.job-step-label').textContent)).toEqual([
      'Вход в терминал роутера',
      'Определение архитектуры роутера',
      'Скачивание агента',
    ])
    expect(items.map((li) => li.className)).toEqual(['job-step job-step-done', 'job-step job-step-active', 'job-step job-step-pending'])
    expect(items[1].getAttribute('aria-current')).toBe('step')
    expect(root.textContent).toContain('Уход с экрана задание не отменяет: оно продолжится на сервере.')
    expect(button(root, 'Открыть роутер')).toBeFalsy()
    expect(calls.done).toEqual([])
    cleanup(root)
  })

  it('сетевая ошибка -- «Переподключение…», последнее состояние на месте', async () => {
    mocks.replies = [job(), new Error('net')]
    const { root } = await mount()
    expect(root.querySelector('.job-reconnecting').textContent).toBe('Переподключение…')
    expect(root.querySelectorAll('.job-step')).toHaveLength(3)
    cleanup(root)
  })

  it('40 ошибок подряд -- нет связи, опрос остановлен', async () => {
    mocks.replies = Array.from({ length: 40 }, () => new Error('net'))
    const { root } = await mount()
    expect(root.textContent).toContain('Нет связи с сервером')
    expect(root.textContent).toContain('Задание могло продолжиться на сервере. Откройте этот экран заново, чтобы проверить.')
    expect(mocks.asked).toHaveLength(40)
    cleanup(root)
  })

  it('404 -- задание истекло, больше не спрашиваем', async () => {
    mocks.replies = [new ApiError(404, 'job_not_found', 'x', 'Задание не найдено или истекло')]
    const { root } = await mount()
    expect(root.textContent).toContain('Задание завершилось больше 30 минут назад')
    expect(mocks.asked).toHaveLength(1)
    expect(button(root, 'Закрыть')).toBeTruthy()
    cleanup(root)
  })

  it('провал: шаг словами, подсказка, подробности под раскрытием', async () => {
    mocks.replies = [
      job({
        state: 'failed',
        steps: STEPS(['done', 'done', 'failed']),
        hint: 'Роутер не скачал агента: проверьте интернет на роутере.',
        tail: 'wget: bad address',
      }),
    ]
    const { root, calls } = await mount()
    expect(root.querySelector('.job-status').textContent).toBe('Не получилось: Скачивание агента')
    expect(root.querySelector('.job-status').classList.contains('job-status-bad')).toBe(true)
    expect(root.querySelector('.job-hint').textContent).toBe('Роутер не скачал агента: проверьте интернет на роутере.')
    const details = root.querySelector('details.job-details')
    expect(details.open).toBe(false)
    expect(details.querySelector('summary').textContent).toBe('Подробности')
    expect(details.querySelector('pre.job-tail').textContent).toBe('wget: bad address')
    expect(root.querySelector('.job-step-detail').textContent).toBe('arm64')
    expect(button(root, 'Открыть роутер')).toBeFalsy()
    expect(root.textContent).not.toContain('Уход с экрана')
    await act(async () => button(root, 'Закрыть').click())
    expect(calls.closed).toBe(1)
    expect(calls.done).toEqual(['failed'])
    cleanup(root)
  })

  it('успех с роутером -- «Открыть роутер», onDone один раз', async () => {
    mocks.replies = [job(), job({ state: 'success', steps: STEPS(['done', 'done', 'done']), version: 'v0.36.0', router_id: 9 })]
    const { root, calls } = await mount()
    expect(root.querySelector('.job-status').textContent).toBe('Агент установлен и на связи · v0.36.0')
    await flush()
    expect(calls.done).toEqual(['success'])
    await act(async () => button(root, 'Открыть роутер').click())
    expect(calls.opened).toEqual([9])
    expect(mocks.asked).toHaveLength(2)
    cleanup(root)
  })

  it('успех без router_id -- только «Закрыть»', async () => {
    mocks.replies = [job({ state: 'success', steps: STEPS(['done', 'done', 'done']) })]
    const { root } = await mount()
    expect(button(root, 'Открыть роутер')).toBeFalsy()
    expect(button(root, 'Закрыть')).toBeTruthy()
    cleanup(root)
  })

  it('«назад» слоя -- onClose, подпись настраивается', async () => {
    mocks.replies = [job()]
    const { root, calls } = await mount({ backLabel: 'Обслуживание' })
    const back = root.querySelector('.overlay-back')
    expect(back.textContent).toBe('Обслуживание')
    await act(async () => back.click())
    expect(calls.closed).toBe(1)
    cleanup(root)
  })
})
