// Переустановка агента сейчас и перенаправление агента на другой бэкенд:
// поля листа, готовность, тела запросов, тексты. Только чистые функции.
//
// Оба действия запускают задание на сервере (SSH на роутер) и отвечают
// {job_id}; дальше человек смотрит «Ход работы» (часть 2, openLayer('job')). Пароли вводятся
// полями листа и живут только в Sheet.jsx: сюда они приходят снимком на
// момент нажатия и уходят в тело одного POST.
//
// «Панель роутера» -- так её называет остальной мини-апп (revive.js).
import { isAway } from './agentUpdate.js'
import { normalizeVersion } from './agentVersionPick.js'
import { validAgentVersion } from './formRules.js'
import { jobTitle } from './jobSteps.js'

export const JOB_SECRET_NOTE = 'Пароли уходят на сервер один раз и не сохраняются.'

const VERSION_EXAMPLE = 'v0.36.0'
const URL_HINT = 'Нужен адрес с https://, например https://wg.example.com.'

const trimmed = (v) => String(v ?? '').trim()

// Набранная версия: «0.35.0» дописывается до «v0.35.0», годность -- общим
// правилом formRules.js (vN.N.N, vN.N.N-rcN). Негодная -- пустая строка.
function goodVersion(v) {
  const n = normalizeVersion(v)
  return n && validAgentVersion(n) ? n : ''
}

const PANEL_FIELDS = [
  { name: 'awgm_login', label: 'Логин панели роутера (необязательно)', type: 'text' },
  { name: 'awgm_password', label: 'Пароль панели роутера (необязательно)', type: 'password' },
  { name: 'awgm_api_key', label: 'Ключ панели роутера (необязательно)', type: 'password' },
]

// Пароли не обрезаются: пробел может быть частью пароля. Логин и ключ --
// обрезаются: пробел по краям там только от копирования.
function panelBody(values) {
  return {
    awgm_login: trimmed(values?.awgm_login),
    awgm_password: values?.awgm_password ?? '',
    awgm_api_key: trimmed(values?.awgm_api_key),
  }
}

// Только роутеру на связи (спека, п. 7): спящему и молчащему есть «Оживить
// агент», который дождётся его появления.
export function reinstallAllowed(router) {
  return Boolean(router) && !isAway(router)
}

export function reinstallSheetText(router) {
  const name = router?.nickname ?? ''
  return {
    title: `Переустановить агент на «${name}» сейчас?`,
    body:
      `Сервер зайдёт на «${name}» по SSH и поставит агента заново. Проверки замолчат на пару минут, VPN-туннели не трогаются. ` +
      'Нужен пароль root; вход в панель роутера — по желанию. Ход установки откроется сразу после запуска.',
  }
}

function versionHint(values) {
  const raw = trimmed(values?.version)
  if (!raw) return 'Пусто — последняя версия.'
  return goodVersion(raw) ? '' : `Версия пишется так: ${VERSION_EXAMPLE}.`
}

export function reinstallFields() {
  return [
    { name: 'root_password', label: 'Пароль root', type: 'password' },
    ...PANEL_FIELDS,
    { name: 'version', label: 'Версия агента', type: 'text', placeholder: 'последняя', hint: versionHint },
  ]
}

export function reinstallReady(values = {}) {
  if (trimmed(values.root_password) === '') return false
  const raw = trimmed(values.version)
  return raw === '' || goodVersion(raw) !== ''
}

export function reinstallRequestBody(values = {}, typed) {
  return {
    root_password: values.root_password ?? '',
    ...panelBody(values),
    version: goodVersion(values.version),
    confirm: typed,
  }
}

export function reinstallJobTitle(router) {
  return jobTitle('repair_reinstall', router?.nickname ?? '')
}

export function backendURLCheck(value) {
  const raw = trimmed(value)
  if (!raw) return { ok: true, hint: 'Пусто — текущий публичный адрес этого сервера.' }
  try {
    const u = new URL(raw)
    if (u.protocol === 'https:' && u.hostname) return { ok: true, hint: '' }
  } catch {
    // не адрес вовсе -- та же подсказка
  }
  return { ok: false, hint: URL_HINT }
}

export function repointSheetText(router) {
  const name = router?.nickname ?? ''
  return {
    title: `Перенаправить агента «${name}» на другой сервер?`,
    body:
      'Агент начнёт отправлять отчёты на другой сервер. Этот сервер перестанет его видеть. ' +
      'Сервер зайдёт на роутер по SSH и перепишет адрес в настройках агента. Нужен пароль root.',
  }
}

export function repointFields() {
  return [
    { name: 'root_password', label: 'Пароль root', type: 'password' },
    {
      name: 'new_backend_url',
      label: 'Новый адрес сервера',
      type: 'text',
      placeholder: 'https://wg.example.com',
      hint: (values) => backendURLCheck(values?.new_backend_url).hint,
    },
    ...PANEL_FIELDS,
  ]
}

export function repointReady(values = {}) {
  return trimmed(values.root_password) !== '' && backendURLCheck(values.new_backend_url).ok
}

export function repointRequestBody(values = {}, typed) {
  return {
    root_password: values.root_password ?? '',
    new_backend_url: trimmed(values.new_backend_url),
    ...panelBody(values),
    confirm: typed,
  }
}

export function repointJobTitle(router) {
  return jobTitle('repair_repoint', router?.nickname ?? '')
}

// Новые маршруты отвечают русским message (спека, «Ошибки») -- его и
// показываем. 401 приходит из общей middleware по-английски -- своей фразой.
// Пустая строка -- лист скажет общее «Не получилось».
export function jobStartErrorText(err) {
  if (err?.status === 401) return 'Сессия истекла — войдите заново.'
  return trimmed(err?.serverMessage)
}
