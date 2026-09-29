import { whenText } from './when.js'

// Часы сервера на экране (MINI-10).
//
// Часы телефона врут: спешат, отстают, стоят в чужом поясе. Возраст
// «измерено N назад», посчитанный по ним, врёт вместе с ними. Сервер в каждом
// ответе про роутер кладёт пару last_seen_at + last_seen_age_sec -- возраст
// посчитан по ЕГО часам в момент ответа, значит их сумма -- «сейчас» сервера.
// Разница с часами телефона в момент получения -- сдвиг, которым и
// поправляется любой возраст на экране.
//
// receivedMs -- когда ответ пришёл: сдвиг снимается сразу, а не при
// отрисовке, иначе ответ, пролежавший десять минут, дал бы сдвиг в десять
// минут.
export function serverClockOffset(router, receivedMs = Date.now()) {
  const at = Date.parse(router?.last_seen_at ?? '')
  const age = router?.last_seen_age_sec
  if (Number.isNaN(at) || typeof age !== 'number') return null
  return at + age * 1000 - receivedMs
}

// Возраст метки ts в секундах по часам сервера. Без сдвига -- null: тогда
// экран пишет время словами (оно от часов телефона не зависит), а не
// придумывает возраст.
export function ageByServerClock(ts, { clockOffsetMs = null, nowMs = Date.now() } = {}) {
  const t = Date.parse(ts ?? '')
  if (Number.isNaN(t) || typeof clockOffsetMs !== 'number') return null
  return Math.max(0, Math.round((nowMs + clockOffsetMs - t) / 1000))
}

export function clockTime(ts) {
  return whenText(ts)
}
