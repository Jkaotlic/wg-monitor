// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from 'preact'
import { act } from 'preact/test-utils'

const mocks = vi.hoisted(() => ({
  instances: [],
  defaults: null,
  loadFail: false,
  calls: [],
  create: null,
  updateReply: null,
  toggleReply: null,
  deleteReply: null,
  checkReply: null,
}))

vi.mock('../src/api.js', async (importOriginal) => {
  const real = await importOriginal()
  const reply = (v, fallback) => (v instanceof Error ? Promise.reject(v) : Promise.resolve(v ?? fallback))
  // Тело копируется в момент вызова: экран стирает пароль из тела сразу после
  // отправки, и проверять надо то, что ушло, а не то, что осталось.
  const log = (...args) => mocks.calls.push(structuredClone(args))
  return {
    ...real,
    fetchSelfhosted: () =>
      mocks.calls.push(['list']) &&
      mocks.loadFail ? Promise.reject(new Error('net')) : Promise.resolve({ instances: structuredClone(mocks.instances), defaults: structuredClone(mocks.defaults) }),
    createSelfhosted: (body) => {
      log('create', body)
      return new Promise((resolve, reject) => {
        mocks.create = { resolve, reject }
      })
    },
    updateSelfhosted: (id, body) => {
      log('update', id, body)
      // Удачная правка ложится в «сервер» так же, как у настоящего: экран
      // перечитывает список после сохранения.
      if (!mocks.updateReply) {
        const inst = mocks.instances.find((i) => i.id === id)
        if (inst) {
          const { ssh_password: pw, ...rest } = body
          Object.assign(inst, rest)
          if (pw) inst.password_set = true
          if (rest.ssh_host === '') Object.assign(inst, { ssh_user: '', ssh_port: 0, password_set: false })
        }
      }
      return reply(mocks.updateReply, null)
    },
    toggleSelfhosted: (id, enabled) => {
      log('toggle', id, enabled)
      return reply(mocks.toggleReply, null)
    },
    deleteSelfhosted: (id, confirm) => {
      log('delete', id, confirm)
      return reply(mocks.deleteReply, null)
    },
    checkSelfhosted: (id) => {
      log('check', id)
      return reply(mocks.checkReply, { ok: true, message: '' })
    },
  }
})

const { SelfhostedScreen } = await import('../src/screens/SelfhostedScreen.jsx')
const { SelfhostedInstanceScreen } = await import('../src/screens/SelfhostedInstanceScreen.jsx')
const { Sheet } = await import('../src/ui/Sheet.jsx')
const { ApiError } = await import('../src/api.js')

const AMS = {
  id: 'ams',
  label: 'Амстердам',
  enabled: true,
  container: 'amnezia-awg2',
  interface: 'awg0',
  endpoint_host: 'vpn.example.com',
  endpoint_port: 51820,
  config_path: '',
  clients_path: '',
  server_public_key_path: '',
  preshared_key_path: '',
  dns: ['203.0.113.53'],
  ssh_host: '203.0.113.10',
  ssh_port: 22,
  ssh_user: 'root',
  password_set: true,
}

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })
const button = (root, text) => [...root.querySelectorAll('button')].find((b) => b.textContent.trim() === text)
const cleanup = (root) => { render(null, root); root.remove() }
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

async function click(el) {
  await act(async () => el.click())
  await flush()
}

async function mountInstance(instanceId) {
  const seen = { closed: 0, sheets: [] }
  const root = await mountNode(
    <SelfhostedInstanceScreen instanceId={instanceId} openSheet={(s) => seen.sheets.push(s)} onClose={() => seen.closed++} />,
  )
  return { root, seen }
}

beforeEach(() => {
  mocks.instances = [structuredClone(AMS), { id: 'spare', label: '', enabled: false }]
  mocks.loadFail = false
  mocks.defaults = null
  mocks.calls = []
  mocks.create = null
  mocks.updateReply = null
  mocks.toggleReply = null
  mocks.deleteReply = null
  mocks.checkReply = null
})

describe('«Свои VPN-серверы»: список', () => {
  it('строки, открытие сервера, «Добавить сервер», «назад»', async () => {
    const opened = []
    let closed = 0
    const root = await mountNode(<SelfhostedScreen backLabel="Обслуживание" onClose={() => closed++} onOpenInstance={(id) => opened.push(id)} />)
    expect(root.querySelector('.overlay-title').textContent).toBe('Свои VPN-серверы')
    expect(root.querySelector('.overlay-back').textContent).toContain('Обслуживание')
    const rows = [...root.querySelectorAll('.selfhosted-list .list-row')]
    expect(rows.map((r) => r.querySelector('.row-title').textContent)).toEqual(['Амстердам', 'spare'])
    expect(rows.map((r) => r.querySelector('.list-row-sub').textContent)).toEqual(['vpn.example.com:51820', 'выключен'])
    await click(rows[0])
    await click(button(root, 'Добавить сервер'))
    expect(opened).toEqual(['ams', ''])
    await click(root.querySelector('.overlay-back'))
    expect(closed).toBe(1)
    cleanup(root)
  })

  it('пусто -- слова и кнопка; не прочиталось -- слова', async () => {
    mocks.instances = []
    let root = await mountNode(<SelfhostedScreen onClose={() => {}} onOpenInstance={() => {}} />)
    expect(root.textContent).toContain('Своих серверов пока нет.')
    expect(button(root, 'Добавить сервер')).toBeTruthy()
    cleanup(root)

    mocks.loadFail = true
    root = await mountNode(<SelfhostedScreen onClose={() => {}} onOpenInstance={() => {}} />)
    expect(root.textContent).toContain('Не удалось прочитать список серверов.')
    cleanup(root)
  })
})

describe('новый сервер', () => {
  it('группы, короткое имя, поле пароля; проверка до отправки; нет удаления и проверки', async () => {
    const { root } = await mountInstance('')
    expect(root.querySelector('.overlay-title').textContent).toBe('Новый сервер')
    expect([...root.querySelectorAll('.section-title')].map((h) => h.textContent)).toEqual(['Адрес для клиентов', 'Контейнер и пути', 'SSH'])
    expect(root.querySelector('#sh-id')).toBeTruthy()
    const pw = root.querySelector('#sh-ssh_password')
    expect(pw.type).toBe('password')
    expect(pw.getAttribute('autocomplete')).toBe('new-password')
    expect(root.textContent).toContain('Пароль SSH хранится на сервере и наружу не отдаётся.')
    expect(button(root, 'Удалить сервер')).toBeFalsy()
    expect(button(root, 'Проверить подключение')).toBeFalsy()
    await click(button(root, 'Добавить сервер'))
    expect(root.querySelector('.wizard-error').textContent).toBe('Короткое имя: строчная латиница, цифры, «-» и «_», от 2 до 16 знаков, первая — буква.')
    expect(calls('create')).toEqual([])
    cleanup(root)
  })

  async function fillNew(root) {
    await fill(root, 'sh-id', 'ams')
    await fill(root, 'sh-label', 'Амстердам')
    await fill(root, 'sh-endpoint_host', 'vpn.example.com')
    await fill(root, 'sh-endpoint_port', '51820')
    await fill(root, 'sh-ssh_host', '203.0.113.10')
    await fill(root, 'sh-ssh_password', 'S3cret pw')
  }

  it('создание: пароль уходит в теле и стирается до ответа; успех -- к списку', async () => {
    const { root, seen } = await mountInstance('')
    await fillNew(root)
    await click(button(root, 'Добавить сервер'))
    expect(calls('create')).toEqual([
      [
        'create',
        {
          id: 'ams',
          label: 'Амстердам',
          endpoint_host: 'vpn.example.com',
          endpoint_port: 51820,
          dns: [],
          container: '',
          interface: '',
          config_path: '',
          clients_path: '',
          server_public_key_path: '',
          preshared_key_path: '',
          ssh_host: '203.0.113.10',
          ssh_port: 0,
          ssh_user: '',
          ssh_password: 'S3cret pw',
        },
      ],
    ])
    expect(root.querySelector('#sh-ssh_password').value).toBe('')
    expect(button(root, 'Сохраняем…')).toBeTruthy()
    expect(seen.closed).toBe(0)
    await act(async () => mocks.create.resolve({ id: 'ams' }))
    await flush()
    expect(seen.closed).toBe(1)
    cleanup(root)
  })

  // Сверка с частью 1: адрес SSH необязателен (контейнер на той же машине),
  // и тогда пароль не спрашивается; плейсхолдеры -- defaults сервера.
  it('без адреса SSH пароль не нужен; плейсхолдеры -- значения сервера по умолчанию', async () => {
    mocks.defaults = { container: 'awg-main', dns: ['203.0.113.53'], ssh_port: 22, ssh_user: 'root' }
    const { root } = await mountInstance('')
    expect(root.querySelector('#sh-container').getAttribute('placeholder')).toBe('awg-main')
    expect(root.querySelector('#sh-dns').getAttribute('placeholder')).toBe('203.0.113.53')
    await fill(root, 'sh-id', 'local')
    await fill(root, 'sh-endpoint_host', 'vpn.example.com')
    await fill(root, 'sh-endpoint_port', '51820')
    await click(button(root, 'Добавить сервер'))
    expect(root.querySelector('.wizard-error')).toBe(null)
    const [[, body]] = calls('create')
    expect(body.ssh_host).toBe('')
    expect('ssh_password' in body).toBe(false)
    cleanup(root)
  })

  it('отказ сервера -- его слова, пароль надо ввести заново, остальное на месте', async () => {
    const { root, seen } = await mountInstance('')
    await fillNew(root)
    await click(button(root, 'Добавить сервер'))
    await act(async () => mocks.create.reject(new ApiError(409, 'instance_exists', 'x')))
    await flush()
    expect(root.querySelector('.wizard-error').textContent).toBe('Сервер с таким коротким именем уже есть.')
    expect(root.querySelector('#sh-ssh_password').value).toBe('')
    expect(root.querySelector('#sh-label').value).toBe('Амстердам')
    expect(seen.closed).toBe(0)
    cleanup(root)
  })
})

describe('экран сервера', () => {
  it('поля с сервера, короткое имя не правится, «Пароль задан»; без изменений запроса нет', async () => {
    const { root } = await mountInstance('ams')
    expect(root.querySelector('.overlay-title').textContent).toBe('Сервер «Амстердам»')
    expect(root.querySelector('#sh-id')).toBe(null)
    expect(root.querySelector('#sh-label').value).toBe('Амстердам')
    expect(root.querySelector('#sh-dns').value).toBe('203.0.113.53')
    expect(root.querySelector('#sh-ssh_password').value).toBe('')
    expect(root.textContent).toContain('Пароль задан. Пустое поле оставит его как есть.')
    await click(button(root, 'Сохранить'))
    expect(calls('update')).toEqual([])
    expect(root.querySelector('.connection-notice').textContent).toBe('Ничего не изменилось.')
    cleanup(root)
  })

  it('пустой пароль не уходит; введённый -- уходит и стирается', async () => {
    const { root } = await mountInstance('ams')
    await fill(root, 'sh-label', 'Амстердам-2')
    await click(button(root, 'Сохранить'))
    const [first] = calls('update')
    expect(first[1]).toBe('ams')
    expect(first[2].label).toBe('Амстердам-2')
    expect('ssh_password' in first[2]).toBe(false)
    expect('id' in first[2]).toBe(false)
    expect(root.querySelector('.connection-notice').textContent).toBe('Сохранено.')

    await fill(root, 'sh-ssh_password', 'new-pw')
    await click(button(root, 'Сохранить'))
    const second = calls('update')[1]
    expect(second[2].ssh_password).toBe('new-pw')
    expect(root.querySelector('#sh-ssh_password').value).toBe('')
    cleanup(root)
  })

  it('пароль не задан -- так и сказано; с адресом SSH сохранить без пароля нельзя', async () => {
    mocks.instances[0].password_set = false
    const { root } = await mountInstance('ams')
    expect(root.textContent).toContain('Пароль не задан.')
    await fill(root, 'sh-label', 'Амстердам-2')
    await click(button(root, 'Сохранить'))
    expect(root.querySelector('.wizard-error').textContent).toBe('Укажите пароль SSH.')
    expect(calls('update')).toEqual([])
    cleanup(root)
  })

  it('«Проверить подключение» -- одна попытка, итог словами', async () => {
    mocks.checkReply = { ok: false, message: 'SSH: неверный пароль' }
    const { root } = await mountInstance('ams')
    expect(root.textContent).toContain('Проверяется то, что уже сохранено: одна попытка входа по SSH.')
    await click(button(root, 'Проверить подключение'))
    expect(calls('check')).toEqual([['check', 'ams']])
    const out = root.querySelector('.selfhosted-check')
    expect(out.textContent).toBe('SSH: неверный пароль')
    expect(out.classList.contains('selfhosted-check-bad')).toBe(true)
    cleanup(root)
  })

  it('вкл/выкл', async () => {
    const { root } = await mountInstance('ams')
    await click(button(root, 'Выключить'))
    expect(calls('toggle')).toEqual([['toggle', 'ams', false]])
    expect(root.querySelector('.connection-notice').textContent).toBe('Сервер выключен: выпускать с него VPN-туннели нельзя, пока не включите.')
    expect(button(root, 'Включить')).toBeTruthy()
    cleanup(root)
  })

  it('удалить -- набор названия строго; успех -- к списку', async () => {
    const { root, seen } = await mountInstance('ams')
    await click(button(root, 'Удалить сервер'))
    expect(seen.sheets[0]).toMatchObject({ title: 'Удалить сервер «Амстердам»?', danger: true, confirmPhrase: 'Амстердам', confirmStrict: true })
    const sheetRoot = await mountNode(<Sheet sheet={seen.sheets[0]} asleep={false} onClose={() => {}} />)
    const primary = () => [...sheetRoot.querySelectorAll('.sheet-actions button')].pop()
    await fill(sheetRoot, 'sheet-confirm-input', 'амстердам')
    expect(primary().disabled).toBe(true)
    await fill(sheetRoot, 'sheet-confirm-input', 'Амстердам')
    expect(primary().disabled).toBe(false)
    await click(primary())
    expect(calls('delete')).toEqual([['delete', 'ams', 'Амстердам']])
    expect(seen.closed).toBe(1)
    cleanup(sheetRoot)
    cleanup(root)
  })

  it('сервера больше нет -- слова вместо формы', async () => {
    const { root } = await mountInstance('gone')
    expect(root.textContent).toContain('Такого сервера больше нет — вернитесь к списку.')
    expect(button(root, 'Сохранить')).toBeFalsy()
    cleanup(root)
  })
})

describe('ошибка поля от сервера', () => {
  it('invalid_field: поле подсвечено, слова сервера под ним, фокус на нём', async () => {
    mocks.updateReply = new ApiError(400, 'invalid_field', 'x', 'Порт для клиентов вне диапазона', 'endpoint_port')
    const { root } = await mountInstance('ams')
    await fill(root, 'sh-endpoint_port', '51821')
    await click(button(root, 'Сохранить'))
    await flush()
    const input = root.querySelector('#sh-endpoint_port')
    const field = input.closest('.field')
    expect(field.classList.contains('field-error')).toBe(true)
    expect(input.getAttribute('aria-invalid')).toBe('true')
    expect(field.querySelector('.field-error-text').textContent).toBe('Порт для клиентов вне диапазона')
    expect(document.activeElement).toBe(input)
    expect(root.querySelector('.wizard-error')).toBe(null)
    expect(root.querySelectorAll('.field-error')).toHaveLength(1)
    // Правка поля снимает подсветку.
    await fill(root, 'sh-endpoint_port', '51822')
    expect(root.querySelector('.field-error')).toBe(null)
    cleanup(root)
  })

  it('незнакомое поле -- слова над формой, как раньше', async () => {
    mocks.updateReply = new ApiError(400, 'invalid_field', 'x', 'Поле заполнено неверно', 'mystery')
    const { root } = await mountInstance('ams')
    await fill(root, 'sh-label', 'Амстердам-2')
    await click(button(root, 'Сохранить'))
    await flush()
    expect(root.querySelector('.field-error')).toBe(null)
    expect(root.querySelector('.wizard-error').textContent).toBe('Поле заполнено неверно')
    cleanup(root)
  })
})

describe('стёртый адрес SSH', () => {
  it('предупреждение под полем и лист подтверждения; без согласия запроса нет', async () => {
    const { root, seen } = await mountInstance('ams')
    expect(root.textContent).not.toContain('Без адреса SSH сохранённый пароль будет удалён')
    await fill(root, 'sh-ssh_host', '')
    const warn = root.querySelector('#sh-ssh_host').closest('.field').querySelector('.field-warn')
    expect(warn.textContent).toBe('Без адреса SSH сохранённый пароль будет удалён')
    await fill(root, 'sh-ssh_password', 'typed-pw')
    await click(button(root, 'Сохранить'))
    expect(calls('update')).toEqual([])
    expect(seen.sheets).toHaveLength(1)
    expect(seen.sheets[0]).toMatchObject({ danger: true })
    expect(seen.sheets[0].body).toContain('Без адреса SSH сохранённый пароль будет удалён')
    expect(JSON.stringify(seen.sheets[0])).not.toContain('typed-pw')
    expect(root.querySelector('#sh-ssh_password').value).toBe('')

    const sheetRoot = await mountNode(<Sheet sheet={seen.sheets[0]} asleep={false} onClose={() => {}} />)
    await click([...sheetRoot.querySelectorAll('.sheet-actions button')].pop())
    await flush()
    const [[, id, body]] = calls('update')
    expect(id).toBe('ams')
    expect(body.ssh_host).toBe('')
    expect('ssh_password' in body).toBe(false)
    expect(root.querySelector('.connection-notice').textContent).toBe('Сохранено.')
    expect(root.textContent).toContain('Пароль не задан.')
    expect(root.textContent).not.toContain('Без адреса SSH сохранённый пароль будет удалён')
    cleanup(sheetRoot)
    cleanup(root)
  })
})

describe('смена адреса SSH и перечитывание', () => {
  it('сменили порт SSH -- предупреждение под адресом, без пароля не сохраняется', async () => {
    const { root } = await mountInstance('ams')
    await fill(root, 'sh-ssh_port', '2222')
    const warn = root.querySelector('#sh-ssh_host').closest('.field').querySelector('.field-warn')
    expect(warn.textContent).toBe('Адрес SSH изменён — введите пароль заново')
    await click(button(root, 'Сохранить'))
    expect(calls('update')).toEqual([])
    const pw = root.querySelector('#sh-ssh_password')
    expect(pw.closest('.field').querySelector('.field-error-text').textContent).toBe('Адрес SSH изменён — введите пароль заново')
    await fill(root, 'sh-ssh_password', 'new-pw')
    await click(button(root, 'Сохранить'))
    expect(calls('update')).toHaveLength(1)
    expect(calls('update')[0][2].ssh_password).toBe('new-pw')
    cleanup(root)
  })

  it('после сохранения сервер перечитан: новое название в заголовке', async () => {
    const { root } = await mountInstance('ams')
    expect(calls('list')).toHaveLength(1)
    await fill(root, 'sh-label', 'Амстердам-2')
    mocks.instances[0].label = 'Амстердам-2'
    mocks.instances[0].ssh_user = 'root'
    await click(button(root, 'Сохранить'))
    await flush()
    expect(calls('list')).toHaveLength(2)
    expect(root.querySelector('.overlay-title').textContent).toBe('Сервер «Амстердам-2»')
    expect(root.querySelector('.connection-notice').textContent).toBe('Сохранено.')
    cleanup(root)
  })
})

