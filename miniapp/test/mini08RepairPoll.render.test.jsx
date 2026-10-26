// @vitest-environment jsdom
// MINI-08: опрос починки последовательный, а ответ, начатый до «Починить»,
// не возвращает кнопку поверх идущей починки.
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ status: null, calls: 0 }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRepairStatus: () => {
    mocks.calls++
    return mocks.status()
  },
  startRepair: () => Promise.resolve({ job_id: 'j-9', state: 'running', running: true, steps: [], check_name: 'tunnel_awg10' }),
}))

const { RepairScreen } = await import('../src/screens/RepairScreen.jsx')
const tick = () => act(async () => { await Promise.resolve() })

async function mount() {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<RepairScreen routerID={5} checkName="tunnel_awg10" lineName="vymysel-nl" onClose={() => {}} />, root))
  await tick()
  return root
}

afterEach(() => {
  vi.useRealTimers()
  mocks.calls = 0
})

describe('MINI-08: экран починки', () => {
  it('поздний ответ «не идёт», начатый до запуска, не отменяет запущенную починку', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    let release
    const second = new Promise((r) => { release = r })
    let n = 0
    mocks.status = () => (n++ === 0 ? Promise.resolve({ running: false }) : second)
    const root = await mount()
    // Второй опрос ушёл и висит: ответ на него придёт уже после запуска.
    await act(async () => { await vi.advanceTimersByTimeAsync(2500) })
    expect(mocks.calls).toBe(2)
    await act(async () => root.querySelector('.repair-start').click())
    await tick()
    expect(root.querySelector('.repair-start')).toBe(null)
    await act(async () => release({ running: false }))
    await tick()
    expect(root.querySelector('.repair-start')).toBe(null)
    expect(root.querySelector('.repair-title').textContent).toBe('Поднимаю связь')
    render(null, root)
    root.remove()
  })

  it('следующий опрос не начинается, пока не пришёл ответ на прежний', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    mocks.status = () => new Promise(() => {})
    const root = await mount()
    expect(mocks.calls).toBe(1)
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
    expect(mocks.calls).toBe(1)
    render(null, root)
    root.remove()
  })
})
