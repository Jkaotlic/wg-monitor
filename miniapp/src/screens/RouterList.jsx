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

  if (error) return <p class="state-message state-message-error">{error}</p>
  if (routers == null) return <p class="state-message">Загрузка…</p>
  if (routers.length === 0) return <p class="state-message">Нет доступных роутеров</p>

  return (
    <ul class="router-list">
      {routers.map((r) => (
        <li key={r.id} class={`router-row router-row-${r.status}`} onClick={() => onSelect(r.id)}>
          <span class="router-row-nickname">{r.nickname}</span>
          <span class="router-row-status">{STATUS_LABEL[r.status] ?? r.status}</span>
        </li>
      ))}
    </ul>
  )
}
