// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

// «Загрузить конфиг .conf»: выбор файла, имя, предпросмотр, «Добавить как
// новый». Содержимое конфига не должно оказаться ни в DOM, ни во втором запросе.
const mocks = vi.hoisted(() => ({ calls: [], answers: {}, role: 'owner', api: [], previewReply: null, pollReplies: [], confirmReply: null, result: null, reader: null }))

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

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  const reply = (v) => (v instanceof Error ? Promise.reject(v) : Promise.resolve(v))
  return {
    ...real,
    fetchRouterSettings: () => Promise.resolve({ role: mocks.role }),
    previewTunnelImport: (routerID, body) => {
      mocks.api.push(['preview', routerID, body])
      return reply(mocks.previewReply)
    },
    fetchTunnelImport: (routerID, token) => {
      mocks.api.push(['poll', routerID, token])
      return reply(mocks.pollReplies.shift() ?? mocks.previewReply)
    },
    confirmTunnelImport: (routerID, token) => {
      mocks.api.push(['confirm', routerID, token])
      return reply(mocks.confirmReply ?? { state: 'queued', cmd_id: 'c5', tunnel_name: 'amsterdam-nl' })
    },
    fetchCommandResult: (routerID, cmdID) => {
      mocks.api.push(['result', routerID, cmdID])
      return mocks.result instanceof Error ? Promise.reject(mocks.result) : Promise.resolve(mocks.result)
    },
  }
})

// Паузы опроса -- мгновенные: тест не ждёт секунд между вопросами.
vi.mock('../src/commandWait.js', async (importOriginal) => {
  const real = await importOriginal()
  return {
    ...real,
    repeatWhilePending: (once, opts) => {
      let t = 0
      return real.repeatWhilePending(once, { ...opts, sleep: () => Promise.resolve(), now: () => (t += 10_000) })
    },
  }
})

// Чтение файла -- по желанию теста: гонку двух выборов надо уметь развести.
vi.mock('../src/confImport.js', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, readConfBase64: (file, R) => (mocks.reader ? mocks.reader(file) : real.readConfBase64(file, R)) }
})

const { TunnelsTab } = await import('../src/screens/TunnelsTab.jsx')
const { ApiError } = await import('../src/api.js')

const SNAP = {
  policy_model: true,
  tunnels: [{ id: 'nwg1', name: 'amsterdam', iface: 'nwg1', type: 'managed', status: 'running' }],
  counts: {},
  policies: [],
}
const CONF = '[Interface]\nPrivateKey = test-only-not-a-key\nAddress = 198.51.100.2/32\n\n[Peer]\nEndpoint = 203.0.113.7:51820\n'
// Форма -- контракт части 1: state, analyzed, note?, can_confirm, preview.
const PREVIEW = {
  token: 'tok1',
  name: 'amsterdam-nl',
  state: 'ready',
  analyzed: true,
  can_confirm: true,
  preview: {
    endpoint: '203.0.113.7:51820',
    addresses: ['198.51.100.2/32'],
    dns: ['198.51.100.53'],
    mtu: 1280,
    problems: [{ code: 'mtu_low', message: 'MTU ниже рекомендуемого', severity: 'warning' }],
  },
}

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const flushMany = async (n = 6) => {
  for (let i = 0; i < n; i++) await flush()
}
const byText = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)
const routeStatusCalls = () => mocks.calls.filter((c) => c.action === 'route_status').length

async function mount(extra = {}) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<TunnelsTab routerID={7} openSheet={() => {}} onOpenRoutes={() => {}} {...extra} />, root))
  await flushMany(2)
  return root
}

// С v0.41 вход в загрузку -- обычная строка списка под «VPN-туннелями», а не
// карточка-переход (акцентная там одна -- «Новый VPN-туннель из кабинета»).
function importCard(root) {
  return [...root.querySelectorAll('.list-row-btn')].find((c) => c.querySelector('.row-title')?.textContent === 'Загрузить конфиг .conf')
}

async function openImport(root) {
  const card = importCard(root)
  expect(card, 'карточки «Загрузить конфиг .conf» нет').toBeTruthy()
  await act(async () => card.click())
}

async function pickFile(root, file) {
  const input = root.querySelector('.conf-pick-input')
  Object.defineProperty(input, 'files', { value: [file], configurable: true })
  await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })))
  await flushMany()
}

async function typeName(root, text) {
  const input = root.querySelector('#conf-import-name')
  await act(async () => {
    input.value = text
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function click(root, text) {
  const b = byText(root, text)
  expect(b, `кнопки «${text}» нет`).toBeTruthy()
  await act(async () => b.click())
  await flushMany()
}

beforeEach(() => {
  mocks.calls = []
  mocks.api = []
  mocks.answers = { route_status: { status: 'ok', output: JSON.stringify(SNAP) } }
  mocks.role = 'owner'
  mocks.previewReply = structuredClone(PREVIEW)
  mocks.pollReplies = []
  mocks.confirmReply = null
  mocks.reader = null
  mocks.result = { status: 'ok', output: '' }
})

describe('загрузка .conf', () => {
  it('вход -- только у владельца и админа', async () => {
    let root = await mount()
    expect(importCard(root)).toBeTruthy()
    render(null, root)
    mocks.role = 'operator'
    root = await mount()
    expect(importCard(root)).toBeFalsy()
    render(null, root)
  })

  it('не .conf и слишком большой -- отказ до чтения и без запроса', async () => {
    const root = await mount()
    await openImport(root)
    await pickFile(root, new File(['x'], 'notes.txt'))
    expect(root.textContent).toContain('Нужен файл с расширением .conf — конфиг WireGuard или AmneziaWG.')
    expect(byText(root, 'Проверить конфиг').disabled).toBe(true)
    await pickFile(root, new File(['a'.repeat(50 * 1024 + 1)], 'big.conf'))
    expect(root.textContent).toContain('Файл больше 50 КиБ — это не конфиг VPN-туннеля.')
    expect(byText(root, 'Проверить конфиг').disabled).toBe(true)
    expect(mocks.api).toEqual([])
    render(null, root)
  })

  it('весь путь: файл → имя → предпросмотр → «Добавить как новый»', async () => {
    const root = await mount()
    await openImport(root)
    await pickFile(root, new File([CONF], 'Amsterdam NL.conf'))
    expect(root.querySelector('.conf-pick-input').value).toBe('')
    expect(root.querySelector('#conf-import-name').value).toBe('amsterdam-nl')

    await typeName(root, 'Bad Name')
    expect(root.textContent).toContain('Имя не подходит')
    expect(byText(root, 'Проверить конфиг').disabled).toBe(true)
    await typeName(root, 'amsterdam-nl')
    expect(byText(root, 'Проверить конфиг').disabled).toBe(false)

    await click(root, 'Проверить конфиг')
    expect(mocks.api).toEqual([['preview', 7, { name: 'amsterdam-nl', confB64: btoa(CONF) }]])
    expect(root.textContent).toContain('203.0.113.7:51820')
    expect(root.textContent).toContain('198.51.100.53')
    expect(root.textContent).toContain('MTU ниже рекомендуемого')
    expect(root.textContent).toContain('Это добавление, а не замена')
    expect(root.innerHTML).not.toContain('PrivateKey')
    expect(root.innerHTML).not.toContain(btoa(CONF))

    const before = routeStatusCalls()
    await click(root, 'Добавить как новый')
    expect(mocks.api.slice(1)).toEqual([
      ['confirm', 7, 'tok1'],
      ['result', 7, 'c5'],
    ])
    expect(root.querySelector('.tunnel-outcome').textContent).toBe('VPN-туннель «amsterdam-nl» добавлен. Перенести на него правила можно в «Маршрутах».')
    expect(routeStatusCalls()).toBe(before + 1)
    expect(mocks.api.filter((c) => c[0] === 'preview')).toHaveLength(1)

    await click(root, 'К списку VPN-туннелей')
    expect(root.querySelector('.conf-import')).toBe(null)
    render(null, root)
  })

  it('роутер проверяет -- экран опрашивает, пока не готово; «Добавить» ждёт', async () => {
    mocks.previewReply = { ...structuredClone(PREVIEW), state: 'analyzing', analyzed: false, can_confirm: false, preview: { endpoint: '203.0.113.7:51820', problems: [] } }
    mocks.pollReplies = [structuredClone(mocks.previewReply), structuredClone(PREVIEW)]
    const root = await mount()
    await openImport(root)
    await pickFile(root, new File([CONF], 'home.conf'))
    await click(root, 'Проверить конфиг')
    expect(mocks.api.map((c) => c[0])).toEqual(['preview', 'poll', 'poll'])
    expect(mocks.api.filter((c) => c[0] === 'poll').every((c) => c[2] === 'tok1')).toBe(true)
    expect(root.textContent).toContain('MTU ниже рекомендуемого')
    expect(byText(root, 'Добавить как новый').disabled).toBe(false)
    render(null, root)
  })

  it('роутер так и не закончил проверку -- слова и «Проверить ещё раз»', async () => {
    const analyzing = { ...structuredClone(PREVIEW), state: 'analyzing', analyzed: false, can_confirm: false, preview: { endpoint: '203.0.113.7:51820' } }
    mocks.previewReply = analyzing
    mocks.pollReplies = Array.from({ length: 40 }, () => structuredClone(analyzing))
    const root = await mount()
    await openImport(root)
    await pickFile(root, new File([CONF], 'home.conf'))
    await click(root, 'Проверить конфиг')
    expect(root.textContent).toContain('Проверка ещё идёт: роутер пока не закончил её')
    expect(byText(root, 'Добавить как новый').disabled).toBe(true)
    mocks.pollReplies = [structuredClone(PREVIEW)]
    await click(root, 'Проверить ещё раз')
    expect(byText(root, 'Добавить как новый').disabled).toBe(false)
    expect(mocks.api.filter((c) => c[0] === 'preview')).toHaveLength(1)
    render(null, root)
  })

  it('проверка пропущена -- слова сервера, а без них -- свои', async () => {
    mocks.previewReply = { ...structuredClone(PREVIEW), analyzed: false, note: 'Агент на роутере старше v0.28 — проверка пропущена.', preview: { endpoint: '203.0.113.7:51820' } }
    let root = await mount()
    await openImport(root)
    await pickFile(root, new File([CONF], 'home.conf'))
    await click(root, 'Проверить конфиг')
    expect(root.textContent).toContain('Агент на роутере старше v0.28 — проверка пропущена.')
    expect(byText(root, 'Добавить как новый').disabled).toBe(false)
    render(null, root)
    mocks.previewReply = { ...structuredClone(PREVIEW), analyzed: false, preview: { endpoint: '203.0.113.7:51820' } }
    root = await mount()
    await openImport(root)
    await pickFile(root, new File([CONF], 'home.conf'))
    await click(root, 'Проверить конфиг')
    expect(root.textContent).toContain('агент на нём старше v0.28')
    render(null, root)
  })

  it('ошибка анализа -- «Добавить» погашена, сказано почему', async () => {
    mocks.previewReply = structuredClone(PREVIEW)
    mocks.previewReply.preview.problems = [{ code: 'bad_key', message: 'Ключ пира повреждён', severity: 'error' }]
    mocks.previewReply.can_confirm = false
    const root = await mount()
    await openImport(root)
    await pickFile(root, new File([CONF], 'home.conf'))
    await click(root, 'Проверить конфиг')
    expect(root.querySelector('.conf-problem-error').textContent).toBe('Ключ пира повреждён')
    expect(root.querySelector('.conf-blocking').textContent).toContain('Роутер не примет этот конфиг')
    expect(byText(root, 'Добавить как новый').disabled).toBe(true)
    render(null, root)
  })

  it('сервер отверг конфиг -- файл выбирается заново', async () => {
    mocks.previewReply = new ApiError(400, 'invalid_conf', 'x')
    const root = await mount()
    await openImport(root)
    await pickFile(root, new File([CONF], 'home.conf'))
    await click(root, 'Проверить конфиг')
    expect(root.textContent).toContain('Это не похоже на конфиг WireGuard или AmneziaWG')
    expect(byText(root, 'Проверить конфиг').disabled).toBe(true)
    mocks.previewReply = structuredClone(PREVIEW)
    await pickFile(root, new File([CONF], 'home.conf'))
    expect(byText(root, 'Проверить конфиг').disabled).toBe(false)
    render(null, root)
  })

  it('предпросмотр устарел -- назад к выбору файла', async () => {
    mocks.confirmReply = new ApiError(410, 'preview_expired', 'x')
    const root = await mount()
    await openImport(root)
    await pickFile(root, new File([CONF], 'home.conf'))
    await click(root, 'Проверить конфиг')
    await click(root, 'Добавить как новый')
    expect(root.textContent).toContain('Проверка устарела: с неё прошло больше 5 минут. Выберите файл заново.')
    expect(byText(root, 'Добавить как новый')).toBeFalsy()
    expect(byText(root, 'Проверить конфиг').disabled).toBe(true)
    expect(mocks.api.some((c) => c[0] === 'result')).toBe(false)
    render(null, root)
  })

  it('роутер отверг конфиг при подтверждении -- назад к выбору файла', async () => {
    mocks.confirmReply = new ApiError(409, 'conf_rejected', 'x', 'Роутер не примет этот конфиг: ключ пира повреждён.')
    const root = await mount()
    await openImport(root)
    await pickFile(root, new File([CONF], 'home.conf'))
    await click(root, 'Проверить конфиг')
    await click(root, 'Добавить как новый')
    expect(root.textContent).toContain('Роутер не примет этот конфиг: ключ пира повреждён.')
    expect(byText(root, 'Добавить как новый')).toBeFalsy()
    expect(byText(root, 'Проверить конфиг').disabled).toBe(true)
    render(null, root)
  })

})

// Ревью цикла 4.
describe('загрузка .conf: ревью', () => {
  it('поле файла без фильтра accept: Telegram на телефоне иначе не даёт выбрать .conf', async () => {
    const root = await mount()
    await openImport(root)
    expect(root.querySelector('.conf-pick-input').hasAttribute('accept')).toBe(false)
    render(null, root)
  })

  it('команда ушла, а ожидание сорвалось -- итог «ушла на роутер», а не «попробуйте ещё»', async () => {
    mocks.result = new Error('502')
    const root = await mount()
    await openImport(root)
    await pickFile(root, new File([CONF], 'home.conf'))
    await click(root, 'Проверить конфиг')
    const before = routeStatusCalls()
    await click(root, 'Добавить как новый')
    expect(root.querySelector('.tunnel-outcome').textContent).toBe('Команда ушла на роутер, но он пока не ответил. Загляните в список VPN-туннелей через минуту.')
    expect(byText(root, 'К списку VPN-туннелей')).toBeTruthy()
    expect(byText(root, 'Добавить как новый')).toBeFalsy()
    expect(routeStatusCalls()).toBe(before + 1)
    render(null, root)
  })

  it('двойное касание «Добавить» -- одна команда', async () => {
    const root = await mount()
    await openImport(root)
    await pickFile(root, new File([CONF], 'home.conf'))
    await click(root, 'Проверить конфиг')
    const b = byText(root, 'Добавить как новый')
    await act(async () => {
      b.click()
      b.click()
    })
    await flushMany()
    expect(mocks.api.filter((c) => c[0] === 'confirm')).toHaveLength(1)
    render(null, root)
  })

  it('два выбора подряд -- в проверку уходит последний файл', async () => {
    let releaseFirst
    mocks.reader = (file) =>
      file.name === 'first.conf' ? new Promise((resolve) => (releaseFirst = () => resolve(btoa('first')))) : Promise.resolve(btoa('second'))
    const root = await mount()
    await openImport(root)
    await pickFile(root, new File([CONF], 'first.conf'))
    await pickFile(root, new File([CONF], 'second.conf'))
    await act(async () => releaseFirst())
    await flushMany()
    await click(root, 'Проверить конфиг')
    expect(mocks.api[0][2].confB64).toBe(btoa('second'))
    render(null, root)
  })

  it('отказ проверки -- имя файла стёрто, сказано выбрать заново', async () => {
    mocks.previewReply = new ApiError(500, 'unknown', 'x')
    const root = await mount()
    await openImport(root)
    await pickFile(root, new File([CONF], 'home.conf'))
    await click(root, 'Проверить конфиг')
    expect(root.querySelector('.conf-file-name')).toBe(null)
    expect(root.textContent).toContain('Не получилось. Попробуйте ещё раз. Выберите файл заново.')
    expect(root.querySelector('.conf-pick').textContent).toContain('Выбрать файл .conf')
    render(null, root)
  })

  it('analysis_pending при подтверждении -- проверка ещё идёт, опрос продолжается', async () => {
    mocks.confirmReply = new ApiError(409, 'analysis_pending', 'x')
    mocks.pollReplies = [{ ...structuredClone(PREVIEW), state: 'analyzing', can_confirm: false }, structuredClone(PREVIEW)]
    const root = await mount()
    await openImport(root)
    await pickFile(root, new File([CONF], 'home.conf'))
    await click(root, 'Проверить конфиг')
    await click(root, 'Добавить как новый')
    expect(mocks.api.map((c) => c[0])).toEqual(['preview', 'confirm', 'poll', 'poll'])
    expect(root.querySelector('[role="alert"]')).toBe(null)
    mocks.confirmReply = null
    await click(root, 'Добавить как новый')
    expect(root.querySelector('.tunnel-outcome').textContent).toContain('добавлен')
    render(null, root)
  })

  it('analysis_pending на опросе -- как «ещё проверяет»', async () => {
    mocks.previewReply = { ...structuredClone(PREVIEW), state: 'analyzing', can_confirm: false }
    mocks.pollReplies = [new ApiError(409, 'analysis_pending', 'x'), structuredClone(PREVIEW)]
    const root = await mount()
    await openImport(root)
    await pickFile(root, new File([CONF], 'home.conf'))
    await click(root, 'Проверить конфиг')
    expect(root.querySelector('[role="alert"]')).toBe(null)
    expect(byText(root, 'Добавить как новый').disabled).toBe(false)
    render(null, root)
  })
})

describe('низ «VPN-туннелей» (v0.41, спека C4)', () => {
  it('акцентный переход один -- кабинет; загрузка -- обычная строка списка', async () => {
    const root = await mount({ onOpenCabinet: () => {} })
    const cards = [...root.querySelectorAll('.nav-card')].map((c) => c.querySelector('.nav-card-title').textContent)
    expect(cards).toEqual(['Новый VPN-туннель из кабинета'])
    expect(importCard(root)).toBeTruthy()
    render(null, root)
  })
})

describe('приёмка: вход в загрузку', () => {
  it('подпись карточки -- какие конфиги подходят', async () => {
    const root = await mount()
    expect(importCard(root).querySelector('.list-row-sub').textContent).toBe('WireGuard · AmneziaWG')
    render(null, root)
  })
})
