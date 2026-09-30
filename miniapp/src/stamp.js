import { whenText, sinceText } from './when.js'

// Время события -- одним модулем (when.js): «сегодня, 17:56», «27 сен, 17:56».
// Пояс -- того, кто смотрит; timeZone и now задают тесты.
export function stampText(iso, { timeZone, now } = {}) {
  return whenText(iso, { timeZone, now })
}

// Начало состояния: «сегодня с 17:56».
export function sinceStamp(iso, { timeZone, now } = {}) {
  return sinceText(iso, { timeZone, now })
}

// «N назад» считается между двумя временами сервера: часы телефона и Pi
// расходятся, а оба времени пришли в одном ответе.
export function secondsBetween(fromIso, toIso) {
  const from = Date.parse(fromIso ?? '')
  const to = Date.parse(toIso ?? '')
  if (Number.isNaN(from) || Number.isNaN(to)) return null
  return Math.max(0, Math.round((to - from) / 1000))
}
