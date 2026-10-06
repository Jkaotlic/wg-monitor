// Свои VPS (Amnezia на своём VPS): форма сервера, проверка, тело
// запроса и тексты. Только админ. SSH-пароль сюда приходит лишь снимком
// значений формы -- чтобы положить его в тело, когда он введён. Сервер его
// наружу не отдаёт: вместо него password_set.

import { placeText } from './places.js'
import { pluralRu } from './labels.js'
import { whenText } from './when.js'

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
    key: 'endpoint',
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
    key: 'paths',
    fold: true,
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
    key: 'ssh',
    fold: true,
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

// Итог свёрнутой группы формы (v0.50, спека п. 3.2): что там сейчас, не
// раскрывая. SSH -- «пользователь@адрес:порт» или «не задан»; пути -- сколько
// полей заданы своими значениями.
export function groupSummary(group, values) {
  const v = (key) => String(values?.[key] ?? '').trim()
  if (group?.key === 'ssh') {
    if (!v('ssh_host')) return 'не задан — контейнер на этой машине'
    return `${v('ssh_user') || 'root'}@${v('ssh_host')}:${v('ssh_port') || '22'}`
  }
  if (group?.key === 'paths') {
    const n = group.fields.filter((f) => v(f.key)).length
    return n ? `${n} ${pluralRu(n, 'поле задано', 'поля заданы', 'полей задано')}` : 'как у Amnezia по умолчанию'
  }
  return ''
}

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

// values -- текущая форма: сменённый вход SSH делает сохранённый пароль чужим.
export function passwordHint(inst, { isNew }, values = null) {
  if (isNew) return 'Пароль SSH хранится на сервере и наружу не отдаётся.'
  if (inst?.password_set && values && sshAddressChanged(inst, values)) return 'Сохранённый пароль был для прежнего входа — введите пароль заново.'
  return inst?.password_set ? 'Пароль задан. Пустое поле оставит его как есть.' : 'Пароль не задан.'
}

export const SELFHOSTED_TEXTS = {
  title: 'Серверы',
  intro: `Серверы Amnezia, которыми вы управляете сами. С включённого сервера можно выпустить VPN-туннель для любого роутера — в ${placeText('newTunnel')}.`,
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
  noEnabled: `Включённых серверов нет. Добавить или включить сервер можно здесь: ${placeText('vps')}.`,
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

// Ключ своего сервера (v0.55, B2): отпечаток запоминает первый удачный вход,
// смена ключа -- отказ входа. Блок только у сервера с адресом SSH.
// C1 (v0.56): отказанный ключ сервер помнит ожидающим -- карточка показывает
// «было» и «сейчас», админ подтверждает именно его; сброса больше нет.
export const HOSTKEY_TEXTS = {
  label: 'Ключ сервера',
  unknown: 'Ещё не запомнен: запомнится при следующем входе.',
  changed: 'Сервер предъявил другой ключ — входы на сервер остановлены, пока вы не подтвердите его.',
  was: 'Было',
  now: 'Сервер сейчас предъявляет',
  confirmed: 'Новый ключ сервера подтверждён — входы на сервер снова идут.',
}

export function hostKeyConfirmLabel(fingerprint) {
  return `Подтвердить ключ сервера «${fingerprint}»`
}

export function hostKeyView(inst, { now, timeZone } = {}) {
  if (!inst?.ssh_host) return null
  const str = (v) => (typeof v === 'string' ? v.trim() : '')
  const fingerprint = str(inst.ssh_host_key)
  const pending = str(inst.ssh_host_key_pending)
  const seenAt = pending ? whenText(inst.ssh_host_key_pending_at, { now, timeZone }) : ''
  return { fingerprint, pending, seenText: seenAt ? `Замечен ${seenAt}` : '' }
}

export function confirmHostKeySheetText(inst) {
  const view = hostKeyView(inst) || { fingerprint: '', pending: '' }
  return {
    title: `Подтвердить новый ключ сервера «${deleteConfirmPhrase(inst)}»?`,
    body: `Было: «${view.fingerprint}», сейчас: «${view.pending}». Подтверждайте, только если сервер переустанавливали и отпечаток совпадает с тем, что показывает сам сервер: иначе смена ключа может значить, что отвечает чужая машина.`,
  }
}

// Выданные подключения (v0.55, B3): список читается с сервера по кнопке,
// отзыв -- листом с набором названия сервера.
export const CLIENTS_TEXTS = {
  title: 'Выданные подключения',
  hint: 'Список читается с самого сервера: бот заходит на него только когда вы нажмёте кнопку.',
  show: 'Показать выданные подключения',
  refresh: 'Обновить список',
  loading: 'Читаем список с сервера…',
  empty: 'Выданных подключений нет.',
  revoke: 'Отозвать',
  revoking: 'Отзываем…',
  revoked: 'Подключение отозвано.',
}

function clientDate(iso) {
  const t = typeof iso === 'string' ? new Date(iso) : null
  if (!t || Number.isNaN(t.getTime())) return ''
  return t.toLocaleDateString('ru-RU', { timeZone: 'UTC', day: '2-digit', month: '2-digit', year: 'numeric' })
}

function inUseText(c) {
  const u = c?.in_use
  if (!u || !u.router || !u.tunnel) return ''
  // Самое новое подключение роутера -- «скорее всего»; прежние -- «возможно»:
  // файл в личку и сорвавшийся импорт не дают сказать наверняка, чем живёт туннель.
  if (u.likely === true) return `Скорее всего, этим подключением живёт VPN-туннель «${u.tunnel}» роутера «${u.router}»: после отзыва он перестанет работать.`
  return `Возможно, этим подключением живёт VPN-туннель «${u.tunnel}» роутера «${u.router}»: если это так, после отзыва он перестанет работать.`
}

export function clientRows(resp) {
  const list = Array.isArray(resp?.clients) ? resp.clients : []
  return list.map((c) => ({
    id: String(c.id ?? ''),
    name: String(c.name || c.address || ''),
    address: String(c.address ?? ''),
    date: clientDate(c.created_at),
    inUse: inUseText(c),
  }))
}

export function revokeSheetText(inst, client) {
  const base = 'Подключение будет убрано с сервера, и устройство или роутер, которому оно выдано, больше не сможет им пользоваться. Вернуть его нельзя — можно только выдать новое.'
  return {
    title: `Отозвать подключение «${client.name}»?`,
    body: client.inUse ? `${client.inUse} ${base}` : base,
  }
}

const SELFHOSTED_ERRORS = {
  client_not_found: 'Подключения уже нет на сервере — обновите список.',
  confirm_mismatch: 'Название сервера набрано не так.',
  not_found: 'Такого сервера больше нет — вернитесь к списку.',
  instance_not_found: 'Такого сервера больше нет — вернитесь к списку.',
  host_key_not_pending: 'Сервер уже предъявляет другой ключ — обновите экран и сверьте отпечаток заново',
  host_key_nothing_pending: 'Подтверждать нечего — ключ сервера уже доверенный или сменился адрес',
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

