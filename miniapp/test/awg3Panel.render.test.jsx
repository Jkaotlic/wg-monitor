// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({
  panels: [],
  panelsErr: null,
  selfhosted: [],
  calls: [],
  saveReply: null,
  deleteReply: null,
}))

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  const reply = (v, fallback) => (v instanceof Error ? Promise.reject(v) : Promise.resolve(v ?? fallback))
  const log = (...args) => mocks.calls.push(structuredClone(args))
  return {
    ...real,
    fetchSelfhosted: () => Promise.resolve({ instances: structuredClone(mocks.selfhosted), defaults: null }),
    fetchAwg3Panels: () => {
      log('list')
      return mocks.panelsErr ? Promise.reject(mocks.panelsErr) : Promise.resolve({ panels: structuredClone(mocks.panels) })
    },
    createAwg3Panel: (body) => {
      log('create', body)
      return reply(mocks.saveReply, { panel: { id: body.id, label: body.label, base_url: body.base_url, user: body.user, password_set: true, cert_set: true, state: 'ok' }, check: { ran: true, ok: true, message: 'Панель ответила: интерфейсов — 2.' } })
    },
    updateAwg3Panel: (id, body) => {
      log('update', id, body)
      return reply(mocks.saveReply, { panel: { ...mocks.panels[0], ...body }, check: null })
    },
    deleteAwg3Panel: (id, confirm) => {
      log('delete', id, confirm)
      return reply(mocks.deleteReply, null)
    },
  }
})

vi.mock('../src/awg3Panel.js', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, readFileBase64: vi.fn(async () => 'UDEyLUZJTEU=') }
})

const { SelfhostedScreen } = await import('../src/screens/SelfhostedScreen.jsx')
const { Awg3PanelFormScreen } = await import('../src/screens/Awg3PanelFormScreen.jsx')
const { ApiError } = await import('../src/api.js')

const MAIN = { id: 'main', label: 'Main', base_url: 'https://panel.example.com', user: 'admin', enabled: true, password_set: true, cert_set: true, cert_subject: 'anex', cert_not_after: '2028-11-26T00:00:00Z', state: 'ok', readonly: false }

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)
const calls = (name) => mocks.calls.filter((c) => c[0] === name)

async function mountNode(node) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(node, root))
  await flush()
  await flush()
  return root
}

async function fill(root, id, value) {
  const el = root.querySelector(`#${id}`)
  await act(async () => {
    el.value = value
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function pickFile(root, name = 'anex.p12') {
  const input = root.querySelector('#a3-p12')
  Object.defineProperty(input, 'files', { value: [new File(['P12'], name)], configurable: true })
  await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })))
  await flush()
}

async function click(el) {
  await act(async () => el.click())
  await flush()
}

beforeEach(() => {
  mocks.panels = [structuredClone(MAIN), { id: 'nl2', label: 'nl2', base_url: 'https://203.0.113.5:8444', state: 'bad_password', password_set: true, cert_set: true }]
  mocks.panelsErr = null
  mocks.selfhosted = []
  mocks.calls = []
  mocks.saveReply = null
  mocks.deleteReply = null
})

describe('«Свои VPN-серверы»: группа «Панели awg3»', () => {
  it('строки панелей с состоянием, открытие и «Добавить панель»', async () => {
    const opened = []
    let added = 0
    const root = await mountNode(<SelfhostedScreen onClose={() => {}} onOpenInstance={() => {}} onOpenAwg3={(id) => opened.push(id)} onAddAwg3={() => added++} />)
    expect(root.textContent).toContain('Панели awg3')
    const rows = [...root.querySelectorAll('.awg3-list .list-row-btn')]
    expect(rows.map((r) => r.textContent)).toEqual([expect.stringContaining('panel.example.com'), expect.stringContaining('неверный пароль')])
    await click(rows[0])
    await click(button(root, 'Добавить панель'))
    expect(opened).toEqual(['main'])
    expect(added).toBe(1)
  })

  it('без onOpenAwg3 группы нет и панели не спрашиваются', async () => {
    const root = await mountNode(<SelfhostedScreen onClose={() => {}} onOpenInstance={() => {}} />)
    expect(root.textContent).not.toContain('Панели awg3')
    expect(calls('list').length).toBe(0)
  })

  it('отказ списка панелей не ломает свои серверы', async () => {
    mocks.panelsErr = new ApiError(503, 'awg3_not_configured', 'x', 'Панели awg3 на этом сервере не настроены')
    const root = await mountNode(<SelfhostedScreen onClose={() => {}} onOpenInstance={() => {}} onOpenAwg3={() => {}} onAddAwg3={() => {}} />)
    expect(root.textContent).toContain('Панели awg3 на этом сервере не настроены.')
    expect(button(root, 'Добавить сервер')).toBeTruthy()
    expect(button(root, 'Добавить панель')).toBeFalsy()
  })
})

describe('форма панели', () => {
  it('новая: все поля, .p12, одна отправка; секреты стёрты до ответа', async () => {
    const root = await mountNode(<Awg3PanelFormScreen onClose={() => {}} openSheet={() => {}} />)
    await fill(root, 'a3-label', 'Main')
    await fill(root, 'a3-id', 'main')
    await fill(root, 'a3-base_url', 'https://panel.example.com')
    await fill(root, 'a3-user', 'admin')
    await fill(root, 'a3-password', 'PANEL-PW')
    await pickFile(root)
    await fill(root, 'a3-p12_password', 'P12-PW')
    expect(root.textContent).toContain('Выбран файл «anex.p12»')
    await click(button(root, 'Сохранить и проверить'))
    expect(calls('create')).toEqual([['create', { id: 'main', label: 'Main', base_url: 'https://panel.example.com', user: 'admin', password: 'PANEL-PW', p12_base64: 'UDEyLUZJTEU=', p12_password: 'P12-PW' }]])
    expect(root.querySelector('#a3-password').value).toBe('')
    expect(root.querySelector('#a3-p12_password').value).toBe('')
    expect(root.textContent).toContain('Панель ответила: интерфейсов — 2.')
    // После создания экран -- правка той же панели: повторное сохранение не создаёт вторую.
    await fill(root, 'a3-label', 'Main 2')
    await click(button(root, 'Сохранить'))
    expect(calls('create').length).toBe(1)
    expect(calls('update')[0][1]).toBe('main')
  })

  it('без файла .p12 -- отказ до сервера', async () => {
    const root = await mountNode(<Awg3PanelFormScreen onClose={() => {}} openSheet={() => {}} />)
    await fill(root, 'a3-id', 'main')
    await fill(root, 'a3-base_url', 'https://panel.example.com')
    await fill(root, 'a3-user', 'admin')
    await fill(root, 'a3-password', 'pw')
    await click(button(root, 'Сохранить и проверить'))
    expect(calls('create').length).toBe(0)
    expect(root.textContent).toContain('Выберите файл .p12')
  })

  it('сервер отверг пароль .p12 -- слова под этим полем', async () => {
    mocks.saveReply = new ApiError(400, 'invalid_field', 'x', 'Пароль от файла .p12 не подошёл', 'p12_password')
    const root = await mountNode(<Awg3PanelFormScreen onClose={() => {}} openSheet={() => {}} />)
    await fill(root, 'a3-id', 'main')
    await fill(root, 'a3-base_url', 'https://panel.example.com')
    await fill(root, 'a3-user', 'admin')
    await fill(root, 'a3-password', 'pw')
    await pickFile(root)
    await click(button(root, 'Сохранить и проверить'))
    const field = root.querySelector('#a3-p12_password').closest('.field')
    expect(field.classList.contains('field-error')).toBe(true)
    expect(field.textContent).toContain('Пароль от файла .p12 не подошёл')
  })

  it('проверка не прошла -- панель сохранена, слова про пароль', async () => {
    mocks.saveReply = { panel: { ...MAIN, state: 'bad_password' }, check: { ran: true, ok: false, code: 'awg3_bad_password', message: 'x' } }
    const root = await mountNode(<Awg3PanelFormScreen onClose={() => {}} openSheet={() => {}} />)
    await fill(root, 'a3-id', 'main')
    await fill(root, 'a3-base_url', 'https://panel.example.com')
    await fill(root, 'a3-user', 'admin')
    await fill(root, 'a3-password', 'wrong')
    await pickFile(root)
    await click(button(root, 'Сохранить и проверить'))
    expect(root.querySelector('.awg3-check-bad').textContent).toContain('пересохраните')
  })

  it('правка: без пароля и файла в теле, подсказки про сохранённое', async () => {
    const root = await mountNode(<Awg3PanelFormScreen panelId="main" onClose={() => {}} openSheet={() => {}} />)
    expect(root.querySelector('#a3-id')).toBe(null)
    expect(root.textContent).toContain('Пароль задан')
    expect(root.textContent).toContain('Сертификат «anex»')
    await fill(root, 'a3-label', 'Main 2')
    await click(button(root, 'Сохранить'))
    expect(calls('update')).toEqual([['update', 'main', { label: 'Main 2', base_url: 'https://panel.example.com', user: 'admin' }]])
    expect(root.textContent).toContain('Сохранено.')
  })

  it('удаление -- лист с набором названия', async () => {
    const sheets = []
    let deleted = 0
    const root = await mountNode(<Awg3PanelFormScreen panelId="main" onClose={() => {}} onDeleted={() => deleted++} openSheet={(s) => sheets.push(s)} />)
    await click(button(root, 'Удалить панель'))
    expect(sheets[0]).toMatchObject({ title: 'Удалить панель «Main»?', confirmPhrase: 'Main', danger: true })
    await sheets[0].perform('Main')
    sheets[0].onDone()
    expect(calls('delete')).toEqual([['delete', 'main', 'Main']])
    expect(deleted).toBe(1)
  })
})
