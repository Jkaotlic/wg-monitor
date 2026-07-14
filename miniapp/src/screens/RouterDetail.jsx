import { useEffect, useState } from 'preact/hooks'
import {
  fetchRouter,
  fetchRouterChecks,
  fetchIncidentHistory,
  silenceIncident,
  ackIncident,
  muteIncident,
} from '../api.js'
import { setBackButtonVisible, onBackButtonClick } from '../telegram.js'

const SILENCE_OPTIONS = [
  { ttl: '1h', label: '1ч' },
  { ttl: '4h', label: '4ч' },
  { ttl: '24h', label: '24ч' },
]

function formatTime(iso) {
  if (!iso) return ''
  return new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function formatDateTime(iso) {
  if (!iso) return ''
  return new Date(iso).toLocaleString([], { dateStyle: 'short', timeStyle: 'short' })
}

function isSuppressed(incident) {
  return incident.acked || (incident.silenced_until != null && new Date(incident.silenced_until) > new Date())
}

function IncidentCard({ routerID, incident, onUpdate }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(null)
  const [expanded, setExpanded] = useState(false)
  const [history, setHistory] = useState(null)
  const [historyTruncated, setHistoryTruncated] = useState(false)
  const [historyError, setHistoryError] = useState(null)

  function runAction(call) {
    setBusy(true)
    setError(null)
    call()
      .then((data) => onUpdate(data.incident))
      .catch((err) => setError(err.message))
      .finally(() => setBusy(false))
  }

  function toggleHistory() {
    const next = !expanded
    setExpanded(next)
    if (next && history == null) {
      setHistoryError(null)
      fetchIncidentHistory(routerID, incident.check_name)
        .then((data) => {
          setHistory(data.transitions ?? [])
          setHistoryTruncated(!!data.truncated)
        })
        .catch((err) => setHistoryError(err.message))
    }
  }

  const suppressed = isSuppressed(incident)

  return (
    <li class="incident-card">
      <div class="incident-header">
        <span class="incident-name">{incident.check_name}</span>
        {incident.hard_since && <span class="incident-since">с {formatDateTime(incident.hard_since)}</span>}
      </div>

      {suppressed ? (
        <p class="incident-suppressed">
          {incident.acked ? 'квитирован' : `заглушён до ${formatTime(incident.silenced_until)}`}
        </p>
      ) : (
        <div class="incident-actions">
          {SILENCE_OPTIONS.map((opt) => (
            <button key={opt.ttl} disabled={busy} onClick={() => runAction(() => silenceIncident(routerID, incident.check_name, opt.ttl))}>
              {opt.label}
            </button>
          ))}
          <button disabled={busy} onClick={() => runAction(() => ackIncident(routerID, incident.check_name))}>
            Квитировать
          </button>
          <button disabled={busy} onClick={() => runAction(() => muteIncident(routerID, incident.check_name))}>
            Заглушить
          </button>
        </div>
      )}

      {error && <p class="incident-error">{error}</p>}

      <button class="incident-history-toggle" onClick={toggleHistory}>
        {expanded ? 'Скрыть историю' : 'История за 24ч'}
      </button>

      {expanded && (
        <div class="incident-history">
          {historyError && <p class="incident-error">{historyError}</p>}
          {historyError == null && history == null && <p class="state-message">Загрузка…</p>}
          {history != null && history.length === 0 && <p class="state-message">Нет событий за 24ч</p>}
          {history != null && history.length > 0 && (
            <>
              {historyTruncated && <p class="state-message">показаны только последние события</p>}
              <ul>
                {history.map((t, i) => (
                  <li key={`${t.ts}-${i}`} class={`history-entry history-entry-${t.status}`}>
                    {formatTime(t.ts)} · {t.label}
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
      )}
    </li>
  )
}

export function RouterDetail({ id, onBack }) {
  const [router, setRouter] = useState(null)
  const [incidents, setIncidents] = useState([])
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
        setIncidents(r.incidents ?? [])
        setChecks(c.checks ?? [])
      })
      .catch((err) => setError(err.message))
  }, [id])

  function updateIncident(updated) {
    setIncidents((prev) => prev.map((inc) => (inc.check_name === updated.check_name ? updated : inc)))
  }

  if (error) return <p class="state-message state-message-error">{error}</p>
  if (router == null) return <p class="state-message">Загрузка…</p>

  return (
    <div class="router-detail">
      <h1>{router.nickname}</h1>
      <p class={`router-status router-status-${router.status}`}>{router.status}</p>
      {incidents.length > 0 && (
        <section>
          <h2>Активные тревоги</h2>
          <ul class="incident-list">
            {incidents.map((inc) => (
              <IncidentCard key={inc.check_name} routerID={id} incident={inc} onUpdate={updateIncident} />
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
