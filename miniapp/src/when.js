import { humanAge } from './labels.js'

// Одно время на всё приложение (v0.50, спека п. 3.5): «сегодня, 17:56»,
// «вчера, 17:56», «27 сен, 17:56», год -- только не текущий. Пояс -- того,
// кто смотрит; now и timeZone задают тесты. Раньше рядом жили «12.09 14:20»,
// «12.09 в 14:20» и «29.09.2026, 17:56» -- три записи одного и того же.
const MONTHS = ['янв', 'фев', 'мар', 'апр', 'мая', 'июн', 'июл', 'авг', 'сен', 'окт', 'ноя', 'дек']

function parts(date, timeZone) {
  const p = new Intl.DateTimeFormat('ru-RU', {
    year: 'numeric',
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
    timeZone,
  }).formatToParts(date)
  const num = (type) => Number(p.find((x) => x.type === type)?.value)
  return { y: num('year'), m: num('month'), d: num('day'), hh: String(num('hour')).padStart(2, '0'), mi: String(num('minute')).padStart(2, '0') }
}

const sameDay = (a, b) => a.y === b.y && a.m === b.m && a.d === b.d

export function whenText(iso, { now = Date.now(), timeZone, absolute = false } = {}) {
  if (!iso) return ''
  const t = new Date(iso)
  if (Number.isNaN(t.getTime())) return ''
  const at = parts(t, timeZone)
  const today = parts(new Date(now), timeZone)
  const time = `${at.hh}:${at.mi}`
  if (!absolute && sameDay(at, today)) return `сегодня, ${time}`
  if (!absolute && sameDay(at, parts(new Date(now - 86_400_000), timeZone))) return `вчера, ${time}`
  const year = at.y === today.y ? '' : ` ${at.y}`
  return `${at.d} ${MONTHS[at.m - 1]}${year}, ${time}`
}

// Отсчёт «N мин назад» -- один хелпер (переехал из awg3Panel.js).
export function agoText(sec) {
  if (typeof sec !== 'number' || !Number.isFinite(sec) || sec < 0) return ''
  if (sec < 60) return 'только что'
  return `${humanAge(sec)} назад`
}

// «с» -- начало состояния: «сегодня с 17:56», «вчера с 06:00», «27 сен с
// 17:56». Запятая из whenText здесь читалась бы «с сегодня, 17:56».
export function sinceText(iso, opts) {
  return whenText(iso, opts).replace(', ', ' с ')
}
