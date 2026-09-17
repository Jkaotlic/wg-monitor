import { describe, it, expect, vi, beforeEach } from 'vitest'

const mocks = vi.hoisted(() => ({ replies: [], calls: [] }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchCommandResult: (routerID, cmdID, waitSec) => {
    mocks.calls.push([routerID, cmdID, waitSec])
    const r = mocks.replies.shift()
    return r instanceof Error ? Promise.reject(r) : Promise.resolve(r ?? null)
  },
}))

const { waitCommand, waitDeadlineMs, commandOutcome, repeatWhilePending } = await import('../src/commandWait.js')
const { AGENT_OLDER_THAN_APP } = await import('../src/labels.js')

beforeEach(() => {
  mocks.replies = []
  mocks.calls = []
})

describe('waitCommand', () => {
  it('ждёт первого ответа короткими хопами', async () => {
    mocks.replies = [null, null, { status: 'ok', output: '' }]
    expect(await waitCommand(7, 'c1')).toEqual({ status: 'ok', output: '' })
    expect(mocks.calls).toEqual([
      [7, 'c1', 10],
      [7, 'c1', 10],
      [7, 'c1', 10],
    ])
  })

  it('дедлайн: последний хоп не перелетает, итог -- null', async () => {
    let t = 0
    const now = () => (t += 25_000)
    expect(await waitCommand(7, 'c1', { deadlineMs: 90_000, now })).toBe(null)
    expect(mocks.calls).toEqual([
      [7, 'c1', 10],
      [7, 'c1', 1],
    ])
  })

  it('экран ушёл во время хопа -- ответ не принимается', async () => {
    mocks.replies = [{ status: 'ok', output: '' }]
    let n = 0
    expect(await waitCommand(7, 'c1', { alive: () => n++ < 1 })).toBe(null)
    expect(mocks.calls).toHaveLength(1)
  })

  it('ошибка сети -- исключение, а не null', async () => {
    mocks.replies = [new Error('net')]
    await expect(waitCommand(7, 'c1')).rejects.toThrow('net')
  })

  it('дедлайн шире для спящего роутера', () => {
    expect(waitDeadlineMs(false)).toBe(90_000)
    expect(waitDeadlineMs(true)).toBe(360_000)
  })
})

// Сервер отвечает «ещё спрашиваю роутер» (state checking у удаления,
// analyzing у предпросмотра) -- клиент повторяет тот же запрос с паузой.
describe('repeatWhilePending', () => {
  const pending = (r) => r?.state === 'checking'

  it('повторяет, пока ответ «в процессе», и отдаёт первый настоящий', async () => {
    const replies = [{ state: 'checking' }, { state: 'checking' }, { state: 'queued', cmd_id: 'c1' }]
    const pauses = []
    let sent = 0
    const out = await repeatWhilePending(
      () => {
        sent++
        return Promise.resolve(replies.shift())
      },
      { pending, pauseMs: 1500, sleep: (ms) => (pauses.push(ms), Promise.resolve()) },
    )
    expect(out).toEqual({ resp: { state: 'queued', cmd_id: 'c1' }, settled: true })
    expect(sent).toBe(3)
    expect(pauses).toEqual([1500, 1500])
  })

  it('дедлайн -- последний ответ и settled:false', async () => {
    let t = 0
    const out = await repeatWhilePending(() => Promise.resolve({ state: 'checking' }), {
      pending,
      deadlineMs: 120_000,
      now: () => (t += 50_000),
      sleep: () => Promise.resolve(),
    })
    expect(out).toEqual({ resp: { state: 'checking' }, settled: false })
  })

  it('экран ушёл -- не повторяет', async () => {
    let sent = 0
    let alive = true
    const out = await repeatWhilePending(
      () => {
        sent++
        alive = false
        return Promise.resolve({ state: 'checking' })
      },
      { pending, alive: () => alive, sleep: () => Promise.resolve() },
    )
    expect(sent).toBe(1)
    expect(out.settled).toBe(false)
  })

  it('ошибка запроса -- исключение', async () => {
    await expect(repeatWhilePending(() => Promise.reject(new Error('409')), { pending })).rejects.toThrow('409')
  })
})

describe('commandOutcome', () => {
  const words = { ok: 'Готово.', fail: 'Роутер не сделал', pending: 'Ждём роутер.' }

  it('успех, ожидание, отказ, старый агент', () => {
    expect(commandOutcome({ status: 'ok', output: 'x' }, words)).toEqual({ tone: 'ok', text: 'Готово.', done: true })
    expect(commandOutcome(null, words)).toEqual({ tone: 'warn', text: 'Ждём роутер.', done: false })
    expect(commandOutcome({ status: 'err', output: ' awg-manager: 500 ' }, words)).toEqual({ tone: 'error', text: 'Роутер не сделал: awg-manager: 500', done: false })
    expect(commandOutcome({ status: 'timeout', output: '' }, words)).toEqual({ tone: 'error', text: 'Роутер не сделал: timeout', done: false })
    expect(commandOutcome({ status: 'err', output: 'unknown action: tunnel_delete' }, words)).toEqual({ tone: 'error', text: AGENT_OLDER_THAN_APP, done: false })
  })
})
