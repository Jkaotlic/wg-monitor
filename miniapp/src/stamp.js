// Время события словами: «12.09 14:20». Пояс -- того, кто смотрит (так же
// показывал время старый дашборд); timeZone задают тесты.
export function stampText(iso, { timeZone } = {}) {
  if (!iso) return ''
  const t = new Date(iso)
  if (Number.isNaN(t.getTime())) return ''
  const parts = new Intl.DateTimeFormat('ru-RU', {
    day: '2-digit',
    month: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
    timeZone,
  }).formatToParts(t)
  const get = (type) => parts.find((p) => p.type === type)?.value ?? ''
  return `${get('day')}.${get('month')} ${get('hour')}:${get('minute')}`
}

// «N назад» считается между двумя временами сервера: часы телефона и Pi
// расходятся, а оба времени пришли в одном ответе.
export function secondsBetween(fromIso, toIso) {
  const from = Date.parse(fromIso ?? '')
  const to = Date.parse(toIso ?? '')
  if (Number.isNaN(from) || Number.isNaN(to)) return null
  return Math.max(0, Math.round((to - from) / 1000))
}
