import { useEffect, useState } from 'preact/hooks'
import { fetchTimeline } from '../api.js'
import { groupByDay } from '../events.js'
import { incidentLine, groupIncidentsByDay } from '../incidents.js'

const DAYS = 7

function dayTitle(day) {
  const today = new Date().toISOString().slice(0, 10)
  if (day === today) return 'Сегодня'
  const yesterday = new Date(Date.now() - 86_400_000).toISOString().slice(0, 10)
  if (day === yesterday) return 'Вчера'
  return new Date(`${day}T00:00:00Z`).toLocaleDateString('ru-RU', { day: 'numeric', month: 'long' })
}

function time(ts) {
  return new Date(ts).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' })
}

// Что было за неделю: происшествия, а не события.
//
// Событие -- это «проверка hydraroute вернула fail в 09:05:00»; таких строк за
// неделю десятки тысяч, и человеку они не говорят ничего. Происшествие --
// «обход блокировок не работал с 09:05 до 09:09» -- отвечает на вопрос, с
// которым сюда приходят: что я почувствовал и как долго это длилось.
//
// Свернул их бэкенд: он один видит всю неделю. Клиенту приезжают только
// пятьсот новейших строк -- полтора часа, -- и раньше экран показывал этот
// час под заголовком «7 дней».
export function EventsTab({ routerID, routerName }) {
  const [data, setData] = useState(null)
  const [error, setError] = useState(null)
  const [raw, setRaw] = useState(false)

  useEffect(() => {
    setData(null)
    setError(null)
    fetchTimeline(routerID, DAYS, { raw })
      .then(setData)
      // Человеку нужен ответ, а не путь и код: техническая строка вида
      // "/routers/1/timeline failed: 404" ничего ему не говорит.
      .catch(() => setError('Не удалось загрузить события. Потяните экран вниз и попробуйте ещё раз.'))
  }, [routerID, raw])

  if (error) return <p class="state state-error">{error}</p>
  if (data == null) return <p class="state">Загрузка…</p>

  return (
    <div class="screen">
      <h1 class="screen-title">Что было</h1>
      <p class="router-lastseen">
        {routerName ? `Роутер «${routerName}» · ${DAYS} дней` : `За ${DAYS} дней`}
      </p>

      <div class="filter-row">
        <button
          type="button"
          class={`filter-chip${raw ? '' : ' filter-chip-active'}`}
          onClick={() => setRaw(false)}
        >
          Как было
        </button>
        <button
          type="button"
          class={`filter-chip${raw ? ' filter-chip-active' : ''}`}
          onClick={() => setRaw(true)}
        >
          Как есть
        </button>
      </div>

      {raw ? <RawFeed data={data} /> : <IncidentFeed data={data} />}
    </div>
  )
}

function IncidentFeed({ data }) {
  const incidents = data.incidents ?? []
  const groups = groupIncidentsByDay(incidents, DAYS)

  return (
    <>
      {data.truncated && (
        <p class="state">
          Событий за неделю оказалось больше, чем мы читаем за раз, — часть самого старого
          в этот список не попала.
        </p>
      )}

      {incidents.length === 0 && (
        <p class="hint">За неделю ничего не ломалось — это хорошая новость.</p>
      )}

      {groups.map((g) => (
        <section key={g.day} class="section">
          <h2 class="section-title">{dayTitle(g.day)}</h2>
          {g.quiet ? (
            <p class="day-quiet">Всё работало</p>
          ) : (
            <ul class="card list-reset">
              {g.incidents.map((incident, i) => {
                const line = incidentLine(incident)
                return (
                  <li key={`${incident.check_name}-${incident.from}-${i}`} class="inc">
                    <span class={`inc-dot inc-dot-${line.tone}`} />
                    <span class="inc-main">
                      <b class="inc-title">{line.title}</b>
                      <span class="inc-detail">{line.detail}</span>
                    </span>
                  </li>
                )
              })}
            </ul>
          )}
        </section>
      ))}
    </>
  )
}

// «Как есть» -- сырая лента для того, кто полез разбираться: машинные имена
// проверок и их состояния. Окно она обязана называть словами: пятьсот строк
// это примерно час, а не неделя, и молчать об этом под заголовком «7 дней»
// значило бы врать ровно так, как врал прежний экран.
function RawFeed({ data }) {
  const events = data.events ?? []
  const groups = groupByDay(events)

  if (events.length === 0) return <p class="state">За неделю событий не записано.</p>

  return (
    <>
      <p class="hint">
        Последние {events.length} записей — это примерно час, а не неделя.
        {data.truncated ? ' Более старые сюда не поместились.' : ''}
      </p>
      {groups.map((g) => (
        <section key={g.day} class="section">
          <h2 class="section-title">{dayTitle(g.day)}</h2>
          <ul class="card list-reset">
            {g.events.map((e, i) => (
              <li key={`${e.check_name}-${e.ts}-${i}`} class="ev">
                <time class="ev-time">{time(e.ts)}</time>
                <span class="ev-main">
                  {e.check_name}
                  <u class="ev-code">{e.status}</u>
                </span>
                <span class={`ev-dot ev-dot-${e.status === 'ok' ? 'sig' : 'bad'}`} />
              </li>
            ))}
          </ul>
        </section>
      ))}
    </>
  )
}
