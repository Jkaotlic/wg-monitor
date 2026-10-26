// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// MINI-01: команда уже поставлена (есть cmd_id), а опрос итога упал на
// временной ошибке релея. Экран не имеет права писать «Команда не отправлена».
const mocks = vi.hoisted(() => ({ send: null, poll: [], pollCalls: 0 }))

vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  sendCommand: (...a) => mocks.send(...a),
  fetchCommandResult: () => {
    mocks.pollCalls++
    const r = mocks.poll.length > 1 ? mocks.poll.shift() : mocks.poll[0]
    return r instanceof Error ? Promise.reject(r) : Promise.resolve(r)
  },
}))

const { ApiError, isTransient } = await import('../src/api.js')
const { useCommand } = await import('../src/useCommand.js')
const { commandFailText, SENT_NO_RESULT_TEXT } = await import('../src/maintenance.js')

const SENT_PHRASE = 'Команда ушла на роутер, но ответ не получен — откройте экран позже, чтобы увидеть итог.'

let api
function Probe({ routerID }) {
  api = useCommand(routerID)
  return <p id="t">{api.error ? commandFailText({ sent: api.sent, errorCode: api.errorCode, error: api.error }) : ''}</p>
}

let root
async function mount(routerID = 1) {
  root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<Probe routerID={routerID} />, root))
}
const text = () => root.querySelector('#t').textContent

beforeEach(() => {
  vi.useFakeTimers()
  mocks.poll = []
  mocks.pollCalls = 0
  mocks.send = () => Promise.resolve({ cmd_id: 'c1' })
})
afterEach(() => {
  vi.useRealTimers()
  if (root) { render(null, root); root.remove(); root = null }
})

describe('isTransient', () => {
  it('0, 429, 502, 503, 504 и сбой fetch -- временные', () => {
    for (const s of [0, 429, 502, 503, 504]) expect(isTransient(new ApiError(s, 'unknown', 'x'))).toBe(true)
    expect(isTransient(new TypeError('Failed to fetch'))).toBe(true)
  })
  it('прочее -- нет', () => {
    for (const s of [400, 401, 403, 404, 409, 500]) expect(isTransient(new ApiError(s, 'x', 'x'))).toBe(false)
    expect(isTransient(null)).toBe(false)
    expect(isTransient(new Error('boom'))).toBe(false)
  })
})

describe('commandFailText', () => {
  it('ушла, итога нет', () => {
    expect(SENT_NO_RESULT_TEXT).toBe(SENT_PHRASE)
    expect(commandFailText({ sent: true, errorCode: null, error: SENT_PHRASE })).toBe(SENT_PHRASE)
  })
  it('не отправлена: код без своей фразы', () => {
    expect(commandFailText({ sent: false, errorCode: 'weird_code', error: 'x failed: 400' })).toBe('Команда не отправлена')
  })
  it('не отправлена: без кода -- готовая фраза', () => {
    expect(commandFailText({ sent: false, errorCode: null, error: 'Сервер не ответил — проверьте связь и повторите.' })).toBe('Сервер не ответил — проверьте связь и повторите.')
  })
})

describe('useCommand: сбой опроса после постановки', () => {
  it('503 на опросе, затем итог -- итог есть, ошибки нет', async () => {
    mocks.poll = [new ApiError(503, 'unknown', 'x'), { status: 'ok', output: '' }]
    await mount()
    let p
    await act(async () => { p = api.run('x') })
    await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
    await p
    expect(api.error).toBe(null)
    expect(api.result.status).toBe('ok')
    expect(mocks.pollCalls).toBe(2)
  })

  it('502 до дедлайна -- sent и фраза «ушла, ответ не получен»', async () => {
    mocks.poll = [new ApiError(502, 'unknown', 'x')]
    await mount()
    let p
    await act(async () => { p = api.run('x', {}, { deadlineMs: 6000 }) })
    await act(async () => { await vi.advanceTimersByTimeAsync(20000) })
    await p
    expect(api.sent).toBe(true)
    expect(text()).toBe(SENT_PHRASE)
    expect(mocks.pollCalls).toBeGreaterThan(1)
    expect(mocks.pollCalls).toBeLessThan(10)
  })

  it('постановка упала -- sent=false, «Команда не отправлена»', async () => {
    mocks.send = () => Promise.reject(new ApiError(400, 'weird_code', 'x failed: 400'))
    await mount()
    await act(async () => { await api.run('x') })
    expect(api.sent).toBe(false)
    expect(text()).toBe('Команда не отправлена')
  })

  it('500 на опросе после постановки -- фраза «ушла», не «повторите»', async () => {
    mocks.poll = [new ApiError(500, 'internal', 'x')]
    await mount()
    await act(async () => { await api.run('x') })
    expect(api.sent).toBe(true)
    expect(text()).toBe(SENT_PHRASE)
  })

  it('commandFailText: sent и общая ошибка -- фраза «ушла»', () => {
    expect(commandFailText({ sent: true, errorCode: 'internal', error: 'Не получилось: сервер ответил ошибкой. Повторите; если повторится — напишите админу.' })).toBe(SENT_PHRASE)
  })

  it('смена роутера на экране во время повторов: итог роутера 1 помечен for_router=1', async () => {
    mocks.poll = [new ApiError(503, 'unknown', 'x'), { status: 'ok', output: '' }]
    await mount(1)
    let p
    await act(async () => { p = api.run('x') })
    await act(async () => render(<Probe routerID={2} />, root))
    await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
    const res = await p
    expect(res.for_router).toBe(1)
    expect(api.result.for_router).toBe(1)
    expect(api.result.for_router).not.toBe(2)
  })

  it('смена роутера во время повторов: поздний итог не применяется', async () => {
    mocks.poll = [new ApiError(503, 'unknown', 'x'), { status: 'ok', output: '' }]
    await mount(1)
    let p
    await act(async () => { p = api.run('x') })
    await act(async () => render(null, root))
    await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
    expect(await p).toBe(null)
  })
})
