import { useEffect, useState } from 'preact/hooks'
import { fetchRouter, fetchRouterChecks } from '../api.js'
import { setBackButtonVisible, onBackButtonClick } from '../telegram.js'

export function RouterDetail({ id, onBack }) {
  const [router, setRouter] = useState(null)
  const [checks, setChecks] = useState(null)
  const [error, setError] = useState(null)

  useEffect(() => {
    setBackButtonVisible(true)
    const off = onBackButtonClick(onBack)
    return () => {
      setBackButtonVisible(false)
      off()
    }
  }, [onBack])

  useEffect(() => {
    Promise.all([fetchRouter(id), fetchRouterChecks(id)])
      .then(([r, c]) => {
        setRouter(r.router)
        setChecks(c.checks ?? [])
      })
      .catch((err) => setError(err.message))
  }, [id])

  if (error) return <p class="state-message state-message-error">{error}</p>
  if (router == null) return <p class="state-message">Загрузка…</p>

  return (
    <div class="router-detail">
      <h1>{router.nickname}</h1>
      <p class={`router-status router-status-${router.status}`}>{router.status}</p>
      {router.active_incidents?.length > 0 && (
        <section>
          <h2>Активные тревоги</h2>
          <ul>
            {router.active_incidents.map((inc) => (
              <li key={inc.check_name}>{inc.check_name} — с {inc.hard_since}</li>
            ))}
          </ul>
        </section>
      )}
      <section>
        <h2>Проверки</h2>
        <ul>
          {(checks ?? []).map((c) => (
            <li key={c.check_name}>{c.check_name}: {c.status} ({c.ts})</li>
          ))}
        </ul>
      </section>
    </div>
  )
}
