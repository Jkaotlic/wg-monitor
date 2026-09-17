// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({
  cabinets: null,
  accounts: null,
  role: 'owner',
  instances: [],
  calls: [],
  addReply: null,
  deleteReply: null,
  activeReply: null,
  revokeReply: null,
  issueReply: null,
  result: null,
  sendReply: null,
}))

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  const reply = (v, fallback) => (v instanceof Error ? Promise.reject(v) : Promise.resolve(v ?? fallback))
  const log = (...args) => mocks.calls.push(args)
  return {
    ...real,
    fetchCabinets: (id) => {
      log('cabinets', id)
      if (mocks.cabinetsFail) return Promise.reject(mocks.cabinetsFail)
      return Promise.resolve(structuredClone(mocks.cabinets))
    },
    fetchVPNAccounts: (id) => {
      log('vpn', id)
      return Promise.resolve({ accounts: structuredClone(mocks.accounts) })
    },
    fetchRouterSettings: () => {
      log('settings')
      return mocks.settingsFail ? Promise.reject(new Error('net')) : Promise.resolve({ role: mocks.role })
    },
    fetchSelfhosted: () => {
      log('selfhosted')
      return Promise.resolve({ instances: structuredClone(mocks.instances) })
    },
    addCabinetSecret: (id, kind, secret, label) => {
      log('add', id, kind, secret, label)
      return reply(mocks.addReply, { id: 'k9', mask: 'zz99' })
    },
    setCabinetActive: (id, kind, keyID) => {
      log('active', id, kind, keyID)
      return reply(mocks.activeReply, null)
    },
    deleteCabinetSecret: (id, kind, keyID) => {
      log('delete', id, kind, keyID)
      return reply(mocks.deleteReply, null)
    },
    revokeAmneziaSlot: (id, country, confirm) => {
      log('revoke', id, country, confirm)
      return reply(mocks.revokeReply, { ok: true })
    },
    issueVPNConfig: (id, provider, option, instance) => {
      log('issue', id, provider, option, instance)
      if (mocks.issueReply === 'hang') return new Promise(() => {})
      return reply(mocks.issueReply, { cmd_id: 'c1', tunnel_name: 'amnezia_de' })
    },
    fetchCommandResult: () => Promise.resolve(mocks.result ?? { status: 'ok', output: '' }),
    sendVPNConf: (id, body) => {
      log('send', id, body)
      return reply(mocks.sendReply, { sent_to: 'dm' })
    },
  }
})

const { CabinetScreen } = await import('../src/screens/CabinetScreen.jsx')
const { Sheet } = await import('../src/ui/Sheet.jsx')
const { ApiError } = await import('../src/api.js')

const CABINETS = {
  amnezia: {
    keys: [
      { id: 'k2', label: '', mask: 'c3d4', active: false },
      { id: 'k1', label: 'основной', mask: 'a1b2', active: true },
    ],
  },
  hidemy: { codes: [] },
  selfhosted: { available: false },
}

const ACCOUNTS = [
  {
    provider: 'amnezia',
    label: 'Amnezia Premium',
    connected: true,
    devices_used: 1,
    devices_max: 3,
    options: [
      { id: 'nl', label: 'Нидерланды', issued: true },
      { id: 'de', label: 'Германия' },
    ],
  },
  {
    provider: 'hidemyname',
    label: 'HideMy.name',
    connected: false,
    note: 'Код доступа не сохранён. Отправьте его боту в теме роутера — приложение коды не спрашивает.',
  },
]

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const buttons = (root, text) => [...root.querySelectorAll('button')].filter((b) => b.textContent.trim() === text)
const button = (root, text) => buttons(root, text)[0]
const cleanup = (root) => { render(null, root); root.remove() }
const calls = (name) => mocks.calls.filter((c) => c[0] === name)

async function mount() {
  const sheets = []
  const seen = { closed: 0, issued: 0 }
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => {
    render(
      <CabinetScreen
        routerID={7}
        routerName="dacha-1"
        asleep={false}
        openSheet={(s) => sheets.push(s)}
        onClose={() => seen.closed++}
        onIssued={() => seen.issued++}
      />,
      root,
    )
  })
  await flush()
  await flush()
  await flush()
  return { root, sheets, seen }
}

async function mountSheet(sheet) {
  const root = document.createElement('div')
  document.body.appendChild(root)
  await act(async () => render(<Sheet sheet={sheet} asleep={false} onClose={() => {}} />, root))
  return root
}

async function fill(root, id, value) {
  const el = root.querySelector(`#${id}`)
  await act(async () => {
    el.value = value
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function submit(sheetRoot) {
  await act(async () => [...sheetRoot.querySelectorAll('.sheet-actions button')].pop().click())
  await flush()
  await flush()
}

async function tab(root, title) {
  await act(async () => [...root.querySelectorAll('.segment-tab')].find((b) => b.textContent === title).click())
  await flush()
}

async function pickOption(root, label) {
  await act(async () => [...root.querySelectorAll('.cabinet-option-main')].find((b) => b.textContent.includes(label)).click())
  await flush()
}

beforeEach(() => {
  mocks.cabinets = structuredClone(CABINETS)
  mocks.accounts = structuredClone(ACCOUNTS)
  mocks.role = 'owner'
  mocks.settingsFail = false
  mocks.cabinetsFail = null
  mocks.instances = []
  mocks.calls = []
  mocks.addReply = null
  mocks.deleteReply = null
  mocks.activeReply = null
  mocks.revokeReply = null
  mocks.issueReply = null
  mocks.result = null
  mocks.sendReply = null
})

describe('кабинет роутера: вкладки и ключи', () => {
  it('вкладки Amnezia и HideMy; своего сервера нет, список серверов не спрашивается', async () => {
    const { root } = await mount()
    expect(root.querySelector('.overlay-title').textContent).toBe('Кабинеты VPN «dacha-1»')
    const tabs = [...root.querySelectorAll('.segment-tab')]
    expect(tabs.map((t) => t.textContent)).toEqual(['Amnezia', 'HideMy'])
    expect(tabs[0].getAttribute('aria-selected')).toBe('true')
    expect(calls('selfhosted')).toEqual([])
    cleanup(root)
  })

  it('админу сервер разрешил -- третья вкладка и список серверов', async () => {
    mocks.cabinets.selfhosted.available = true
    mocks.role = 'admin'
    const { root } = await mount()
    expect([...root.querySelectorAll('.segment-tab')].map((t) => t.textContent)).toEqual(['Amnezia', 'HideMy', 'Свой сервер'])
    expect(calls('selfhosted')).toHaveLength(1)
    cleanup(root)
  })

  it('ключи с маской, активный первым; владельцу -- все кнопки', async () => {
    const { root } = await mount()
    const rows = [...root.querySelectorAll('.cabinet-secret')]
    expect(rows.map((r) => r.querySelector('.row-title').textContent)).toEqual(['основной', 'Ключ без подписи'])
    expect(rows[0].querySelector('.list-row-sub').textContent).toBe('ключ …a1b2 · активный')
    expect(rows[0].classList.contains('cabinet-secret-active')).toBe(true)
    expect(rows[1].querySelector('.list-row-sub').textContent).toBe('ключ …c3d4')
    expect(buttons(root, 'Сделать активным')).toHaveLength(1)
    expect(buttons(root, 'Удалить')).toHaveLength(2)
    expect(button(root, 'Добавить ключ')).toBeTruthy()
    expect(buttons(root, 'Отозвать')).toHaveLength(1)
    cleanup(root)
  })

  it('оператор: добавить и сделать активным -- да; удалить, отозвать -- нет', async () => {
    mocks.role = 'operator'
    const { root } = await mount()
    expect(button(root, 'Добавить ключ')).toBeTruthy()
    expect(buttons(root, 'Сделать активным')).toHaveLength(1)
    expect(buttons(root, 'Удалить')).toHaveLength(0)
    expect(buttons(root, 'Отозвать')).toHaveLength(0)
    cleanup(root)
  })

  it('добавить ключ: поле-пароль new-password, секрет только в запросе и стирается, список перечитан', async () => {
    const { root, sheets } = await mount()
    await act(async () => button(root, 'Добавить ключ').click())
    expect(sheets).toHaveLength(1)
    const sheet = sheets[0]
    expect(sheet.title).toBe('Добавить ключ Amnezia Premium')
    const sheetRoot = await mountSheet(sheet)
    const input = sheetRoot.querySelector('#sheet-field-secret')
    expect(input.type).toBe('password')
    expect(input.getAttribute('autocomplete')).toBe('new-password')
    await fill(sheetRoot, 'sheet-field-secret', 'vpn://SECRET-KEY')
    await fill(sheetRoot, 'sheet-field-label', 'запасной')
    await submit(sheetRoot)
    expect(calls('add')).toEqual([['add', 7, 'amnezia', 'vpn://SECRET-KEY', 'запасной']])
    expect(sheetRoot.querySelector('#sheet-field-secret').value).toBe('')
    expect(JSON.stringify(sheet)).not.toContain('SECRET-KEY')
    await flush()
    await flush()
    expect(calls('cabinets')).toHaveLength(2)
    expect(root.querySelector('.cabinet-notice').textContent).toBe('Ключ сохранён.')
    expect(root.textContent).not.toContain('SECRET-KEY')
    cleanup(sheetRoot)
    cleanup(root)
  })

  it('кабинет отказал -- его слова на листе, секрет стёрт, подпись осталась', async () => {
    mocks.addReply = new ApiError(422, 'cabinet_rejected', 'x', 'Кабинет не принял ключ: подписка истекла')
    const { root, sheets } = await mount()
    await act(async () => button(root, 'Добавить ключ').click())
    const sheetRoot = await mountSheet(sheets[0])
    await fill(sheetRoot, 'sheet-field-secret', 'vpn://BAD')
    await fill(sheetRoot, 'sheet-field-label', 'запасной')
    await submit(sheetRoot)
    expect(sheetRoot.textContent).toContain('Кабинет не принял ключ: подписка истекла')
    expect(sheetRoot.querySelector('#sheet-field-secret').value).toBe('')
    expect(sheetRoot.querySelector('#sheet-field-label').value).toBe('запасной')
    expect(calls('cabinets')).toHaveLength(1)
    cleanup(sheetRoot)
    cleanup(root)
  })

  it('сделать активным -- запрос, итог, список перечитан', async () => {
    const { root } = await mount()
    await act(async () => button(root, 'Сделать активным').click())
    await flush()
    await flush()
    expect(calls('active')).toEqual([['active', 7, 'amnezia', 'k2']])
    expect(root.querySelector('.cabinet-notice').textContent).toBe('Активный ключ — «Ключ без подписи».')
    expect(calls('cabinets')).toHaveLength(2)
    cleanup(root)
  })

  it('удалить -- подтверждение без набора', async () => {
    const { root, sheets } = await mount()
    await act(async () => buttons(root, 'Удалить')[0].click())
    expect(sheets[0]).toMatchObject({ title: 'Удалить ключ «основной»?', danger: true, confirmPhrase: '' })
    const sheetRoot = await mountSheet(sheets[0])
    await submit(sheetRoot)
    expect(calls('delete')).toEqual([['delete', 7, 'amnezia', 'k1']])
    await flush()
    expect(root.querySelector('.cabinet-notice').textContent).toBe('Ключ удалён.')
    cleanup(sheetRoot)
    cleanup(root)
  })

  it('HideMy без кодов -- просьба добавить код, слов про бота нет, стран нет', async () => {
    const { root } = await mount()
    await tab(root, 'HideMy')
    expect(root.textContent).toContain('Код доступа HideMy.name ещё не добавлен.')
    expect(root.textContent).not.toMatch(/боту/)
    expect(button(root, 'Добавить код')).toBeTruthy()
    expect(root.querySelector('.cabinet-options')).toBe(null)
    cleanup(root)
  })
})

describe('кабинет роутера: страны и отзыв', () => {
  it('отозвать выпущенную страну -- набор имени роутера', async () => {
    const { root, sheets } = await mount()
    const nl = [...root.querySelectorAll('.cabinet-option')].find((li) => li.textContent.includes('Нидерланды'))
    await act(async () => nl.querySelector('.cabinet-danger').click())
    expect(sheets[0]).toMatchObject({ title: 'Отозвать «Нидерланды»?', danger: true, confirmPhrase: 'dacha-1' })
    const sheetRoot = await mountSheet(sheets[0])
    await fill(sheetRoot, 'sheet-confirm-input', 'dacha-1')
    await submit(sheetRoot)
    expect(calls('revoke')).toEqual([['revoke', 7, 'nl', 'dacha-1']])
    await flush()
    expect(root.querySelector('.cabinet-notice').textContent).toBe('Конфиг «Нидерланды» отозван, место в подписке свободно.')
    cleanup(sheetRoot)
    cleanup(root)
  })

  it('мест нет -- новые страны закрыты, выпущенная активна, «Отозвать» есть', async () => {
    mocks.accounts[0].devices_used = 3
    const { root } = await mount()
    expect(root.textContent).toContain('выпуск новых стран закрыт')
    expect(root.textContent).toContain('Освободите место — отзовите одну из выпущенных стран ниже')
    const main = (label) => [...root.querySelectorAll('.cabinet-option-main')].find((b) => b.textContent.includes(label))
    expect(main('Германия').disabled).toBe(true)
    expect(main('Нидерланды').disabled).toBe(false)
    expect(buttons(root, 'Отозвать')).toHaveLength(1)
    cleanup(root)
  })

  it('мест нет: выпущенную страну можно выпустить заново и прислать .conf', async () => {
    mocks.accounts[0].devices_used = 3
    const { root, sheets } = await mount()
    await pickOption(root, 'Нидерланды')
    expect(button(root, 'Выпустить и положить на роутер')).toBeTruthy()
    await act(async () => button(root, 'Прислать .conf в личку').click())
    expect(sheets[0].title).toBe('Прислать .conf в личку?')
    await act(async () => button(root, 'Выпустить и положить на роутер').click())
    await flush()
    await flush()
    expect(calls('issue')).toEqual([['issue', 7, 'amnezia', 'nl', '']])
    cleanup(root)
  })
})

describe('кабинет роутера: выпуск', () => {
  it('успех: выбор, «назад» к списку, выпуск и итог', async () => {
    const { root, seen } = await mount()
    await pickOption(root, 'Германия')
    expect(root.querySelector('.overlay-back').textContent).toContain('Назад')
    await act(async () => button(root, 'Выпустить и положить на роутер').click())
    await flush()
    await flush()
    expect(calls('issue')).toEqual([['issue', 7, 'amnezia', 'de', '']])
    expect(root.textContent).toContain('Конфиг выпущен и импортирован как «amnezia_de»')
    expect(seen.issued).toBe(1)
    await act(async () => root.querySelector('.overlay-back').click())
    expect(root.querySelector('.segment-tabs')).toBeTruthy()
    expect(seen.closed).toBe(0)
    await act(async () => root.querySelector('.overlay-back').click())
    expect(seen.closed).toBe(1)
    cleanup(root)
  })

  it('slot_busy -- слова и возврат к списку, где можно отозвать', async () => {
    mocks.issueReply = new ApiError(409, 'slot_busy', 'x')
    const { root } = await mount()
    await pickOption(root, 'Германия')
    await act(async () => button(root, 'Выпустить и положить на роутер').click())
    await flush()
    expect(root.querySelector('.cabinet-outcome').textContent).toBe('Свободных мест в подписке нет. Отзовите одну из выпущенных стран — и выпуск пройдёт.')
    await act(async () => button(root, 'Выбрать, что отозвать').click())
    expect(buttons(root, 'Отозвать')).toHaveLength(1)
    cleanup(root)
  })

  it('slot_busy у оператора -- кто может, без кнопки', async () => {
    mocks.role = 'operator'
    mocks.issueReply = new ApiError(409, 'slot_busy', 'x')
    const { root } = await mount()
    await pickOption(root, 'Германия')
    await act(async () => button(root, 'Выпустить и положить на роутер').click())
    await flush()
    expect(root.querySelector('.cabinet-outcome').textContent).toBe('Свободных мест в подписке нет. Отозвать выпущенную страну может владелец роутера или администратор.')
    expect(button(root, 'Выбрать, что отозвать')).toBeFalsy()
    cleanup(root)
  })

  it('.conf в личку: лист с приватным ключом; успех -- итог; оператору кнопки нет', async () => {
    const { root, sheets } = await mount()
    await pickOption(root, 'Германия')
    await act(async () => button(root, 'Прислать .conf в личку').click())
    expect(sheets[0].title).toBe('Прислать .conf в личку?')
    expect(sheets[0].note).toContain('В файле приватный ключ')
    const sheetRoot = await mountSheet(sheets[0])
    await submit(sheetRoot)
    expect(calls('send')).toEqual([['send', 7, { provider: 'amnezia', option: 'de', instanceID: '' }]])
    await flush()
    expect(root.textContent).toContain('Файл отправлен вам в личку.')
    cleanup(sheetRoot)
    cleanup(root)

    mocks.role = 'operator'
    const again = await mount()
    await pickOption(again.root, 'Германия')
    expect(button(again.root, 'Прислать .conf в личку')).toBeFalsy()
    cleanup(again.root)
  })

  it('dm_unreachable -- словами на листе', async () => {
    mocks.sendReply = new ApiError(409, 'dm_unreachable', 'x')
    const { root, sheets } = await mount()
    await pickOption(root, 'Германия')
    await act(async () => button(root, 'Прислать .conf в личку').click())
    const sheetRoot = await mountSheet(sheets[0])
    await submit(sheetRoot)
    expect(sheetRoot.textContent).toContain('Бот не может написать вам — откройте бота, нажмите /start и повторите.')
    cleanup(sheetRoot)
    cleanup(root)
  })

  it('свой сервер: только включённые, выпуск и .conf -- через id сервера', async () => {
    mocks.cabinets.selfhosted.available = true
    mocks.role = 'admin'
    mocks.instances = [
      { id: 'ams', label: 'Амстердам', enabled: true, endpoint_host: 'vpn.example.com', endpoint_port: 51820, password_set: true },
      { id: 'spare', label: 'Запасной', enabled: false },
    ]
    const { root, sheets } = await mount()
    await tab(root, 'Свой сервер')
    const titles = [...root.querySelectorAll('.list-row .row-title')].map((t) => t.textContent)
    expect(titles).toEqual(['Амстердам'])
    await act(async () => root.querySelector('.list-row-btn').click())
    await flush()
    expect(root.textContent).toContain('Сервер создаст на «Амстердам» нового клиента')
    await act(async () => button(root, 'Выпустить и положить на роутер').click())
    await flush()
    await flush()
    expect(calls('issue')).toEqual([['issue', 7, 'selfhosted', 'ams', 'ams']])
    await act(async () => button(root, 'Прислать .conf в личку').click())
    expect(sheets[0].body).toContain('На сервере для этого будет создан ещё один клиент.')
    cleanup(root)
  })
})

describe('кабинет роутера: права, загрузка, перечитывание', () => {
  it('права не прочитались -- слова и «Повторить», кнопок правки нет; повтор возвращает их', async () => {
    mocks.settingsFail = true
    const { root } = await mount()
    expect(root.textContent).toContain('Не удалось узнать ваши права.')
    expect(button(root, 'Добавить ключ')).toBeFalsy()
    mocks.settingsFail = false
    await act(async () => button(root, 'Повторить').click())
    await flush()
    await flush()
    expect(root.textContent).not.toContain('Не удалось узнать ваши права.')
    expect(button(root, 'Добавить ключ')).toBeTruthy()
    expect(calls('settings')).toHaveLength(2)
    cleanup(root)
  })

  it('кабинеты на сервере не настроены -- слова сервера', async () => {
    mocks.cabinetsFail = new ApiError(503, 'cabinets_not_configured', 'x', 'Кабинеты VPN на сервере не настроены')
    const { root } = await mount()
    expect(root.querySelector('.state-error').textContent).toBe('Кабинеты VPN на сервере не настроены')
    cleanup(root)
  })

  it('пока выпуск идёт, «назад» погашен', async () => {
    mocks.issueReply = 'hang'
    const { root } = await mount()
    await pickOption(root, 'Германия')
    expect(root.querySelector('.overlay-back').disabled).toBe(false)
    await act(async () => button(root, 'Выпустить и положить на роутер').click())
    await flush()
    expect(root.querySelector('.overlay-back').disabled).toBe(true)
    cleanup(root)
  })

  it('после успешного выпуска и после возврата к списку кабинет перечитан', async () => {
    const { root } = await mount()
    expect(calls('cabinets')).toHaveLength(1)
    await pickOption(root, 'Германия')
    await act(async () => button(root, 'Выпустить и положить на роутер').click())
    await flush()
    await flush()
    expect(calls('cabinets')).toHaveLength(2)
    expect(calls('vpn')).toHaveLength(2)
    await act(async () => root.querySelector('.overlay-back').click())
    await flush()
    expect(calls('cabinets')).toHaveLength(3)
    expect(calls('vpn')).toHaveLength(3)
    expect(root.querySelector('.segment-tabs')).toBeTruthy()
    cleanup(root)
  })
})

