// Логика входа в веб-управление -- без разметки, чтобы проверять её без DOM.

export const SESSION_EXPIRED_TEXT = 'Сессия закончилась — войдите снова'
export const ADMIN_NOT_CONFIGURED_TEXT = 'На сервере не задан администратор — вход в веб-управление невозможен'

const LINK_DEAD_TEXT = 'Ссылка больше не действует — попросите новую.'
const RATE_LIMITED_TEXT = 'Слишком много попыток, подождите минуту'
const SERVER_DOWN_TEXT = 'Сервер не отвечает'

// Личная ссылка приходит как /dashboard/login#token=<raw>. Токен стирается из
// адреса сразу, до любого запроса: при ошибке обмена он не должен остаться ни
// в строке адреса, ни в истории браузера.
export function takeHashToken(loc = window.location, hist = window.history) {
  const hash = loc?.hash ?? ''
  if (!hash.startsWith('#')) return ''
  const token = new URLSearchParams(hash.slice(1)).get('token') ?? ''
  if (!token) return ''
  hist.replaceState(null, '', (loc.pathname ?? '') + (loc.search ?? ''))
  return token
}

export function redeemFromHash({ location, history, redeem }) {
  const token = takeHashToken(location, history)
  if (!token) return null
  return redeem(token)
}

function isNetworkError(err) {
  return !(err && typeof err.status === 'number')
}

// source: 'token' -- форма, 'link' -- личная ссылка. Ответ бэкенда на
// неверный токен английский, поэтому у формы фраза своя; у ссылки сервер
// говорит по-русски и знает причину лучше клиента.
export function loginErrorText(err, source) {
  if (isNetworkError(err)) return SERVER_DOWN_TEXT
  if (err.status === 429) return err.serverMessage || RATE_LIMITED_TEXT
  if (err.status === 401) {
    if (err.code === 'admin_not_configured') return ADMIN_NOT_CONFIGURED_TEXT
    return source === 'link' ? err.serverMessage || LINK_DEAD_TEXT : 'Токен не подошёл'
  }
  return 'Не получилось войти. Попробуйте ещё раз.'
}

// Что показать, если при старте не удалось получить сессию или список
// роутеров. В Telegram -- как было: одна ошибка «откройте заново». В web
// «откройте из Telegram» было бы неправдой: там либо вход, либо сервер молчит.
export function bootFailure(err, mode) {
  if (mode !== 'web') return { status: 'error', notice: '' }
  if (!isNetworkError(err) && err.status === 401) {
    return { status: 'login', notice: err.code === 'admin_not_configured' ? ADMIN_NOT_CONFIGURED_TEXT : '' }
  }
  return { status: 'down', notice: '' }
}
