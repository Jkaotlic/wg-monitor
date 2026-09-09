// Происшествие на экране: фраза о последствии и строка «когда и сколько».
//
// Сворачивание живёт на бэкенде -- клиент видит только пятьсот новейших строк
// и посчитать по ним неделю не может. Здесь остаётся то, что и должно быть на
// клиенте: как это назвать по-русски и как разложить по дням.
import { incidentWhatPlain, humanAge } from './labels.js'

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
    const day = (inc.from ?? '').slice(0, 10)
    if (!byDay.has(day)) byDay.set(day, [])
    byDay.get(day).push(inc)
  }

  const out = []
  for (let i = 0; i < days; i++) {
    const d = new Date(nowMs - i * 86_400_000)
    const day = d.toISOString().slice(0, 10)
    const list = byDay.get(day) ?? []
    out.push({ day, incidents: list, quiet: list.length === 0 })
  }
  return out
}
