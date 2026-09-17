// Свои VPN-серверы (Amnezia на своём VPS): форма сервера, проверка, тело
// запроса и тексты. Только админ. SSH-пароль сюда приходит лишь снимком
// значений формы -- чтобы положить его в тело, когда он введён. Сервер его
// наружу не отдаёт: вместо него password_set.

export const INSTANCE_ID_RE = /^[a-z][a-z0-9_-]{1,15}$/

export const INSTANCE_KEYS = [
  'id',
  'label',
  'endpoint_host',
  'endpoint_port',
  'dns',
  'container',
  'interface',
  'config_path',
  'clients_path',
  'server_public_key_path',
  'preshared_key_path',
  'ssh_host',
  'ssh_port',
  'ssh_user',
]

// Плейсхолдеры путей -- значения по умолчанию сервера (selfhostedamnezia,
// withDefaults): пустое поле и означает их.
export const SELFHOSTED_GROUPS = [
  {
    title: 'Адрес для клиентов',
    fields: [
      { key: 'label', label: 'Название', kind: 'text', placeholder: 'Амстердам' },
      { key: 'id', label: 'Короткое имя', kind: 'text', placeholder: 'ams-1', newOnly: true, hint: 'Латиница, цифры, «-» и «_». Потом не меняется.' },
      { key: 'endpoint_host', label: 'Адрес сервера', kind: 'text', placeholder: 'vpn.example.com' },
      { key: 'endpoint_port', label: 'Порт', kind: 'text', placeholder: '51820', inputMode: 'numeric' },
      { key: 'dns', label: 'DNS для клиентов', kind: 'text', placeholder: '', hint: 'Через запятую. Пусто — как на сервере по умолчанию.' },
    ],
  },
  {
    title: 'Контейнер и пути',
    note: 'Пустое поле — значение Amnezia по умолчанию.',
    fields: [
      { key: 'container', label: 'Контейнер', kind: 'text', placeholder: 'amnezia-awg2' },
      { key: 'interface', label: 'Интерфейс', kind: 'text', placeholder: 'awg0' },
      { key: 'config_path', label: 'Конфиг сервера', kind: 'text', placeholder: '/opt/amnezia/awg/awg0.conf' },
      { key: 'clients_path', label: 'Таблица клиентов', kind: 'text', placeholder: '/opt/amnezia/awg/clientsTable' },
      { key: 'server_public_key_path', label: 'Публичный ключ сервера', kind: 'text', placeholder: '/opt/amnezia/awg/wireguard_server_public_key.key' },
      { key: 'preshared_key_path', label: 'Общий ключ (PSK)', kind: 'text', placeholder: '/opt/amnezia/awg/wireguard_psk.key' },
    ],
  },
  {
    title: 'SSH',
    note: 'Адрес SSH пустой — контейнер на той же машине, что и сервер wg-monitor; тогда пароль не нужен.',
    fields: [
      { key: 'ssh_host', label: 'Адрес', kind: 'text', placeholder: '203.0.113.10' },
      { key: 'ssh_port', label: 'Порт', kind: 'text', placeholder: '22', inputMode: 'numeric' },
      { key: 'ssh_user', label: 'Пользователь', kind: 'text', placeholder: 'root' },
      { key: 'ssh_password', label: 'Пароль', kind: 'password' },
    ],
  },
]

// Плейсхолдер поля -- значение сервера по умолчанию из ответа списка
// (defaults), если сервер его прислал; иначе зашитое. Пароля в defaults не
// бывает, и у поля пароля плейсхолдера нет.
export function fieldPlaceholder(field, defaults) {
  if (!field || field.kind === 'password') return ''
  const d = defaults?.[field.key]
  if (Array.isArray(d) && d.length > 0) return d.join(', ')
  if (typeof d === 'number' && d > 0) return String(d)
  if (typeof d === 'string' && d !== '') return d
  return field.placeholder ?? ''
}

function trimmed(v) {
  return typeof v === 'string' ? v.trim() : ''
}

export function instanceFormValues(inst) {
  const values = {}
  for (const key of INSTANCE_KEYS) {
    const v = inst?.[key]
    values[key] = v == null ? '' : String(v)
  }
  values.endpoint_port = inst?.endpoint_port ? String(inst.endpoint_port) : ''
  values.ssh_port = inst?.ssh_port ? String(inst.ssh_port) : ''
  values.dns = Array.isArray(inst?.dns) ? inst.dns.join(', ') : ''
  values.ssh_password = ''
  return values
}

function validPort(v) {
  return /^\d+$/.test(v) && Number(v) >= 1 && Number(v) <= 65535
}

// Правила -- как у сервера (сверка с частью 1): название необязательно
// (пусто -- короткое имя), адрес SSH тоже (пусто -- контейнер на той же
// машине); пароль обязателен, только когда адрес SSH задан, а пароля ещё нет
// (passwordSet -- password_set сохранённого сервера).
export function validateInstance(values, { isNew, passwordSet = false, saved = null }) {
  if (isNew && !INSTANCE_ID_RE.test(trimmed(values.id))) {
    return 'Короткое имя: строчная латиница, цифры, «-» и «_», от 2 до 16 знаков, первая — буква.'
  }
  if (!trimmed(values.endpoint_host)) return 'Укажите адрес сервера для клиентов.'
  if (!validPort(trimmed(values.endpoint_port))) return 'Порт для клиентов — число от 1 до 65535.'
  const sshPort = trimmed(values.ssh_port)
  if (sshPort && !validPort(sshPort)) return 'Порт SSH — число от 1 до 65535.'
  const needPassword = trimmed(values.ssh_host) !== '' && (isNew || !passwordSet)
  if (needPassword && !values.ssh_password) return 'Укажите пароль SSH.'
  // Сохранённый пароль -- только для того же входа (сервер проверяет так же).
  if (!isNew && !values.ssh_password && sshAddressChanged(saved, values)) return SSH_CHANGED_TEXT
  return ''
}

// Пароль не обрезается: пробел может быть его частью. Пустой -- ключа нет,
// сервер оставляет прежний (спека: «ssh_password пусто = не менять»).
export function instanceRequestBody(values, { isNew }) {
  const body = {}
  for (const key of INSTANCE_KEYS) body[key] = trimmed(values?.[key])
  if (!isNew) delete body.id
  body.endpoint_port = body.endpoint_port === '' ? 0 : Number(body.endpoint_port)
  body.ssh_port = body.ssh_port === '' ? 0 : Number(body.ssh_port)
  body.dns = body.dns ? body.dns.split(/[,\s]+/).filter(Boolean) : []
  const password = typeof values?.ssh_password === 'string' ? values.ssh_password : ''
  if (password !== '') body.ssh_password = password
  return body
}

export function instanceChanged(initial, values, { isNew }) {
  if (typeof values?.ssh_password === 'string' && values.ssh_password !== '') return true
  return JSON.stringify(instanceRequestBody(initial ?? {}, { isNew })) !== JSON.stringify(instanceRequestBody(values ?? {}, { isNew }))
}

export function endpointText(inst) {
  if (inst?.endpoint) return String(inst.endpoint)
  if (!inst?.endpoint_host) return ''
  return inst.endpoint_port ? `${inst.endpoint_host}:${inst.endpoint_port}` : String(inst.endpoint_host)
}

export function instanceRows(instances) {
  return (Array.isArray(instances) ? instances : []).map((i) => ({
    id: String(i.id),
    title: i.label || String(i.id),
    sub: [endpointText(i), i.enabled === true ? '' : 'выключен'].filter(Boolean).join(' · '),
    enabled: i.enabled === true,
  }))
}

export function selfhostedIssueRows(instances) {
  return instanceRows(instances).filter((r) => r.enabled)
}

export function passwordHint(inst, { isNew }) {
  if (isNew) return 'Пароль SSH хранится на сервере и наружу не отдаётся.'
  return inst?.password_set ? 'Пароль задан. Пустое поле оставит его как есть.' : 'Пароль не задан.'
}

export const SELFHOSTED_TEXTS = {
  title: 'Свои VPN-серверы',
  intro: 'Серверы Amnezia, которыми вы управляете сами. С включённого сервера можно выпустить VPN-туннель для любого роутера — во вкладке «Свой сервер» его кабинета.',
  empty: 'Своих серверов пока нет.',
  add: 'Добавить сервер',
  loading: 'Читаем список серверов…',
  loadError: 'Не удалось прочитать список серверов.',
  notFound: 'Такого сервера больше нет — вернитесь к списку.',
  newTitle: 'Новый сервер',
  saved: 'Сохранено.',
  nothing: 'Ничего не изменилось.',
  checkButton: 'Проверить подключение',
  checking: 'Проверяем…',
  checkHint: 'Проверяется то, что уже сохранено: одна попытка входа по SSH.',
  issueIntro: 'Выберите сервер: на нём будет создан новый клиент, и конфиг сразу уйдёт на роутер.',
  noEnabled: 'Включённых серверов нет. Добавить или включить сервер можно в Парке — «Свои VPN-серверы».',
  adminOnly: 'Этот экран доступен только администратору.',
  keepNote: 'Сохранение проверяет только заполненность полей; подключение — отдельной кнопкой.',
}

export function checkResultView(resp) {
  const ok = resp?.ok === true
  return { tone: ok ? 'ok' : 'bad', text: resp?.message || (ok ? 'Подключение есть.' : 'Подключиться не удалось.') }
}

export function toggleLabel(inst) {
  return inst?.enabled ? 'Выключить' : 'Включить'
}

export function toggleDoneText(enabled) {
  return enabled ? 'Сервер включён.' : 'Сервер выключен: выпускать с него VPN-туннели нельзя, пока не включите.'
}

export function deleteConfirmPhrase(inst) {
  return inst?.label || inst?.id || ''
}

export function deleteInstanceSheetText(inst) {
  return {
    title: `Удалить сервер «${deleteConfirmPhrase(inst)}»?`,
    body: 'Сервер пропадёт из списка, и выпускать с него VPN-туннели станет нельзя. Сам сервер и уже выпущенные VPN-туннели на роутерах не трогаются.',
  }
}

const SELFHOSTED_ERRORS = {
  confirm_mismatch: 'Название сервера набрано не так.',
  not_found: 'Такого сервера больше нет — вернитесь к списку.',
  instance_not_found: 'Такого сервера больше нет — вернитесь к списку.',
  instance_exists: 'Сервер с таким коротким именем уже есть.',
}

export function selfhostedErrorText(err) {
  return err?.serverMessage || SELFHOSTED_ERRORS[err?.code] || 'Не получилось. Попробуйте ещё раз.'
}

// Поле формы, которое отверг сервер (400 invalid_field с field). Незнакомое
// поле -- пусто: тогда слова сервера показываются над формой, как прежде.
export function errorFieldKey(err) {
  if (err?.code !== 'invalid_field') return ''
  const key = typeof err.field === 'string' ? err.field : ''
  return key && (INSTANCE_KEYS.includes(key) || key === 'ssh_password') ? key : ''
}

export const SSH_WIPE_TEXT = 'Без адреса SSH сохранённый пароль будет удалён'

// Сервер без адреса SSH пароля не хранит: PUT с пустым ssh_host стирает
// пользователя, порт и пароль. Предупреждаем, только когда терять есть что.
export function sshWipeWarning(inst, values) {
  if (!inst?.ssh_host || inst.password_set !== true) return ''
  return trimmed(values?.ssh_host) === '' ? SSH_WIPE_TEXT : ''
}

export const SSH_CHANGED_TEXT = 'Адрес SSH изменён — введите пароль заново'

function sshLogin(v) {
  const host = typeof v?.ssh_host === 'string' ? v.ssh_host.trim() : ''
  const port = Number(String(v?.ssh_port ?? '').trim()) || 22
  const user = (typeof v?.ssh_user === 'string' ? v.ssh_user.trim() : '') || 'root'
  return { host, port, user }
}

// Вход SSH сменился: адрес, порт или пользователь (пустые -- 22 и root, как
// на сервере). Стёртый адрес -- не смена, у него своё предупреждение.
export function sshAddressChanged(inst, values) {
  const was = sshLogin(inst)
  const now = sshLogin(values)
  if (!was.host || !now.host) return false
  return was.host !== now.host || was.port !== now.port || was.user !== now.user
}

// Предупреждение под адресом SSH сохранённого сервера с паролем.
export function sshHostWarning(inst, values) {
  const wipe = sshWipeWarning(inst, values)
  if (wipe) return wipe
  return inst?.password_set === true && sshAddressChanged(inst, values) ? SSH_CHANGED_TEXT : ''
}

