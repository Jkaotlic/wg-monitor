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
const { Sheet } = await import('../src/ui/Sheet.jsx')

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

async function pickFile(root, name = 'anex.p12', sizeBytes = 3) {
  const input = root.querySelector('#a3-p12')
  const bytes = sizeBytes === 3 ? ['P12'] : [new Uint8Array(sizeBytes)]
  Object.defineProperty(input, 'files', { value: [new File(bytes, name)], configurable: true })
  await act(async () => input.dispatchEvent(new Event('change', { bubbles: true })))
  await flush()
}

async function click(el) {
  await act(async () => el.click())
  await flush()
}

// Лист монтируется отдельно, как его показывает оболочка (SheetHost).
async function openSheetOf(seen) {
  const host = document.createElement('div')
  document.body.appendChild(host)
  let closed = 0
  await act(async () => render(<Sheet sheet={seen.sheets.at(-1)} onClose={() => closed++} />, host))
  return { host, closed: () => closed }
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

  // Правка 3 (ревью раунд 1): нативный input не шлёт change на тот же файл
  // повторно -- сбрасываем его .value после чтения и после отправки, чтобы
  // повторный выбор того же файла снова сработал.
  it('после выбора и после отправки нативный input сброшен -- тот же файл выбирается снова', async () => {
    mocks.saveReply = new ApiError(400, 'invalid_field', 'x', 'Пароль от файла .p12 не подошёл', 'p12_password')
    const root = await mountNode(<Awg3PanelFormScreen onClose={() => {}} openSheet={() => {}} />)
    await pickFile(root)
    expect(root.querySelector('#a3-p12').value).toBe('')
    await fill(root, 'a3-id', 'main')
    await fill(root, 'a3-base_url', 'https://panel.example.com')
    await fill(root, 'a3-user', 'admin')
    await fill(root, 'a3-password', 'pw')
    await click(button(root, 'Сохранить и проверить'))
    expect(root.querySelector('#a3-p12').value).toBe('')
    mocks.saveReply = null
    // Пароль стирается из состояния сразу после любой попытки отправки
    // (решение задачи 9) -- перед повтором его нужно ввести заново, как и в
    // жизни; проверяем именно повторный выбор ТОГО ЖЕ файла .p12.
    await fill(root, 'a3-password', 'pw')
    // Тот же файл ещё раз (в браузере это второй change только благодаря сбросу выше).
    await pickFile(root)
    await click(button(root, 'Сохранить и проверить'))
    expect(calls('create').length).toBe(2)
    expect(calls('create')[1][1].p12_base64).toBe('UDEyLUZJTEU=')
  })

  // Правка 4 (ревью раунд 1): пароли панели никогда не подсказываются
  // браузером как «уже вводили».
  it('пароль панели и пароль .p12 -- autocomplete=new-password', async () => {
    const root = await mountNode(<Awg3PanelFormScreen onClose={() => {}} openSheet={() => {}} />)
    expect(root.querySelector('#a3-password').getAttribute('autocomplete')).toBe('new-password')
    expect(root.querySelector('#a3-p12_password').getAttribute('autocomplete')).toBe('new-password')
  })

  // Ревью раунд 2: невидимый нативный input не должен быть отдельной
  // остановкой Tab перед кнопкой «Выбрать файл .p12»; доступное имя
  // остаётся -- через <label for="a3-p12"> над кнопкой.
  it('невидимый input .p12 не ловит Tab, но подписан label', async () => {
    const root = await mountNode(<Awg3PanelFormScreen onClose={() => {}} openSheet={() => {}} />)
    const input = root.querySelector('#a3-p12')
    expect(input.getAttribute('tabindex')).toBe('-1')
    expect(input.hasAttribute('aria-hidden')).toBe(false)
    expect(root.querySelector('label[for="a3-p12"]')).toBeTruthy()
  })

  // Правка 5 (ревью раунд 1, сужено раундом 3 финального ревью): потолок
  // сведён к бэкендовому 64 КБ (p12.go maxP12Size) -- отклоняется в
  // браузере, не читается и не уходит проверять readFileBase64.
  it('.p12 больше 64 КБ -- отказ словами, файл не читается', async () => {
    const root = await mountNode(<Awg3PanelFormScreen onClose={() => {}} openSheet={() => {}} />)
    await pickFile(root, 'big.p12', 64 * 1024 + 1)
    const field = root.querySelector('#a3-p12').closest('.field')
    expect(field.textContent).toContain('Файл .p12 больше 64 КБ — это не похоже на сертификат.')
    expect(field.textContent).not.toContain('Выбран файл')
  })

  // Правка 6 (ревью раунд 1): русская кнопка вместо «Choose File / No file
  // chosen», имя выбранного файла -- из состояния формы, а не из нативного
  // контрола.
  it('кнопка «Выбрать файл .p12» вместо нативной подписи браузера', async () => {
    const root = await mountNode(<Awg3PanelFormScreen onClose={() => {}} openSheet={() => {}} />)
    expect(button(root, 'Выбрать файл .p12')).toBeTruthy()
    expect(root.textContent).not.toContain('No file chosen')
    await pickFile(root, 'anex.p12')
    expect(root.textContent).toContain('Выбран файл «anex.p12»')
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
  it('интерфейсы вкладками, сводка, пиры ОДНОЙ строкой (без «handshake»), трафик и ярлык роутера', async () => {
    const { root } = await mountPanel()
    expect(root.textContent).toContain('онлайн 1 из 3')
    // Правка 2 (ревью раунд 1): раз, а не на каждой строке -- что значит время.
    expect(root.textContent).toContain('обмена ключами')
    expect(root.textContent).not.toContain('handshake')
    expect(root.querySelectorAll('.segment-tab').length).toBe(2)
    // Правка 1: пир -- ОДИН .data-row, без обёртки .awg3-peer.
    const peers = [...root.querySelectorAll('.awg3-peers > .data-row')]
    expect(peers.length).toBe(3)
    expect(peers[0].textContent).toContain('2 мин назад')
    expect(peers[0].textContent).not.toContain('handshake')
    expect(peers[0].textContent).toContain('↓\u00a01,5\u202fКБ · ↑\u00a02,0\u202fМБ')
    // Ревью раунд 3 (finding 2): в тексте пилюли только ник, полная фраза --
    // в title (проверяется отдельным тестом ниже), иначе фраза «роутер
    // «nick»» вылезает за колонку на узком экране.
    expect(peers[0].textContent).toContain('home')
    expect(peers[0].textContent).not.toContain('роутер «home»')
    expect(peers[1].textContent).toContain('не подключался')
    expect(peers[0].querySelector('.data-row-dot-ok')).toBeTruthy()
    expect(peers[1].querySelector('.data-row-dot-muted')).toBeTruthy()
    expect(peers[2].querySelector('.data-row-dot-warn')).toBeTruthy()
    await click(button(root, 'reserve'))
    expect(calls('peers').map((c) => c[2])).toEqual(['', 'awg2'])
  })

  it('пир выключен без роутера -- «выключен» под именем, своя точка', async () => {
    mocks.page.peers = [{ id: 'p9', name: 'old-ipad', state: 'off', handshake_age_sec: 259200, rx_bytes: 0, tx_bytes: 0, router: null }]
    const { root } = await mountPanel()
    const rows = [...root.querySelectorAll('.awg3-peers > .data-row')]
    expect(rows.length).toBe(1)
    expect(rows[0].textContent).toContain('выключен')
    expect(rows[0].textContent).not.toContain('роутер')
    expect(rows[0].querySelector('.data-row-dot-muted')).toBeTruthy()
  })

  // Ревью раунд 2: пилюля роутера обрезается многоточием в узкой колонке
  // (CSS, проверено измерением в песочнице), а полная фраза остаётся
  // доступной через title -- проверяем здесь структуру, не пиксели.
  // Ревью раунд 3 (финальный, finding 2): полная фраза «роутер «nick»» в
  // САМОМ тексте пилюли всё ещё вылезала за колонку на узком экране (360 px)
  // -- в тексте остаётся только ник, слово «роутер» и ёлочки живут в title.
  it('ярлык роутера у пира -- в .pill-text только ник, полная фраза в title', async () => {
    const { root } = await mountPanel()
    const tag = root.querySelector('.awg3-peer-tag .pill')
    expect(tag.getAttribute('title')).toBe('роутер «home»')
    expect(tag.querySelector('.pill-text').textContent).toBe('home')
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

  it('кнопки выпуска контурные; форма -- листом, лайм только у «Выпустить» внутри', async () => {
    const { root, seen } = await mountPanel()
    expect(button(root, 'Конфиг на устройство').className).toContain('btn-ghost')
    expect(button(root, 'Выпустить на роутер').className).toContain('btn-ghost')
    expect(root.querySelectorAll('.btn-primary')).toHaveLength(0)
    await click(button(root, 'Конфиг на устройство'))
    expect(root.querySelector('#a3-device-name')).toBe(null)
    const { host } = await openSheetOf(seen)
    expect([...host.querySelectorAll('.btn-primary')].map((b) => b.textContent.trim())).toEqual(['Выпустить'])
    render(null, host)
  })

  it('конфиг на устройство: двойное нажатие -- один выпуск, QR на экране, личка словами', async () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem')
    const { root, seen } = await mountPanel()
    await click(button(root, 'Конфиг на устройство'))
    const { host } = await openSheetOf(seen)
    await fill(host, 'sheet-field-name', 'iphone-anex')
    const submit = button(host, 'Выпустить')
    await act(async () => {
      submit.click()
      submit.click()
    })
    await flush()
    await flush()
    expect(calls('device')).toEqual([['device', 'main', 'awg1', 'iphone-anex']])
    expect(root.querySelector('img.awg3-qr').getAttribute('src')).toBe('data:image/png;base64,iVBORw0KGgo=')
    expect(root.querySelector('.awg3-qr-title').textContent).toBe('QR-код «iphone-anex»')
    expect(root.textContent).toContain('Файл .conf и QR отправлены вам в личку.')
    expect(setItem.mock.calls.some((c) => String(c[1]).includes('iVBOR'))).toBe(false)
    setItem.mockRestore()
    render(null, host)
  })

  it('имя устройства проверяется до сервера; отказ сервера -- словами в листе', async () => {
    const { root, seen } = await mountPanel()
    await click(button(root, 'Конфиг на устройство'))
    const { host } = await openSheetOf(seen)
    await fill(host, 'sheet-field-name', 'wgmon-x')
    expect(button(host, 'Выпустить').disabled).toBe(true)
    expect(host.textContent).toContain('роутерам')
    mocks.deviceReply = new ApiError(409, 'awg3_name_taken', 'x', 'Устройство с таким именем уже есть на этом интерфейсе — выберите другое имя')
    await fill(host, 'sheet-field-name', 'laptop')
    await click(button(host, 'Выпустить'))
    await flush()
    expect(host.textContent).toContain('Устройство с таким именем уже есть')
    expect(host.querySelector('#sheet-field-name').value).toBe('laptop')
    expect(root.querySelector('img.awg3-qr')).toBe(null)
    render(null, host)
  })

  it('первый отказ readonly прячет кнопки выпуска', async () => {
    mocks.deviceReply = new ApiError(409, 'awg3_readonly', 'x', '')
    const { root, seen } = await mountPanel()
    await click(button(root, 'Конфиг на устройство'))
    const { host } = await openSheetOf(seen)
    await fill(host, 'sheet-field-name', 'ipad')
    await click(button(host, 'Выпустить'))
    await flush()
    expect(button(root, 'Конфиг на устройство')).toBeFalsy()
    expect(root.textContent).toContain('только для просмотра')
    render(null, host)
  })

  it('выпуск на роутер: выбор в листе, vpn/issue, итог и «Открыть VPN-туннели «ник»»', async () => {
    const opened = []
    const { root, seen } = await mountPanel({ onOpenRouterTunnels: (id) => opened.push(id) })
    await click(button(root, 'Выпустить на роутер'))
    const { host } = await openSheetOf(seen)
    const select = host.querySelector('#sheet-field-router')
    expect([...select.options].map((o) => o.textContent)).toEqual(['Выберите роутер', 'home', 'work'])
    expect(button(host, 'Выпустить').disabled).toBe(true)
    await act(async () => {
      select.value = '9'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })
    expect(host.textContent).toContain('«wgmon-work»')
    await click(button(host, 'Выпустить'))
    await flush()
    await flush()
    expect(calls('router')).toEqual([['router', 9, 'main', 'awg1']])
    expect(root.querySelector('.awg3-outcome-ok').textContent).toContain('Конфиг встал на «work» VPN-туннелем «main_awg1».')
    await click(button(root, 'Открыть VPN-туннели «work»'))
    expect(opened).toEqual([9])
    render(null, host)
  })

  async function issueToWork(root, seen) {
    await click(button(root, 'Выпустить на роутер'))
    const { host } = await openSheetOf(seen)
    await act(async () => {
      const select = host.querySelector('#sheet-field-router')
      select.value = '9'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })
    await click(button(host, 'Выпустить'))
    await flush()
    await flush()
    render(null, host)
  }

  it('«Открыть VPN-туннели» только после успеха; новый лист снимает прошлый итог', async () => {
    mocks.waitReply = { status: 'error', output: 'boom' }
    const { root, seen } = await mountPanel({ onOpenRouterTunnels: () => {} })
    await issueToWork(root, seen)
    expect(root.querySelector('.awg3-outcome-error')).toBeTruthy()
    expect(button(root, 'Открыть VPN-туннели «work»')).toBeFalsy()
    mocks.waitReply = { status: 'ok' }
    await issueToWork(root, seen)
    expect(button(root, 'Открыть VPN-туннели «work»')).toBeTruthy()
    await click(button(root, 'Конфиг на устройство'))
    expect(root.querySelector('.awg3-outcome')).toBe(null)
    expect(button(root, 'Открыть VPN-туннели «work»')).toBeFalsy()
  })

  it('QR не переживает уход с экрана', async () => {
    let { root, seen } = await mountPanel()
    await click(button(root, 'Конфиг на устройство'))
    const { host } = await openSheetOf(seen)
    await fill(host, 'sheet-field-name', 'ipad')
    await click(button(host, 'Выпустить'))
    await flush()
    expect(root.querySelector('img.awg3-qr')).toBeTruthy()
    render(null, host)
    render(null, root)
    ;({ root } = await mountPanel())
    expect(root.querySelector('img.awg3-qr')).toBe(null)
  })

  it('QR переживает неудачный автоповтор чтения страницы после выпуска', async () => {
    const { root, seen } = await mountPanel()
    await click(button(root, 'Конфиг на устройство'))
    const { host } = await openSheetOf(seen)
    await fill(host, 'sheet-field-name', 'ipad')
    mocks.peersErr = new ApiError(502, 'awg3_unreachable', 'x', '')
    await click(button(host, 'Выпустить'))
    await flush()
    expect(root.querySelector('img.awg3-qr')).toBeTruthy()
    expect(root.querySelector('.awg3-banner')).toBeTruthy()
    render(null, host)
  })
})
