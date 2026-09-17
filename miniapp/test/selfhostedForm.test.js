import { describe, it, expect } from 'vitest'
import {
  INSTANCE_ID_RE,
  INSTANCE_KEYS,
  SELFHOSTED_GROUPS,
  instanceFormValues,
  fieldPlaceholder,
  validateInstance,
  instanceRequestBody,
  instanceChanged,
  endpointText,
  instanceRows,
  selfhostedIssueRows,
  passwordHint,
  checkResultView,
  toggleLabel,
  toggleDoneText,
  deleteInstanceSheetText,
  deleteConfirmPhrase,
  selfhostedErrorText,
  errorFieldKey,
  sshWipeWarning,
  SSH_WIPE_TEXT,
  SSH_CHANGED_TEXT,
  sshAddressChanged,
  sshHostWarning,
  SELFHOSTED_TEXTS,
} from '../src/selfhostedForm.js'
import { ApiError } from '../src/api.js'

const INST = {
  id: 'ams',
  label: 'Амстердам',
  enabled: true,
  container: 'amnezia-awg2',
  interface: 'awg0',
  endpoint_host: 'vpn.example.com',
  endpoint_port: 51820,
  config_path: '/opt/amnezia/awg/awg0.conf',
  clients_path: '/opt/amnezia/awg/clientsTable',
  server_public_key_path: '/opt/amnezia/awg/wireguard_server_public_key.key',
  preshared_key_path: '/opt/amnezia/awg/wireguard_psk.key',
  dns: ['203.0.113.53', '203.0.113.54'],
  ssh_host: '203.0.113.10',
  ssh_port: 22,
  ssh_user: 'root',
  password_set: true,
}

const VALID_NEW = {
  ...instanceFormValues(null),
  id: 'ams',
  label: 'Амстердам',
  endpoint_host: 'vpn.example.com',
  endpoint_port: '51820',
  ssh_host: '203.0.113.10',
  ssh_password: 'pw',
}

describe('группы полей (спека: адрес для клиентов, контейнер и пути, SSH)', () => {
  it('три группы по порядку, пароль -- единственное поле-пароль, id -- только у нового', () => {
    expect(SELFHOSTED_GROUPS.map((g) => g.title)).toEqual(['Адрес для клиентов', 'Контейнер и пути', 'SSH'])
    const all = SELFHOSTED_GROUPS.flatMap((g) => g.fields)
    expect(all.filter((f) => f.kind === 'password').map((f) => f.key)).toEqual(['ssh_password'])
    expect(all.filter((f) => f.newOnly).map((f) => f.key)).toEqual(['id'])
    expect(all.map((f) => f.key).sort()).toEqual([...INSTANCE_KEYS, 'ssh_password'].sort())
  })

  it('плейсхолдеры -- только документационные адреса', () => {
    for (const f of SELFHOSTED_GROUPS.flatMap((g) => g.fields)) {
      const p = f.placeholder ?? ''
      const ip = p.match(/\b\d{1,3}(\.\d{1,3}){3}\b/)
      if (ip) expect(ip[0], f.key).toMatch(/^(198\.51\.100|203\.0\.113)\./)
      if (/\.[a-z]{2,}$/.test(p) && !p.startsWith('/')) expect(p, f.key).toMatch(/example\.com$/)
    }
  })
})

describe('плейсхолдеры из defaults сервера', () => {
  const field = (key) => SELFHOSTED_GROUPS.flatMap((g) => g.fields).find((f) => f.key === key)

  it('значение по умолчанию сервера важнее зашитого; DNS списком, порт строкой', () => {
    const defaults = { container: 'awg-main', dns: ['203.0.113.53', '203.0.113.54'], ssh_port: 2222 }
    expect(fieldPlaceholder(field('container'), defaults)).toBe('awg-main')
    expect(fieldPlaceholder(field('dns'), defaults)).toBe('203.0.113.53, 203.0.113.54')
    expect(fieldPlaceholder(field('ssh_port'), defaults)).toBe('2222')
    expect(fieldPlaceholder(field('interface'), defaults)).toBe('awg0')
    expect(fieldPlaceholder(field('container'), null)).toBe('amnezia-awg2')
    expect(fieldPlaceholder(field('ssh_password'), { ssh_password: 'x' })).toBe('')
  })
})

describe('значения формы', () => {
  it('новый -- пустые строки, пароль пуст', () => {
    const v = instanceFormValues(null)
    for (const key of INSTANCE_KEYS) expect(v[key], key).toBe('')
    expect(v.ssh_password).toBe('')
  })

  it('существующий -- строки, порты строками, DNS через запятую, пароля нет', () => {
    const v = instanceFormValues(INST)
    expect(v.endpoint_port).toBe('51820')
    expect(v.ssh_port).toBe('22')
    expect(v.dns).toBe('203.0.113.53, 203.0.113.54')
    expect(v.ssh_password).toBe('')
    expect(instanceFormValues({ ...INST, ssh_port: 0, dns: null }).ssh_port).toBe('')
    expect(instanceFormValues({ ...INST, ssh_port: 0, dns: null }).dns).toBe('')
  })
})

describe('проверка', () => {
  it('годный новый', () => {
    expect(validateInstance(VALID_NEW, { isNew: true })).toBe('')
  })

  it('по порядку полей', () => {
    expect(validateInstance({ ...VALID_NEW, id: 'Ams' }, { isNew: true })).toBe('Короткое имя: строчная латиница, цифры, «-» и «_», от 2 до 16 знаков, первая — буква.')
    expect(validateInstance({ ...VALID_NEW, endpoint_host: '' }, { isNew: true })).toBe('Укажите адрес сервера для клиентов.')
    expect(validateInstance({ ...VALID_NEW, endpoint_port: '' }, { isNew: true })).toBe('Порт для клиентов — число от 1 до 65535.')
    expect(validateInstance({ ...VALID_NEW, endpoint_port: '70000' }, { isNew: true })).toBe('Порт для клиентов — число от 1 до 65535.')
    expect(validateInstance({ ...VALID_NEW, ssh_port: '22a' }, { isNew: true })).toBe('Порт SSH — число от 1 до 65535.')
    expect(validateInstance({ ...VALID_NEW, ssh_password: '' }, { isNew: true })).toBe('Укажите пароль SSH.')
  })

  // Сверка с частью 1: название необязательно (пусто -- короткое имя), адрес
  // SSH тоже (пусто -- контейнер на той же машине, что и сервер wg-monitor);
  // пароль нужен, только когда задан адрес SSH и пароля ещё нет.
  it('название и SSH необязательны; пароль -- только к адресу SSH', () => {
    expect(validateInstance({ ...VALID_NEW, label: '  ' }, { isNew: true })).toBe('')
    expect(validateInstance({ ...VALID_NEW, ssh_host: '', ssh_password: '' }, { isNew: true })).toBe('')
    const edit = { ...instanceFormValues(INST), ssh_password: '' }
    expect(validateInstance(edit, { isNew: false, passwordSet: true })).toBe('')
    expect(validateInstance(edit, { isNew: false, passwordSet: false })).toBe('Укажите пароль SSH.')
    expect(validateInstance({ ...edit, ssh_host: '' }, { isNew: false, passwordSet: false })).toBe('')
  })

  it('правка: id не проверяется, пустой пароль допустим', () => {
    const v = { ...instanceFormValues(INST), id: '' }
    expect(validateInstance(v, { isNew: false, passwordSet: true })).toBe('')
  })

  it('id сервера -- как у сервера', () => {
    expect(INSTANCE_ID_RE.test('ams-1')).toBe(true)
    expect(INSTANCE_ID_RE.test('a')).toBe(false)
  })
})

describe('тело запроса', () => {
  it('новый: всё, порты числами, DNS списком, пароль как введён', () => {
    const body = instanceRequestBody({ ...VALID_NEW, dns: '203.0.113.53,  203.0.113.54', ssh_password: ' p w ' }, { isNew: true })
    expect(body).toEqual({
      id: 'ams',
      label: 'Амстердам',
      endpoint_host: 'vpn.example.com',
      endpoint_port: 51820,
      dns: ['203.0.113.53', '203.0.113.54'],
      container: '',
      interface: '',
      config_path: '',
      clients_path: '',
      server_public_key_path: '',
      preshared_key_path: '',
      ssh_host: '203.0.113.10',
      ssh_port: 0,
      ssh_user: '',
      ssh_password: ' p w ',
    })
  })

  it('правка: без id; пустой пароль -- ключа нет (не менять)', () => {
    const body = instanceRequestBody(instanceFormValues(INST), { isNew: false })
    expect('id' in body).toBe(false)
    expect('ssh_password' in body).toBe(false)
    expect(body.ssh_port).toBe(22)
    expect(instanceRequestBody({ ...instanceFormValues(INST), ssh_password: 'new' }, { isNew: false }).ssh_password).toBe('new')
  })

  it('изменилось ли: введённый пароль -- изменение', () => {
    const v = instanceFormValues(INST)
    expect(instanceChanged(v, { ...v }, { isNew: false })).toBe(false)
    expect(instanceChanged(v, { ...v, label: 'Амстердам-2' }, { isNew: false })).toBe(true)
    expect(instanceChanged(v, { ...v, ssh_password: 'x' }, { isNew: false })).toBe(true)
  })
})

describe('список', () => {
  it('строки: название или id, адрес, «выключен»', () => {
    expect(endpointText(INST)).toBe('vpn.example.com:51820')
    expect(endpointText({ endpoint: 'vpn.example.com:443', endpoint_host: 'x' })).toBe('vpn.example.com:443')
    expect(endpointText({ endpoint_host: 'vpn.example.com' })).toBe('vpn.example.com')
    expect(endpointText({})).toBe('')
    const rows = instanceRows([INST, { id: 'spare', label: '', enabled: false }])
    expect(rows).toEqual([
      { id: 'ams', title: 'Амстердам', sub: 'vpn.example.com:51820', enabled: true },
      { id: 'spare', title: 'spare', sub: 'выключен', enabled: false },
    ])
    expect(instanceRows(null)).toEqual([])
  })

  it('на выпуск -- только включённые', () => {
    expect(selfhostedIssueRows([INST, { id: 'spare', enabled: false }]).map((r) => r.id)).toEqual(['ams'])
  })
})

describe('тексты экрана сервера', () => {
  it('пароль: задан / не задан / новый', () => {
    expect(passwordHint(INST, { isNew: false })).toBe('Пароль задан. Пустое поле оставит его как есть.')
    expect(passwordHint({ ...INST, password_set: false }, { isNew: false })).toBe('Пароль не задан.')
    expect(passwordHint(null, { isNew: true })).toBe('Пароль SSH хранится на сервере и наружу не отдаётся.')
    // Вход SSH сменён -- «оставит как есть» было бы неправдой.
    expect(passwordHint(INST, { isNew: false }, { ...instanceFormValues(INST), ssh_port: '2222' })).toBe('Сохранённый пароль был для прежнего входа — введите пароль заново.')
    expect(passwordHint(INST, { isNew: false }, instanceFormValues(INST))).toBe('Пароль задан. Пустое поле оставит его как есть.')
  })

  it('проверка подключения', () => {
    expect(checkResultView({ ok: true, message: '' })).toEqual({ tone: 'ok', text: 'Подключение есть.' })
    expect(checkResultView({ ok: false, message: 'SSH: неверный пароль' })).toEqual({ tone: 'bad', text: 'SSH: неверный пароль' })
    expect(checkResultView(null)).toEqual({ tone: 'bad', text: 'Подключиться не удалось.' })
  })

  it('вкл/выкл и удаление', () => {
    expect(toggleLabel({ enabled: true })).toBe('Выключить')
    expect(toggleLabel({ enabled: false })).toBe('Включить')
    expect(toggleDoneText(true)).toBe('Сервер включён.')
    expect(toggleDoneText(false)).toBe('Сервер выключен: выпускать с него VPN-туннели нельзя, пока не включите.')
    expect(deleteInstanceSheetText(INST)).toEqual({
      title: 'Удалить сервер «Амстердам»?',
      body: 'Сервер пропадёт из списка, и выпускать с него VPN-туннели станет нельзя. Сам сервер и уже выпущенные VPN-туннели на роутерах не трогаются.',
    })
    expect(deleteConfirmPhrase(INST)).toBe('Амстердам')
    expect(deleteConfirmPhrase({ id: 'spare', label: '' })).toBe('spare')
  })

  it('ошибки', () => {
    expect(selfhostedErrorText(new ApiError(400, 'invalid_instance', 'x', 'Порт SSH вне диапазона'))).toBe('Порт SSH вне диапазона')
    expect(selfhostedErrorText(new ApiError(400, 'confirm_mismatch', 'x'))).toBe('Название сервера набрано не так.')
    expect(selfhostedErrorText(new ApiError(404, 'not_found', 'x'))).toBe('Такого сервера больше нет — вернитесь к списку.')
    expect(selfhostedErrorText(new ApiError(404, 'instance_not_found', 'x'))).toBe('Такого сервера больше нет — вернитесь к списку.')
    expect(selfhostedErrorText(new ApiError(400, 'invalid_field', 'x', 'Порт для клиентов вне диапазона'))).toBe('Порт для клиентов вне диапазона')
    expect(selfhostedErrorText(new ApiError(409, 'instance_exists', 'x'))).toBe('Сервер с таким коротким именем уже есть.')
    expect(selfhostedErrorText(new Error('net'))).toBe('Не получилось. Попробуйте ещё раз.')
  })

  it('словарь: «Свои VPN-серверы», без self-hosted и без бота', () => {
    expect(SELFHOSTED_TEXTS.title).toBe('Свои VPN-серверы')
    const all = Object.values(SELFHOSTED_TEXTS).join(' ')
    expect(all).not.toMatch(/self-?hosted|боту|в боте/i)
  })
})

describe('ошибка поля и стирание пароля', () => {
  it('errorFieldKey: только invalid_field и известное поле формы', () => {
    expect(errorFieldKey(new ApiError(400, 'invalid_field', 'x', 'm', 'endpoint_port'))).toBe('endpoint_port')
    expect(errorFieldKey(new ApiError(400, 'invalid_field', 'x', 'm', 'ssh_password'))).toBe('ssh_password')
    expect(errorFieldKey(new ApiError(400, 'invalid_field', 'x', 'm', 'nope'))).toBe('')
    expect(errorFieldKey(new ApiError(400, 'invalid_field', 'x', 'm'))).toBe('')
    expect(errorFieldKey(new ApiError(409, 'instance_exists', 'x', 'm', 'id'))).toBe('')
    expect(errorFieldKey(null)).toBe('')
  })

  it('sshWipeWarning: был адрес SSH и пароль, адрес стёрт', () => {
    expect(SSH_WIPE_TEXT).toBe('Без адреса SSH сохранённый пароль будет удалён')
    const v = instanceFormValues(INST)
    expect(sshWipeWarning(INST, { ...v, ssh_host: '  ' })).toBe(SSH_WIPE_TEXT)
    expect(sshWipeWarning(INST, v)).toBe('')
    expect(sshWipeWarning({ ...INST, password_set: false }, { ...v, ssh_host: '' })).toBe('')
    expect(sshWipeWarning({ ...INST, ssh_host: '' }, { ...v, ssh_host: '' })).toBe('')
    expect(sshWipeWarning(null, { ...v, ssh_host: '' })).toBe('')
  })
})

describe('смена адреса SSH', () => {
  const v = instanceFormValues(INST)

  it('адрес, порт или пользователь -- смена; пустые порт и пользователь -- 22 и root', () => {
    expect(sshAddressChanged(INST, v)).toBe(false)
    expect(sshAddressChanged(INST, { ...v, ssh_port: '', ssh_user: '' })).toBe(false)
    expect(sshAddressChanged(INST, { ...v, ssh_host: '203.0.113.11' })).toBe(true)
    expect(sshAddressChanged(INST, { ...v, ssh_port: '2222' })).toBe(true)
    expect(sshAddressChanged(INST, { ...v, ssh_user: 'admin' })).toBe(true)
    // Стёртый адрес -- не смена, а стирание (своё предупреждение).
    expect(sshAddressChanged(INST, { ...v, ssh_host: '' })).toBe(false)
    expect(sshAddressChanged({ ...INST, ssh_host: '' }, { ...v, ssh_host: '203.0.113.11' })).toBe(false)
    expect(sshAddressChanged(null, v)).toBe(false)
  })

  it('проверка: сменили адрес -- пароль обязателен', () => {
    const changed = { ...v, ssh_port: '2222' }
    expect(validateInstance(changed, { isNew: false, passwordSet: true, saved: INST })).toBe(SSH_CHANGED_TEXT)
    expect(validateInstance({ ...changed, ssh_password: 'pw' }, { isNew: false, passwordSet: true, saved: INST })).toBe('')
    expect(validateInstance(v, { isNew: false, passwordSet: true, saved: INST })).toBe('')
  })

  it('предупреждение под адресом: стирание или смена', () => {
    expect(SSH_CHANGED_TEXT).toBe('Адрес SSH изменён — введите пароль заново')
    expect(sshHostWarning(INST, v)).toBe('')
    expect(sshHostWarning(INST, { ...v, ssh_host: '' })).toBe(SSH_WIPE_TEXT)
    expect(sshHostWarning(INST, { ...v, ssh_user: 'admin' })).toBe(SSH_CHANGED_TEXT)
    expect(sshHostWarning({ ...INST, password_set: false }, { ...v, ssh_user: 'admin' })).toBe('')
  })
})

