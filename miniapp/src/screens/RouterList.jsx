import { useEffect, useState } from 'preact/hooks'
import { fetchRouters } from '../api.js'

const STATUS_LABEL = {
  online: 'В сети',
  sleeping: 'Спит',
  offline: 'Офлайн',
  alert: 'Тревога',
}

export function RouterList({ onSelect }) {
  const [routers, setRouters] = useState(null)
  const [error, setError] = useState(null)

  useEffect(() => {
    fetchRouters()
      .then((data) => setRouters(data.routers ?? []))
      .catch((err) => setError(err.message))
  }, [])

  if (error) return <p class="state state-error">{error}</p>
  if (routers == null) return <p class="state">Загрузка…</p>
  if (routers.length === 0) return <p class="state">Нет доступных роутеров</p>

  return (
    <div class="screen">
      <h1 class="screen-title">Роутеры</h1>
      <ul class="card list-reset">
        {routers.map((r) => (
          <li key={r.id} class="row row-clickable" onClick={() => onSelect(r.id)}>
            <span class="row-title">{r.nickname}</span>
            <span class={`badge badge-${r.status}`}>{STATUS_LABEL[r.status] ?? r.status}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}
