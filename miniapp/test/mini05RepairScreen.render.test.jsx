// @vitest-environment jsdom
// MINI-05, проводка: экран починки не пишет «Чиню» над пустым ответом и не
// молчит о сбое опроса.
import { describe, it, expect, vi } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({ status: null }))
vi.mock('../src/api.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchRepairStatus: () => mocks.status(),
  startRepair: () => Promise.resolve({ job_id: 'j-9', state: 'running', running: true }),
}))

const { RepairScreen } = await import('../src/screens/RepairScreen.jsx')
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

async function mount() {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<RepairScreen routerID={5} checkName="tunnel_awg10" lineName="vymysel-nl" onClose={() => {}} />, root))
  await flush()
  return root
}

describe('MINI-05: экран починки', () => {
  it('пустой ответ -- кнопка и никакого «Чиню»', async () => {
    mocks.status = () => Promise.resolve({ running: false })
    const root = await mount()
    expect(root.querySelector('.repair-title').textContent).not.toBe('Чиню')
    expect(root.querySelector('.repair-start')).toBeTruthy()
    render(null, root)
    root.remove()
  })

  it('сбой опроса назван', async () => {
    mocks.status = () => Promise.reject(new Error('сервер не ответил'))
    const root = await mount()
    expect(root.textContent).toContain('Не удалось узнать ход починки')
    render(null, root)
    root.remove()
  })

  it('чужое идущее задание -- починка роутера, не этого туннеля', async () => {
    mocks.status = () => Promise.resolve({ job_id: 'j-1', state: 'running', running: true, steps: [] })
    const root = await mount()
    expect(root.querySelector('.repair-title').textContent).toBe('На роутере идёт починка')
    render(null, root)
    root.remove()
  })
})

describe('v0.54: провал лесенки', () => {
  it('экран говорит, что делать, а не только где сломалось', async () => {
    const steps = ['failover', 'restart', 'reissue', 'recreate', 'failback'].map((name) => ({ name, status: 'failed' }))
    steps[0].status = 'done'
    steps[3].status = 'skipped'
    steps[4].status = 'skipped'
    steps[2].detail = 'источник не выдал конфиг'
    mocks.status = () => Promise.resolve({ job_id: 'j-2', state: 'failed', running: false, check_name: 'tunnel_awg10', hint: 'обновите ключ «Amnezia Premium» во вкладке «Управление»', steps })
    const root = await mount()
    const action = root.querySelector('.repair-action')
    expect(action?.textContent).toBe('Что делать: обновите ключ «Amnezia Premium» во вкладке «Управление».')
    render(null, root)
    root.remove()
  })
})

describe('review п.2: первый опрос /repair не удался', () => {
  it('кнопка «Починить» есть, ошибка названа', async () => {
    mocks.status = () => Promise.reject(new Error('сервер не ответил'))
    const root = await mount()
    expect(root.querySelector('.repair-start')?.textContent).toContain('Починить «vymysel-nl»')
    expect(root.textContent).toContain('Не удалось узнать ход починки')
    expect(root.querySelector('.repair-title').textContent).not.toBe('Узнаю, идёт ли починка…')
    render(null, root)
    root.remove()
  })
})

describe('review п.3: чужая законченная починка', () => {
  it('кнопка называет этот туннель, а не «ещё раз»', async () => {
    mocks.status = () => Promise.resolve({ job_id: 'j-1', state: 'success', running: false, steps: [] })
    const root = await mount()
    expect(root.querySelector('.repair-start').textContent).toContain('Починить «vymysel-nl»')
    expect(root.querySelector('.repair-start').textContent).not.toContain('ещё раз')
    render(null, root)
    root.remove()
  })
})

describe('MINI-05: сервер назвал туннель починки', () => {
  it('идёт починка этого туннеля (check_name совпал) -- «Поднимаю связь»', async () => {
    mocks.status = () => Promise.resolve({ job_id: 'j-7', state: 'running', running: true, steps: [], check_name: 'tunnel_awg10', tunnel_id: 'awg10' })
    const root = await mount()
    expect(root.querySelector('.repair-title').textContent).toBe('Поднимаю связь')
    render(null, root)
    root.remove()
  })
  it('закончена починка другого туннеля -- чистый экран и кнопка', async () => {
    mocks.status = () => Promise.resolve({ job_id: 'j-8', state: 'failed', running: false, check_name: 'tunnel_awg11', tunnel_id: 'awg11', steps: [{ name: 'issue', status: 'failed', detail: 'кабинет не ответил' }] })
    const root = await mount()
    expect(root.querySelector('.repair-title').textContent).toBe('Починки ещё не было')
    expect(root.textContent).not.toContain('кабинет не ответил')
    expect(root.querySelector('.repair-start').textContent).toContain('Починить «vymysel-nl»')
    render(null, root)
    root.remove()
  })
})
