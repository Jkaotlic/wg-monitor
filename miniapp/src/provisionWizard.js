// Мастер «Добавить роутер»: шаги, проверка, сводка и тело запроса. Экран
// (ProvisionWizard.jsx) держит значения в своём состоянии -- пароли не
// попадают ни в навигацию, ни в адрес.
//
// Группа Telegram и тема не спрашиваются (группы уходят, решение оператора
// 14.09): уведомления идут в личку, владельца назначают потом в «Доступ».
import { NICKNAME_RE, NICKNAME_RULE, normalizeAgentVersion, validHttpURL } from './formRules.js'

export const PROVISION_PATHS = [
  {
    value: 'install',
    title: 'Установить агента сейчас',
    sub: 'Сервер сам зайдёт на роутер через панель awg-manager и поставит агента. Нужны адрес панели и пароль root.',
  },
  {
    value: 'token',
    title: 'Только выдать токен',
    sub: 'Агента поставите позже вручную: экран покажет токен и команду установки.',
  },
]

export const AGENT_KINDS = [
  { value: 'static', title: 'Дома', sub: 'стационарный роутер, всегда на связи' },
  { value: 'mobile', title: 'В машине', sub: 'мобильный роутер, бывает без связи' },
]

// Значения -- как у сервера и старого дашборда (index.html:218-223).
export const AWGM_AUTH_OPTIONS = [
  { value: 'web', label: 'Вход в веб (логин и пароль)' },
  { value: 'api-key', label: 'Ключ API' },
  { value: 'none', label: 'Без входа' },
]

export const PROVISION_SECRET_NOTE = 'Пароль уходит на сервер один раз и не сохраняется.'

export const STEP_TITLES = {
  path: 'Как добавить',
  router: 'Роутер',
  access: 'Доступ к роутеру',
  confirm: 'Подтверждение',
}

export const WIZARD_SECRET_KEYS = ['rootPassword', 'awgmPassword', 'awgmAPIKey']

export const TOKEN_TEXTS = {
  once: 'Токен показывается один раз: закроете экран — увидеть его снова будет нельзя.',
  commandHint: 'Выполните на роутере — в SSH или в терминале панели awg-manager:',
  after: 'Владельца роутеру назначают потом — во вкладке «Управление» → «Доступ».',
}

const INSTALL_STEPS = ['path', 'router', 'access', 'confirm']
const TOKEN_STEPS = ['path', 'router', 'confirm']

export function wizardSteps(path) {
  return path === 'token' ? TOKEN_STEPS : INSTALL_STEPS
}

export function initialWizardValues() {
  return {
    path: '',
    nickname: '',
    agentKind: 'static',
    awgmURL: '',
    awgmAuth: 'web',
    rootPassword: '',
    awgmLogin: '',
    awgmPassword: '',
    awgmAPIKey: '',
    version: '',
  }
}

export function clearSecrets(values) {
  const next = { ...values }
  for (const key of WIZARD_SECRET_KEYS) next[key] = ''
  return next
}

function trimmed(v) {
  return typeof v === 'string' ? v.trim() : ''
}

export function stepError(step, values) {
  switch (step) {
    case 'path':
      return values.path === 'install' || values.path === 'token' ? '' : 'Выберите, как добавить роутер.'
    case 'router':
      if (!NICKNAME_RE.test(trimmed(values.nickname))) return NICKNAME_RULE
      return AGENT_KINDS.some((k) => k.value === values.agentKind) ? '' : 'Выберите, где стоит роутер.'
    case 'access': {
      if (!validHttpURL(values.awgmURL)) return 'Нужен адрес панели awg-manager: https://…'
      // Пароль из одних пробелов сервер считает пустым; сам пароль уходит как введён.
      if (trimmed(values.rootPassword) === '') return 'Нужен пароль root'
      if (!AWGM_AUTH_OPTIONS.some((o) => o.value === values.awgmAuth)) return 'Выберите способ входа в панель.'
      if (values.awgmAuth === 'web' && (trimmed(values.awgmLogin) === '' || (values.awgmPassword ?? '') === '')) {
        return 'Для входа в веб нужны логин и пароль панели.'
      }
      if (values.awgmAuth === 'api-key' && trimmed(values.awgmAPIKey) === '') return 'Нужен ключ API панели.'
      if (trimmed(values.version) !== '' && !normalizeAgentVersion(values.version)) return 'Версия пишется так: v0.36.0. Пусто — последняя.'
      return ''
    }
    default:
      return ''
  }
}

export function nextStep(path, step) {
  const steps = wizardSteps(path)
  const i = steps.indexOf(step)
  return i >= 0 && i < steps.length - 1 ? steps[i + 1] : null
}

export function prevStep(path, step) {
  const steps = wizardSteps(path)
  const i = steps.indexOf(step)
  return i > 0 ? steps[i - 1] : null
}

export function stepPosition(path, step) {
  const steps = wizardSteps(path)
  return { index: steps.indexOf(step) + 1, total: steps.length }
}

function titleOf(list, value) {
  return list.find((x) => x.value === value)?.title ?? ''
}

export function provisionSummary(values) {
  const rows = [
    { label: 'Способ', value: titleOf(PROVISION_PATHS, values.path) },
    { label: 'Имя', value: trimmed(values.nickname) },
    { label: 'Где стоит', value: titleOf(AGENT_KINDS, values.agentKind) },
  ]
  if (values.path !== 'install') return rows
  rows.push(
    { label: 'Панель awg-manager', value: trimmed(values.awgmURL) },
    { label: 'Вход в панель', value: AWGM_AUTH_OPTIONS.find((o) => o.value === values.awgmAuth)?.label ?? '' },
    { label: 'Пароль root', value: trimmed(values.rootPassword) ? 'введён' : 'не введён' },
    { label: 'Версия агента', value: normalizeAgentVersion(values.version) || trimmed(values.version) || 'последняя' },
  )
  return rows
}

// Пароли не обрезаются: пробел может быть частью пароля. В тело уходят только
// секреты выбранного способа входа -- лишний пароль не должен покидать экран.
export function provisionRequestBody(values, typed) {
  const body = {
    kind: values.path === 'token' ? 'register' : 'provision',
    nickname: trimmed(values.nickname),
    agent_kind: values.agentKind,
    confirm: typed,
  }
  if (body.kind === 'register') return body
  body.awgm_url = trimmed(values.awgmURL)
  body.awgm_auth = values.awgmAuth
  body.root_password = values.rootPassword
  body.version = normalizeAgentVersion(values.version)
  if (values.awgmAuth === 'web') {
    body.awgm_login = trimmed(values.awgmLogin)
    body.awgm_password = values.awgmPassword
  }
  if (values.awgmAuth === 'api-key') body.awgm_api_key = trimmed(values.awgmAPIKey)
  return body
}

const ERROR_STEP = {
  invalid_nickname: 'router',
  invalid_kind: 'router',
  provision_already_running: 'router',
  no_awgm_url: 'access',
  root_password_required: 'access',
  latest_version_failed: 'access',
  invalid_awgm_url: 'access',
  nickname_taken: 'router',
  checksums_failed: 'access',
}

export function provisionErrorStep(err, path) {
  const step = ERROR_STEP[err?.code]
  if (!step || !wizardSteps(path).includes(step)) return 'confirm'
  return step
}

const ERROR_TEXTS = {
  confirm_mismatch: 'Подтверждение не совпало',
  provision_not_configured: 'Установка агентов на сервере не настроена',
  root_password_required: 'Нужен пароль root',
  no_awgm_url: 'Нужен адрес панели awg-manager',
  invalid_nickname: 'Имя роутера: латиница, цифры и дефис',
  invalid_kind: 'Неизвестный тип роутера',
  provision_already_running: 'Установка на этот роутер уже идёт',
  latest_version_failed: 'Не удалось узнать последнюю версию — повторите через минуту',
  checksums_failed: 'Не удалось скачать контрольные суммы релиза',
  no_public_base_url: 'У сервера не задан публичный адрес — агенту некуда отправлять отчёты',
}

export function provisionErrorText(err) {
  return err?.serverMessage || ERROR_TEXTS[err?.code] || 'Не получилось. Попробуйте ещё раз.'
}

export function tokenResultView(resp) {
  const s = (v) => (typeof v === 'string' ? v : '')
  return {
    nickname: s(resp?.nickname),
    token: s(resp?.raw_token),
    backendURL: s(resp?.backend_url),
    installCommand: s(resp?.install_command),
  }
}
