// «Подключение агента»: как сервер добирается до роутера. Форма, перенесённая
// из «Edit agent» старого дашборда (dashboard_static/app.js:1627-1684).
// Пароли здесь не живут вовсе: сервер их не хранит.
import { AWGM_AUTH_OPTIONS } from './provisionWizard.js'
import { validHttpURL } from './formRules.js'

export const CONNECTION_KEYS = ['awgm_url', 'awgm_auth', 'ssh_host', 'ssh_port', 'ssh_user', 'deploy_mode', 'arch', 'ring', 'expected_mac']

const EMPTY = { value: '', label: 'не задано' }

export const CONNECTION_GROUPS = [
  {
    title: 'Панель awg-manager',
    fields: [
      { key: 'awgm_url', label: 'Адрес панели', kind: 'text', placeholder: 'https://router.example.com' },
      { key: 'awgm_auth', label: 'Вход в панель', kind: 'select', options: [EMPTY, ...AWGM_AUTH_OPTIONS] },
    ],
  },
  {
    title: 'SSH',
    fields: [
      { key: 'ssh_host', label: 'Адрес', kind: 'text', placeholder: '198.51.100.7' },
      { key: 'ssh_port', label: 'Порт', kind: 'text', placeholder: '222', inputMode: 'numeric' },
      { key: 'ssh_user', label: 'Пользователь', kind: 'text', placeholder: 'root' },
    ],
  },
  {
    title: 'Раскатка',
    fields: [
      {
        key: 'deploy_mode',
        label: 'Способ',
        kind: 'select',
        options: [
          EMPTY,
          { value: 'awgm', label: 'через панель awg-manager' },
          { value: 'pull', label: 'агент скачивает сам' },
          { value: 'ssh', label: 'по SSH' },
          { value: 'deferred-awgm', label: 'через панель, когда роутер на связи' },
        ],
      },
      { key: 'arch', label: 'Архитектура', kind: 'select', options: [EMPTY, { value: 'arm64', label: 'arm64' }, { value: 'mipsle', label: 'mipsle' }] },
      { key: 'ring', label: 'Канал', kind: 'select', options: [EMPTY, { value: 'stable', label: 'стабильный' }, { value: 'rc', label: 'пробный (rc)' }] },
    ],
  },
  {
    title: 'Проверка роутера',
    fields: [{ key: 'expected_mac', label: 'Ожидаемый MAC', kind: 'text', placeholder: 'aa:bb:cc:dd:ee:ff' }],
  },
]

export const CONNECTION_TEXTS = {
  intro: 'Как сервер добирается до роутера: панель awg-manager, SSH и способ раскатки агента. Паролей здесь нет — сервер их не хранит.',
  keepNote: 'Пустое поле оставляет прежнее значение.',
  loadError: 'Не удалось прочитать подключение агента.',
  saved: 'Сохранено.',
  nothing: 'Ничего не изменилось.',
}

export function connectionFormValues(resp) {
  const values = {}
  for (const key of CONNECTION_KEYS) {
    const v = resp?.[key]
    values[key] = v == null ? '' : String(v)
  }
  values.ssh_port = resp?.ssh_port ? String(resp.ssh_port) : ''
  return values
}

// Текущее значение, которого нет в списке (старая запись, новый режим
// сервера), остаётся выбранным: иначе select молча показал бы «не задано»,
// и сохранение стёрло бы то, чего человек не трогал.
//
// Значение уже есть -- «не задано» не предлагаем: пустое поле сервер
// понимает как «оставить прежнее», и очистка молча не сработала бы.
export function connectionSelectOptions(field, current) {
  const all = field?.options ?? []
  if (!current) return all
  const options = all.filter((o) => o.value !== '')
  if (options.some((o) => o.value === current)) return options
  return [...options, { value: current, label: current }]
}

const MAC_RE = /^([0-9a-f]{2}[:-]){5}[0-9a-f]{2}$/i

function trimmed(v) {
  return typeof v === 'string' ? v.trim() : ''
}

export function validateConnection(values) {
  const url = trimmed(values.awgm_url)
  if (url && !validHttpURL(url)) return 'Адрес панели должен начинаться с https:// или http://'
  const port = trimmed(values.ssh_port)
  if (port && (!/^\d+$/.test(port) || Number(port) < 1 || Number(port) > 65535)) return 'Порт SSH — число от 1 до 65535'
  const mac = trimmed(values.expected_mac)
  if (mac && !MAC_RE.test(mac)) return 'MAC пишется так: aa:bb:cc:dd:ee:ff'
  return ''
}

export function connectionRequestBody(values) {
  const body = {}
  for (const key of CONNECTION_KEYS) body[key] = trimmed(values[key])
  body.ssh_port = body.ssh_port === '' ? 0 : Number(body.ssh_port)
  return body
}

export function connectionChanged(a, b) {
  return JSON.stringify(connectionRequestBody(a ?? {})) !== JSON.stringify(connectionRequestBody(b ?? {}))
}

const ERRORS = {
  invalid_arch: 'Архитектура: arm64 или mipsle',
  invalid_awgm_url: 'Адрес панели должен начинаться с https:// или http://',
  invalid_kind: 'Неизвестный тип роутера',
}

export function connectionErrorText(err) {
  return err?.serverMessage || ERRORS[err?.code] || 'Не удалось сохранить. Попробуйте ещё раз.'
}
