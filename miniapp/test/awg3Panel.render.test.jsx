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
  page: null,
  peersErr: null,
  deviceReply: null,
  routerReply: null,
  waitReply: { status: 'ok' },
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
    fetchAwg3Peers: (id, iface) => {
      log('peers', id, iface)
      return mocks.peersErr ? Promise.reject(mocks.peersErr) : Promise.resolve({ ...structuredClone(mocks.page), iface: iface || mocks.page.iface })
    },
    issueAwg3Device: (id, iface, name) => {
      log('device', id, iface, name)
      return reply(mocks.deviceReply, { name, address: '10.66.0.9/32', qr_png_base64: 'iVBORw0KGgo=', dm: 'sent' })
    },
    issueAwg3ToRouter: (routerID, id, iface) => {
      log('router', routerID, id, iface)
      return reply(mocks.routerReply, { cmd_id: 'c1', tunnel_name: `${id}_${iface}` })
    },
  }
})

vi.mock('../src/awg3Panel.js', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, readFileBase64: vi.fn(async () => 'UDEyLUZJTEU=') }
})

vi.mock('../src/commandWait.js', async (importOriginal) => {
  const real = await importOriginal()
  return { ...real, waitCommand: vi.fn(async () => mocks.waitReply) }
})

const { SelfhostedScreen } = await import('../src/screens/SelfhostedScreen.jsx')
const { Awg3PanelFormScreen } = await import('../src/screens/Awg3PanelFormScreen.jsx')
const { Awg3PanelScreen } = await import('../src/screens/Awg3PanelScreen.jsx')
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

const PAGE = {
  panel: { ...MAIN },
  ifaces: [{ id: 'awg1', title: 'main', interface: 'awg1' }, { id: 'awg2', title: 'reserve', interface: 'awg2' }],
  iface: 'awg1',
  summary: { peers_total: 3, peers_online: 1, peers_stale: 0, peers_never: 1, rx_bytes: 0, tx_bytes: 0 },
  peers: [
    { id: 'p1', name: 'wgmon-home', address: '10.66.0.2/32', enabled: true, state: 'online', handshake_age_sec: 125, rx_bytes: 1536, tx_bytes: 2097152, router: { id: 7, nickname: 'home' } },
    { id: 'p2', name: 'laptop', address: '10.66.0.3/32', enabled: true, state: 'never', handshake_age_sec: -1, rx_bytes: 0, tx_bytes: 0, router: null },
    { id: 'p3', name: 'tablet', address: '10.66.0.4/32', enabled: true, state: 'idle', handshake_age_sec: 7200, rx_bytes: 10, tx_bytes: 20, router: null },
  ],
  fetched_at: '2026-09-29T10:00:00Z',
}
const ROUTERS = [{ id: 9, nickname: 'work', status: 'online' }, { id: 7, nickname: 'home', status: 'online' }]

async function mountPanel(over = {}) {
  const seen = { sheets: [], edits: [] }
  const root = await mountNode(
    <Awg3PanelScreen panelId="main" routers={ROUTERS} onClose={() => {}} onEdit={(id) => seen.edits.push(id)} openSheet={(s) => seen.sheets.push(s)} {...over} />,
  )
  return { root, seen }
}

beforeEach(() => {
  mocks.panels = [structuredClone(MAIN), { id: 'nl2', label: 'nl2', base_url: 'https://203.0.113.5:8444', state: 'bad_password', password_set: true, cert_set: true }]
  mocks.panelsErr = null
  mocks.selfhosted = []
  mocks.calls = []
  mocks.saveReply = null
  mocks.deleteReply = null
  mocks.page = structuredClone(PAGE)
  mocks.peersErr = null
  mocks.deviceReply = null
  mocks.routerReply = null
  mocks.waitReply = { status: 'ok' }
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

describe('экран панели', () => {
  it('интерфейсы вкладками, сводка, пиры с точками, handshake, трафик и ярлык роутера', async () => {
    const { root } = await mountPanel()
    expect(root.textContent).toContain('онлайн 1 из 3')
    expect(root.querySelectorAll('.segment-tab').length).toBe(2)
    const peers = [...root.querySelectorAll('.awg3-peer')]
    expect(peers.length).toBe(3)
    expect(peers[0].textContent).toContain('handshake 2 мин назад')
    expect(peers[0].textContent).toContain('↓ 1,5 КБ · ↑ 2,0 МБ')
    expect(peers[0].textContent).toContain('роутер «home»')
    expect(peers[1].textContent).toContain('не подключался')
    expect(peers[0].querySelector('.data-row-dot-ok')).toBeTruthy()
    expect(peers[1].querySelector('.data-row-dot-muted')).toBeTruthy()
    expect(peers[2].querySelector('.data-row-dot-warn')).toBeTruthy()
    await click(button(root, 'reserve'))
    expect(calls('peers').map((c) => c[2])).toEqual(['', 'awg2'])
  })

  it('состояния спеки: пароль, пауза ЧЧ:ММ, сертификат, недоступна с повтором', async () => {
    const retry = new Date(2026, 8, 29, 14, 5).toISOString()
    const cases = [
      [new ApiError(409, 'awg3_bad_password', 'x', ''), 'пересохраните'],
      [new ApiError(409, 'awg3_paused', 'x', '', '', { retry_at: retry }), 'Панель ограничила вход, повтор после 14:05.'],
      [new ApiError(409, 'awg3_cert_rejected', 'x', ''), 'Сертификат не принят'],
      [new ApiError(502, 'awg3_unreachable', 'x', ''), 'Панель недоступна'],
    ]
    for (const [err, text] of cases) {
      mocks.peersErr = err
      mocks.calls = []
      const { root, seen } = await mountPanel()
      expect(root.querySelector('.awg3-banner').textContent).toContain(text)
      expect(button(root, 'Конфиг на устройство')).toBeFalsy()
      if (err.code === 'awg3_unreachable') {
        mocks.peersErr = null
        await click(button(root, 'Повторить'))
        expect(calls('peers').length).toBe(2)
        expect(root.textContent).toContain('онлайн 1 из 3')
      }
      await click(button(root, 'Настройки панели'))
      expect(seen.edits).toEqual(['main'])
      render(null, root)
    }
  })

  it('readonly: кнопок выпуска нет, строка «только для просмотра»', async () => {
    mocks.page.panel.readonly = true
    const { root } = await mountPanel()
    expect(button(root, 'Конфиг на устройство')).toBeFalsy()
    expect(button(root, 'Выпустить на роутер')).toBeFalsy()
    expect(root.textContent).toContain('только для просмотра')
  })

  it('конфиг на устройство: двойное нажатие -- один выпуск, QR на экране, личка словами', async () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem')
    const { root } = await mountPanel()
    await click(button(root, 'Конфиг на устройство'))
    await fill(root, 'a3-device-name', 'iphone-anex')
    const submit = button(root, 'Выпустить')
    await act(async () => {
      submit.click()
      submit.click()
    })
    await flush()
    await flush()
    expect(calls('device')).toEqual([['device', 'main', 'awg1', 'iphone-anex']])
    expect(root.querySelector('img.awg3-qr').getAttribute('src')).toBe('data:image/png;base64,iVBORw0KGgo=')
    expect(root.textContent).toContain('Файл .conf и QR отправлены вам в личку.')
    expect(calls('peers').length).toBe(2)
    expect(setItem.mock.calls.some((c) => String(c[1]).includes('iVBOR'))).toBe(false)
    setItem.mockRestore()
  })

  it('имя устройства проверяется до сервера; отказ сервера -- словами', async () => {
    const { root } = await mountPanel()
    await click(button(root, 'Конфиг на устройство'))
    await fill(root, 'a3-device-name', 'wgmon-x')
    await click(button(root, 'Выпустить'))
    expect(calls('device').length).toBe(0)
    expect(root.textContent).toContain('роутерам')
    mocks.deviceReply = new ApiError(409, 'awg3_name_taken', 'x', 'Устройство с таким именем уже есть на этом интерфейсе — выберите другое имя')
    await fill(root, 'a3-device-name', 'laptop')
    await click(button(root, 'Выпустить'))
    expect(root.textContent).toContain('Устройство с таким именем уже есть')
    expect(root.querySelector('img.awg3-qr')).toBe(null)
  })

  it('первый отказ readonly прячет кнопки выпуска', async () => {
    mocks.deviceReply = new ApiError(409, 'awg3_readonly', 'x', '')
    const { root } = await mountPanel()
    await click(button(root, 'Конфиг на устройство'))
    await fill(root, 'a3-device-name', 'ipad')
    await click(button(root, 'Выпустить'))
    expect(button(root, 'Конфиг на устройство')).toBeFalsy()
    expect(root.textContent).toContain('только для просмотра')
  })

  it('выпуск на роутер: выбор, лист, vpn/issue, ожидание итога', async () => {
    const { root, seen } = await mountPanel()
    await click(button(root, 'Выпустить на роутер'))
    const rows = [...root.querySelectorAll('.awg3-routers .list-row-btn')]
    expect(rows.map((r) => r.textContent)).toEqual([expect.stringContaining('уже есть'), expect.stringContaining('«wgmon-work»')])
    await click(rows[1])
    const sheet = seen.sheets[0]
    expect(sheet.title).toBe('Выпустить на «work»?')
    const resp = await sheet.perform()
    expect(calls('router')).toEqual([['router', 9, 'main', 'awg1']])
    await act(async () => {
      await sheet.onDone(resp)
    })
    await flush()
    expect(root.querySelector('.awg3-outcome-ok').textContent).toContain('Конфиг встал на «work» VPN-туннелем «main_awg1».')
  })

  it('QR не переживает уход с экрана', async () => {
    let { root } = await mountPanel()
    await click(button(root, 'Конфиг на устройство'))
    await fill(root, 'a3-device-name', 'ipad')
    await click(button(root, 'Выпустить'))
    expect(root.querySelector('img.awg3-qr')).toBeTruthy()
    render(null, root)
    ;({ root } = await mountPanel())
    expect(root.querySelector('img.awg3-qr')).toBe(null)
  })
})
