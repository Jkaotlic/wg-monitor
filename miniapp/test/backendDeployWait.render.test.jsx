// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ health: null, asked: 0 }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchHealth: () => {
    mocks.asked++
    return mocks.health()
  },
}))

const { BackendDeployWait } = await import('../src/screens/BackendDeployWait.jsx')

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

// Часы теста: каждая пауза сдвигает время на свою длину и возвращается сразу.
function testClock() {
  let t = 1_000_000
  const sleeps = []
  return { now: () => t, sleep: async (ms) => { sleeps.push(ms); t += ms }, sleeps }
}

function queue(items) {
  return () => {
    if (items.length === 0) return new Promise(() => {})
    const r = items.shift()
    return r instanceof Error ? Promise.reject(r) : Promise.resolve(r)
  }
}

async function mount(clock) {
  const calls = { reloads: 0, back: 0 }
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(<BackendDeployWait targetVersion="v0.36.0" clock={clock} reload={() => calls.reloads++} onBack={() => calls.back++} />, root)
  })
  for (let i = 0; i < 4; i++) await flush()
  return { root, calls }
}
const cleanup = (root) => { render(null, root); root.remove() }

beforeEach(() => {
  mocks.asked = 0
})

describe('ожидание раскатки бэкенда', () => {
  it('полноэкранный слой: заголовок и строка хода', async () => {
    mocks.health = queue([{ status: 'ok', version: 'v0.35.0' }])
    const { root } = await mount(testClock())
    const layer = root.querySelector('.deploy-wait')
    expect(layer.getAttribute('role')).toBe('dialog')
    expect(layer.getAttribute('aria-modal')).toBe('true')
    expect(root.querySelector('.deploy-wait-title').textContent).toBe('Бэкенд обновляется до v0.36.0')
    expect(root.querySelector('.deploy-wait-line').textContent).toBe('Сервер ещё отвечает прежней версией v0.35.0 — скачивает новую…')
    expect(root.querySelector('button')).toBe(null)
    cleanup(root)
  })

  it('сервер молчит -- «перезапускается», это не провал', async () => {
    mocks.health = queue([new Error('net')])
    const { root } = await mount(testClock())
    expect(root.querySelector('.deploy-wait-line').textContent).toBe('Сервер перезапускается…')
    expect(root.querySelector('.deploy-wait-card').classList.contains('deploy-wait-running')).toBe(true)
    cleanup(root)
  })

  it('новая версия -- «Готово» и перезагрузка через 2 с', async () => {
    const clock = testClock()
    mocks.health = queue([{ version: 'v0.35.0' }, new Error('net'), { version: 'v0.36.0' }])
    const { root, calls } = await mount(clock)
    expect(root.querySelector('.deploy-wait-title').textContent).toBe('Готово, бэкенд v0.36.0')
    expect(root.querySelector('.deploy-wait-line').textContent).toBe('Перезагружаем страницу…')
    expect(clock.sleeps).toEqual([3000, 3000, 2000])
    expect(calls.reloads).toBe(1)
    cleanup(root)
  })

  it('5 минут без новой версии -- слова и «Вернуться», без перезагрузки', async () => {
    mocks.health = () => Promise.resolve({ version: 'v0.35.0' })
    const { root, calls } = await mount(testClock())
    expect(root.querySelector('.deploy-wait-title').textContent).toBe('Бэкенд не ответил новой версией за 5 минут')
    expect(root.querySelector('.deploy-wait-line').textContent).toBe('Проверьте сводку позже.')
    await act(async () => root.querySelector('button').click())
    expect(calls.back).toBe(1)
    expect(calls.reloads).toBe(0)
    expect(mocks.asked).toBe(101)
    cleanup(root)
  })

  it('уход со слоя останавливает опрос и перезагрузку', async () => {
    let release
    mocks.health = () => new Promise((resolve) => { release = resolve })
    const { root, calls } = await mount(testClock())
    cleanup(root)
    await act(async () => release({ version: 'v0.36.0' }))
    await flush()
    expect(calls.reloads).toBe(0)
    expect(mocks.asked).toBe(1)
  })
})
