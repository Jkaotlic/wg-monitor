import { COMMAND_GONE_TEXT } from './api.js'
import { commandErrorText } from './maintenance.js'

// Ошибка словами (v0.50, спека п. 1.4) -- одна функция на всё приложение.
// Порядок: своя фраза экрана по коду → общая фраза по коду → «сессия
// истекла» → русская фраза сервера → «нет связи» → запасная. err.message
// ApiError -- «/routers/7/commands failed: 400», внутренности запроса, и на
// экран не выходит никогда. Английская фраза сервера (общая middleware:
// «sign in required») -- тоже.
export const FALLBACK_ERROR_TEXT = 'Не получилось: сервер ответил ошибкой. Повторите; если повторится — напишите админу.'
export const OFFLINE_ERROR_TEXT = 'Сервер не ответил — проверьте связь и повторите.'
const SESSION_TEXT = 'Сессия истекла — откройте приложение заново.'

const RUSSIAN = /[А-Яа-яЁё]/

export function errorText(err, codes = {}) {
  if (err == null) return ''
  // Готовая строка (таймаут useCommand, фраза экрана) -- только русская.
  if (typeof err === 'string') return RUSSIAN.test(err) ? err : FALLBACK_ERROR_TEXT
  // api.js сам кладёт человеческую фразу для исчезнувшей команды (SEC-02).
  if (err.message === COMMAND_GONE_TEXT) return COMMAND_GONE_TEXT
  const code = typeof err.code === 'string' ? err.code : ''
  if (code && codes[code]) return codes[code]
  const common = code ? commandErrorText(code) : ''
  if (common) return common
  if (err.status === 401) return SESSION_TEXT
  const said = String(err.serverMessage ?? '').trim()
  if (said && RUSSIAN.test(said)) return said
  // Без статуса ответа не было вовсе: обрыв сети, KeenDNS не пустил.
  if (!err.status) return OFFLINE_ERROR_TEXT
  return FALLBACK_ERROR_TEXT
}

// Не-ok ответ агента на команду (result.status/result.output). Вывод агента --
// английский технический текст, а status -- голое «timeout»; ни то ни другое
// на экран не идёт. Экран говорит своей фразой, а русский вывод агента (если он
// есть) дописывается после неё.
export function agentReplyText(result, fallback) {
  const said = String(result?.output ?? '').trim()
  return said && RUSSIAN.test(said) ? `${fallback} ${said}` : fallback
}
