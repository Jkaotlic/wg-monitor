import { useEffect, useState } from 'preact/hooks'
import { fetchTimeline } from '../api.js'
import { EVENT_FILTERS, filterEvents, groupByDay, annotateEvents } from '../events.js'

const DAYS = 7

function dayTitle(day) {
  const d = new Date(`${day}T00:00:00Z`)
  const today = new Date().toISOString().slice(0, 10)
  if (day === today) return 'Сегодня'
  return d.toLocaleDateString('ru-RU', { day: 'numeric', month: 'long' })
}

function time(ts) {
  return new Date(ts).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' })
}

// Лента за неделю: что происходило, а не что происходит сейчас. Отвечает на
// вопрос "это уже третий раз за сутки или впервые" -- по нему видно, чинить
// туннель или менять конфиг.
export function EventsTab({ routerID, routerName }) {
  const [data, setData] = useState(null)
  const [error, setError] = useState(null)
  const [filter, setFilter] = useState('all')

  useEffect(() => {
    setData(null)
    setError(null)
    fetchTimeline(routerID, DAYS)
      .then(setData)
      // Человеку нужен ответ, а не путь и код: техническая строка вида
      // "/routers/1/timeline failed: 404" ничего ему не говорит.
      .catch(() => setError('Не удалось загрузить события. Потяните экран вниз и попробуйте ещё раз.'))
  }, [routerID])

  if (error) return <p class="state state-error">{error}</p>
  if (data == null) return <p class="state">Загрузка…</p>

  // Длительность считается ДО фильтра: пара «упало -- поднялось» может
  // разъехаться по фильтрам, и посчитанная по отфильтрованной ленте она
  // назвала бы соседнее падение той же проверки.
  const shown = filterEvents(annotateEvents(data.events ?? []), filter)
  const groups = groupByDay(shown)

  return (
    <div class="screen">
      <h1 class="screen-title">События</h1>
      <p class="router-lastseen">
        {routerName ? `Роутер «${routerName}» · ${DAYS} дней` : `За ${DAYS} дней`}
      </p>

      <div class="filter-row">
        {EVENT_FILTERS.map((f) => (
          <button
            key={f.key}
            type="button"
            class={`filter-chip${f.key === filter ? ' filter-chip-active' : ''}`}
            onClick={() => setFilter(f.key)}
          >
            {f.label}
          </button>
        ))}
      </div>

      {data.truncated && (
        <p class="state">Показаны последние {data.events.length} событий — их было больше.</p>
      )}

      {groups.length > 0 && (
        <p class="hint">
          <b>Красная точка</b> — человек это почувствовал. Жёлтая — знаем, что просрочено, но
          заметить было нечего. Лаймовая — стало снова работать.
        </p>
      )}

      {groups.length === 0 ? (
        <p class="state">
          {filter === 'all'
            ? 'За неделю ничего не происходило — это хорошая новость.'
            : 'По этому фильтру за неделю ничего нет.'}
        </p>
      ) : (
        groups.map((g) => (
          <section key={g.day} class="section">
            <h2 class="section-title">{dayTitle(g.day)}</h2>
            <ul class="card list-reset">
              {g.events.map((e, i) => (
                <li key={`${e.check_name}-${e.ts}-${i}`} class="ev">
                  <time class="ev-time">{time(e.ts)}</time>
                  <span class="ev-main">
                    {e.title}
                    <u class="ev-code">{e.code}</u>
                  </span>
                  <span class={`ev-dot ev-dot-${e.tone}`} />
                </li>
              ))}
            </ul>
          </section>
        ))
      )}
    </div>
  )
}
