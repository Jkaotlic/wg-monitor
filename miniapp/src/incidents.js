// Происшествие на экране: фраза о последствии и строка «когда и сколько».
//
// Сворачивание живёт на бэкенде -- клиент видит только пятьсот новейших строк
// и посчитать по ним неделю не может. Здесь остаётся то, что и должно быть на
// клиенте: как это назвать по-русски и как разложить по дням.
import { incidentWhatPlain, humanAge } from './labels.js'

// День -- по местным часам человека, а не по UTC (MINI-08): время строк
// показано местным, и группировать их по UTC значило класть вечернее
// происшествие во «вчера» (или ночное -- в «завтра»).
function dayKey(d) {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const dd = String(d.getDate()).padStart(2, '0')
  return `${y}-${m}-${dd}`
}

export function localDay(ts) {
  const d = new Date(ts ?? '')
  if (Number.isNaN(d.getTime())) return ''
  return dayKey(d)
}

// n дней назад от nowMs по местному календарю (переход на летнее время не
// сдвигает день, в отличие от вычитания 86 400 000).
function localDayBack(nowMs, n) {
  const now = new Date(nowMs)
  return dayKey(new Date(now.getFullYear(), now.getMonth(), now.getDate() - n))
}

export function dayTitle(day, nowMs = Date.now()) {
  if (day === localDayBack(nowMs, 0)) return 'Сегодня'
  if (day === localDayBack(nowMs, 1)) return 'Вчера'
  const [y, m, d] = day.split('-').map(Number)
  return new Date(y, m - 1, d).toLocaleDateString('ru-RU', { day: 'numeric', month: 'long' })
}

function hhmm(iso) {
  return new Date(iso).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' })
}

export function incidentLine(incident) {
  const what = incidentWhatPlain(incident.check_name)
  const from = hhmm(incident.from)
  const down = humanAge(incident.down_sec ?? 0)

  let detail
  if (incident.ongoing) {
    detail = `с ${from}, идёт уже ${down}`
  } else if ((incident.flaps ?? 1) > 1) {
    detail = `моргал ${incident.flaps} раз, с ${from} до ${hhmm(incident.to)} · всего не работал ${down}`
  } else {
    detail = `в ${from}, ${down}`
  }

  return {
    title: what,
    detail,
    // Идущая поломка красная: она про сейчас. Прошедшая жёлтая -- это уже
    // история, и красить её тревогой значило бы звать чинить починенное.
    tone: incident.ongoing ? 'bad' : 'warn',
    ongoing: Boolean(incident.ongoing),
  }
}

// Тихий день обязан остаться на экране: неделя без единой строки выглядит как
// сломанная лента, а не как неделя, когда всё работало.
export function groupIncidentsByDay(incidents = [], days = 7, nowMs = Date.now()) {
  const byDay = new Map()
  for (const inc of incidents) {
    const day = localDay(inc.from)
    if (!byDay.has(day)) byDay.set(day, [])
    byDay.get(day).push(inc)
  }

  const out = []
  for (let i = 0; i < days; i++) {
    const day = localDayBack(nowMs, i)
    const list = byDay.get(day) ?? []
    out.push({ day, incidents: list, quiet: list.length === 0 })
  }
  return out
}
