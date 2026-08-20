// Лента событий: фильтры и группировка по дням. Чистые функции отдельно от
// экрана -- их можно проверить тестом, а разметку нет.
export const EVENT_FILTERS = [
  { key: 'all', label: 'Все' },
  { key: 'problems', label: 'Тревоги' },
  { key: 'recoveries', label: 'Восстановления' },
]

// "unknown" считается проблемой сознательно: неизвестное состояние -- это
// информация о том, что что-то не отвечает, а не спокойный фон.
const PROBLEM = new Set(['fail', 'warn', 'unknown'])

export function filterEvents(events = [], filter = 'all') {
  if (filter === 'problems') return events.filter((e) => PROBLEM.has(e.status))
  if (filter === 'recoveries') return events.filter((e) => e.status === 'ok')
  return events
}

// Порядок внутри дня не трогаем: сервер отдаёт ленту свежими вперёд, и
// пересортировка на клиенте была бы вторым мнением о том же.
export function groupByDay(events = []) {
  const byDay = new Map()
  for (const e of events) {
    const day = (e.ts ?? '').slice(0, 10)
    if (!byDay.has(day)) byDay.set(day, [])
    byDay.get(day).push(e)
  }
  return [...byDay.entries()]
    .sort((a, b) => (a[0] < b[0] ? 1 : -1))
    .map(([day, items]) => ({ day, events: items }))
}

// --- Журнал: фраза о последствии, под ней код мелким -----------------------
//
// Событие в ленте -- это не строка "dns: fail", а новость: что человек из-за
// этого почувствовал и сколько это длилось. Длительность не приходит с
// сервера и не может: она живёт В ПАРЕ событий (упало -- поднялось), и
// считать её здесь честно, потому что обе половины пары лежат в этой же
// ленте.
//
// Лента приходит свежими вперёд, поэтому восстановление стоит ВЫШЕ падения:
// пара ищется вверх по массиву, а не вниз.
import { eventPhrase, humanAge } from './labels.js'

const TONE = { ok: 'sig', fail: 'bad' }

export function annotateEvents(events = [], nowMs = Date.now()) {
  const list = Array.isArray(events) ? events : []
  return list.map((e, i) => {
    const ts = Date.parse(e.ts ?? '')
    let durationSec = null
    let ongoing = false
    if (Number.isFinite(ts)) {
      if (e.status === 'fail') {
        const recovery = findAbove(list, i, e.check_name, 'ok')
        if (recovery) {
          durationSec = Math.max(0, Math.round((Date.parse(recovery.ts) - ts) / 1000))
        } else {
          ongoing = true
          durationSec = Math.max(0, Math.round((nowMs - ts) / 1000))
        }
      } else if (e.status === 'ok') {
        const failure = findBelow(list, i, e.check_name, 'fail')
        if (failure) durationSec = Math.max(0, Math.round((ts - Date.parse(failure.ts)) / 1000))
      }
    }
    return {
      ...e,
      tone: TONE[e.status] ?? 'warn',
      title: eventPhrase(e.check_name, e.status),
      code: codeLine(e, durationSec, ongoing),
      durationSec,
      ongoing,
    }
  })
}

// Код под фразой: имя проверки и, если есть, длительность. Смысл в одиночку
// он не несёт -- он для того, кто полезет разбираться.
function codeLine(event, durationSec, ongoing) {
  if (durationSec == null) return event.check_name
  if (ongoing) return `${event.check_name} · идёт ${humanAge(durationSec)}`
  return event.status === 'ok'
    ? `${event.check_name} · после ${humanAge(durationSec)}`
    : `${event.check_name} · лежал ${humanAge(durationSec)}`
}

// Ближайшее событие той же проверки выше по ленте (то есть ПОЗЖЕ по времени).
function findAbove(list, from, checkName, status) {
  for (let i = from - 1; i >= 0; i--) {
    if (list[i].check_name !== checkName) continue
    return list[i].status === status ? list[i] : null
  }
  return null
}

// Ближайшее событие той же проверки ниже по ленте (РАНЬШЕ по времени).
function findBelow(list, from, checkName, status) {
  for (let i = from + 1; i < list.length; i++) {
    if (list[i].check_name !== checkName) continue
    return list[i].status === status ? list[i] : null
  }
  return null
}
