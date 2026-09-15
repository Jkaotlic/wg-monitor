// Оживление агента с экрана «Парк»: строка роутера, поля листа, тексты.
// Здесь только чистые функции -- экран собирает из них строки.
//
// Решение оператора: «Полный автомат: пароль root или вход в панель
// awg-manager вводится один раз при постановке, хранится на Pi зашифрованным
// до успеха, отмены или срока, затем стирается. Переустановка запускается без
// участия человека.» Поэтому лист спрашивает пароль один раз и честно говорит,
// где он лежит; строка потом говорит только о ходе, пароля в ней нет.
//
// «Панель роутера» -- так её называет остальной мини-апп (agentConfig.js).
//
// Пре-флайт 15.09 (решение координатора): без пароля root переустановка
// отказывает (revive.Secrets.Usable), вход в панель его дополняет, но не
// заменяет. Поэтому root обязателен, остальные поля входа -- по желанию.
import { humanAge, pluralRu } from './labels.js'
import { isAway } from './agentUpdate.js'

export const REVIVE_NOT_CONFIGURED = 'Оживление агента не настроено на сервере.'
export const REVIVE_SECRET_NOTE = 'Пароль хранится на сервере зашифрованным до оживления, потом стирается.'
export const REVIVE_DEFAULT_DAYS = '30'
export const REVIVE_EXPIRY_OPTIONS = [
  { value: '7', label: '7 дней' },
  { value: '14', label: '14 дней' },
  { value: '30', label: '30 дней' },
]

const SECRET_NAMES = ['root_password', 'awgm_password', 'awgm_api_key']

function enabled(fleet) {
  return fleet?.revive_enabled === true
}

// «N мин назад» считается от generated_at того же ответа: часы телефона и
// сервера расходятся, а оба времени пришли с сервера.
function probeAgo(revive, generatedAt) {
  const at = Date.parse(revive?.last_probe_at ?? '')
  const now = Date.parse(generatedAt ?? '')
  if (Number.isNaN(at) || Number.isNaN(now)) return ''
  const sec = Math.max(0, Math.round((now - at) / 1000))
  return sec < 60 ? 'только что' : `${humanAge(sec)} назад`
}

function trimmed(v) {
  return String(v ?? '').trim()
}

export function reviveState(router, fleet) {
  const r = router?.revive ?? null
  const on = enabled(fleet)
  const canStart = on && isAway(router)
  switch (r?.status) {
    case 'waiting': {
      const ago = probeAgo(r, fleet?.generated_at)
      const probe = trimmed(r.last_probe_text)
      let text = ago ? `ждёт роутер · проверка ${ago}${probe ? `: ${probe}` : ''}` : 'ждёт роутер · проверок ещё не было'
      const tries = r.attempts ?? 0
      if (tries > 0) {
        const last = trimmed(r.last_error_text)
        text += ` · ${tries} ${pluralRu(tries, 'попытка', 'попытки', 'попыток')}${last ? `, прошлая: ${last}` : ''}`
      }
      return { tone: 'warn', text, canRevive: false, canCancel: on }
    }
    case 'running':
      // Установка уже идёт на роутере: отмена посреди неё оставила бы роутер
      // без агента вовсе, поэтому кнопки нет.
      return { tone: 'sig', text: 'оживляется…', canRevive: false, canCancel: false }
    case 'done':
      return { tone: 'ok', text: 'ожил', canRevive: canStart, canCancel: false }
    case 'failed':
      return { tone: 'danger', text: `не вышло: ${trimmed(r.last_error_text) || 'причина не названа'}`, canRevive: canStart, canCancel: false }
    case 'expired':
      return { tone: 'warn', text: 'срок истёк', canRevive: canStart, canCancel: false }
    default:
      // Нет намерения или его отменили: говорить нечего.
      return { tone: 'ok', text: '', canRevive: canStart, canCancel: false }
  }
}

// Одна строка на экран, а не на каждый роутер: причина общая (на сервере нет
// ключа), и три одинаковых фразы подряд читались бы как три разных беды.
export function reviveNotConfiguredLine(fleet) {
  if (fleet?.revive_enabled !== false) return ''
  return (fleet?.routers ?? []).some((r) => isAway(r)) ? REVIVE_NOT_CONFIGURED : ''
}

function asksPanelAddress(router) {
  return router?.panel_address_known === false
}

export function reviveSheetText(router) {
  const name = router?.nickname ?? ''
  const parts = [
    `Агент на «${name}» установится заново, когда роутер выйдет на связь: сервер сам проверяет, появился ли он, и запускает установку без вас.`,
    'Нужен пароль root роутера. Вход в панель роутера — логин с паролем или ключ — необязателен.',
  ]
  if (asksPanelAddress(router)) parts.push('Адрес панели у роутера не записан — укажите его.')
  return { title: `Оживить агент на «${name}»?`, body: parts.join(' ') }
}

export function reviveFields(router) {
  const fields = [
    { name: 'root_password', label: 'Пароль root', type: 'password' },
    { name: 'awgm_login', label: 'Логин панели роутера (необязательно)', type: 'text' },
    { name: 'awgm_password', label: 'Пароль панели роутера (необязательно)', type: 'password' },
    { name: 'awgm_api_key', label: 'Ключ панели роутера (необязательно)', type: 'password' },
  ]
  if (asksPanelAddress(router)) {
    fields.push({ name: 'awgm_url', label: 'Адрес панели роутера', type: 'text', placeholder: 'https://192.168.1.1' })
  }
  fields.push({ name: 'expires_days', label: 'Ждать роутер', type: 'select', options: REVIVE_EXPIRY_OPTIONS, initial: REVIVE_DEFAULT_DAYS })
  return fields
}

export function reviveReady(router) {
  const needsAddress = asksPanelAddress(router)
  return (values = {}) => {
    // Сервер обрезает пароль root по краям: из одних пробелов он пустой.
    if (trimmed(values.root_password) === '') return false
    return !needsAddress || trimmed(values.awgm_url) !== ''
  }
}

// Тело запроса. Пароли не обрезаются: пробел может быть частью пароля.
export function reviveRequestBody(values = {}, typed, router) {
  const body = { confirm: typed, expires_days: Number(values.expires_days || REVIVE_DEFAULT_DAYS) }
  for (const name of SECRET_NAMES) {
    const v = name === 'awgm_api_key' ? trimmed(values[name]) : values[name] ?? ''
    if (v !== '') body[name] = v
  }
  const login = trimmed(values.awgm_login)
  if (login) body.awgm_login = login
  const address = trimmed(values.awgm_url)
  if (asksPanelAddress(router) && address) body.awgm_url = address
  return body
}

const ERROR_TEXT = {
  confirm_mismatch: 'Имя роутера набрано неверно — оживление не поставлено.',
  revive_disabled: REVIVE_NOT_CONFIGURED,
  no_awgm_url: 'У роутера не записан адрес панели — укажите его.',
  invalid_awgm_url: 'Адрес панели должен начинаться с http:// или https://.',
  awgm_url_already_set: 'Адрес панели у роутера уже записан — закройте лист и откройте заново.',
  no_credentials: 'Нужен пароль root роутера.',
  agent_alive: 'Агент на роутере отвечает — оживлять нечего.',
  not_found: 'Роутер не найден — закройте экран и откройте заново.',
  revive_running: 'Оживление уже идёт — дождитесь итога.',
  router_not_found: 'Роутер не найден.',
}

// Как в agentUpdate.js: сообщению сервера доверяем только для кодов, которые
// пишут хендлеры мини-аппа по-русски. 401 из middleware -- английский.
const SERVER_MESSAGE_CODES = new Set(['bad_request', 'internal', 'not_configured'])

export function reviveErrorText(err) {
  const known = ERROR_TEXT[err?.code]
  if (known) return known
  if (err?.status === 401) return 'Сессия истекла — откройте приложение заново.'
  if (SERVER_MESSAGE_CODES.has(err?.code)) return trimmed(err?.serverMessage)
  return ''
}

// Дата -- по UTC-частям: у сервера и телефона разные пояса, а день окончания
// и так округлён сроком в сутках.
function dayOf(iso) {
  const t = new Date(iso ?? '')
  if (Number.isNaN(t.getTime())) return ''
  const pad = (n) => String(n).padStart(2, '0')
  return `${pad(t.getUTCDate())}.${pad(t.getUTCMonth() + 1)}.${t.getUTCFullYear()}`
}

export function reviveDoneText(resp, nickname) {
  if (resp?.status === 'running') return `Оживление «${nickname}» запущено: роутер на связи, агент ставится заново.`
  const until = dayOf(resp?.expires_at)
  return `Оживление «${nickname}» поставлено: агент переустановится, когда роутер выйдет на связь.${until ? ` Ждём до ${until}.` : ''}`
}

export function reviveCancelSheetText(router) {
  return {
    title: `Отменить оживление агента на «${router?.nickname ?? ''}»?`,
    body: 'Сервер перестанет ждать роутер и сотрёт пароль. Поставить оживление можно будет заново.',
  }
}

export function reviveCancelDoneText(resp, nickname) {
  if (resp?.cleared) return `Оживление «${nickname}» отменено, пароль стёрт.`
  return `Отменять было нечего: оживление «${nickname}» уже завершилось или снято.`
}
