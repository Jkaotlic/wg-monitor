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
