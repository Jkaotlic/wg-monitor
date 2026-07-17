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
import { AccessSection } from './AccessSection.jsx'
import { RouterDevice, orderChecks } from '../components/RouterDevice.jsx'
import {
  ACTION_LABELS,
  checkLabel,
  checkStateLabel,
  humanAge,
  pingLabel,
  trafficLabel,
  tunnelStateLabel,
} from '../labels.js'

const SILENCE_OPTIONS = [
  { ttl: '1h', label: '1ч' },
  { ttl: '4h', label: '4ч' },
  { ttl: '24h', label: '24ч' },
]

// The header's four words. This is the ONLY router-state vocabulary on the screen:
// `router.status` (online/sleeping/offline/alert, dashboard_handler.go:780-796) is
// the state model the backend actually produces, and a second one would drift from
// it. (The spec's §3.1 ClassifyState port -- ok/degraded/hard/offline -- was cut
// from this phase; nothing produces those values, so labels.js no longer carries
// them either.)
const STATUS_LABEL = {
  online: 'В сети',
  sleeping: 'Спит',
  offline: 'Офлайн',
  alert: 'Тревога',
}

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
    <li class="card">
      <div class="incident-head">
        <span class="row-title">{incident.check_name}</span>
        {incident.hard_since && <span class="incident-since">с {formatDateTime(incident.hard_since)}</span>}
      </div>

      {suppressed ? (
        <span class="badge badge-offline">
          {incident.acked ? 'квитирован' : `заглушён до ${formatTime(incident.silenced_until)}`}
        </span>
      ) : (
        <div class="incident-actions">
          {SILENCE_OPTIONS.map((opt) => (
            <button
              key={opt.ttl}
              class="btn btn-ghost"
              disabled={busy}
              onClick={() => runAction(() => silenceIncident(routerID, incident.check_name, opt.ttl))}
            >
              {opt.label}
            </button>
          ))}
          <button
            class="btn btn-ghost"
            disabled={busy}
            onClick={() => runAction(() => ackIncident(routerID, incident.check_name))}
          >
            Квитировать
          </button>
          <button
            class="btn btn-danger"
            disabled={busy}
            onClick={() => runAction(() => muteIncident(routerID, incident.check_name))}
          >
            Заглушить
          </button>
        </div>
      )}

      {error && <p class="state state-error">{error}</p>}

      <button class="btn btn-ghost incident-history-toggle" onClick={toggleHistory}>
        {expanded ? 'Скрыть историю' : 'История за 24ч'}
      </button>

      {expanded && (
        <div class="incident-history">
          {historyError && <p class="state state-error">{historyError}</p>}
          {historyError == null && history == null && <p class="state">Загрузка…</p>}
          {history != null && history.length === 0 && <p class="state">Нет событий за 24ч</p>}
          {history != null && history.length > 0 && (
            <>
              {historyTruncated && <p class="state">показаны только последние события</p>}
              <ul class="list-reset history-list">
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

// One tunnel, in the shape §3.3 sketches it: name and the egress badge share the
// first line, the human reading spans the second.
//
//   Amsterdam                             весь трафик идёт сюда
//   работает · обмен ключами 45 сек назад · проверка связи живая
//
// Health is NOT re-derived here: `tunnelStateLabel` is imported so the antenna
// above and this row cannot disagree about the same tunnel. That matters most for
// `enabled`, which is a nullable tri-state (miniapp_tunnels.go:38) -- true, false,
// or absent because the agent never told us -- and whose absence a second, naive
// derivation would render as "выключен".
function TunnelRow({ tunnel, isEgress }) {
  const parts = [tunnelStateLabel(tunnel)]
  // `!= null` deliberately: a fresh handshake is `handshake_age_sec: 0`, which is
  // falsy but is the single most positive reading a tunnel has.
  if (tunnel.handshake_age_sec != null) {
    parts.push(`обмен ключами ${humanAge(tunnel.handshake_age_sec)} назад`)
  }
  const ping = pingLabel(tunnel.ping_check_status)
  if (ping) parts.push(`проверка связи ${ping}`)

  // The human name leads and the interface id follows as secondary (§3.3): the
  // owner knows "Amsterdam", not "awg12". When the agent gave us no name, the id
  // IS the name and printing it twice would be noise.
  const named = tunnel.name && tunnel.name !== tunnel.tunnel_id

  return (
    <li class="row tunnel-row">
      <span class="tunnel-name">
        <span class="row-title">{named ? tunnel.name : tunnel.tunnel_id}</span>
        {named && <span class="tunnel-id">{tunnel.tunnel_id}</span>}
      </span>
      {isEgress && <span class="badge badge-online">весь трафик идёт сюда</span>}
      <span class="tunnel-sub">{parts.join(' · ')}</span>
    </li>
  )
}

function TunnelsSection({ tunnels, traffic }) {
  // Only a `vpn` verdict names an egress, and only a name we actually have may be
  // badged -- same guard the drawing uses (RouterDevice.jsx:424-430). A verdict
  // pointing at a tunnel we weren't given badges nothing rather than the first row.
  const egressID = traffic?.mode === 'vpn' ? traffic.egress_tunnel_id : null

  return (
    <section class="section">
      <h2 class="section-title">Туннели</h2>
      {tunnels.length === 0 ? (
        <div class="card">
          <p class="traffic-detail">Туннели не заведены — роутер не сообщил ни одного.</p>
        </div>
      ) : (
        <ul class="card list-reset">
          {tunnels.map((t) => (
            <TunnelRow key={t.tunnel_id} tunnel={t} isEgress={!!egressID && t.tunnel_id === egressID} />
          ))}
        </ul>
      )}
    </section>
  )
}

// The operator's daily question, answered in one line. `onProbe`/`probing` are the
// presentational contract only: Task 12 owns the command dispatch and its waiting
// UX, so the button is threaded but inert until that lands.
function TrafficSection({ traffic, onProbe, probing }) {
  const { title, detail } = trafficLabel(traffic)
  return (
    <section class="section">
      <h2 class="section-title">Куда идёт трафик</h2>
      <div class="card">
        <p class="traffic-title">{title}</p>
        <p class="traffic-detail">{detail}</p>
        {traffic?.contested_default && (
          <p class="traffic-note">Основным настроен ещё один туннель, но трафик несёт не он.</p>
        )}
        <button class="btn btn-primary traffic-probe" disabled={probing} onClick={onProbe}>
          {probing ? 'Проверяю…' : ACTION_LABELS.recheck}
        </button>
      </div>
    </section>
  )
}

export function RouterDetail({ id, onBack, isAdmin }) {
  const [router, setRouter] = useState(null)
  const [incidents, setIncidents] = useState([])
  const [checks, setChecks] = useState(null)
  const [tunnels, setTunnels] = useState([])
  const [traffic, setTraffic] = useState(null)
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
        setTunnels(c.tunnels ?? [])
        // A backend older than this phase sends no `traffic` at all; trafficLabel
        // and RouterDevice both read a missing one as "unknown", which is the
        // honest answer rather than a defaulted-away one.
        setTraffic(c.traffic ?? null)
      })
      .catch((err) => setError(err.message))
  }, [id])

  function updateIncident(updated) {
    setIncidents((prev) => prev.map((inc) => (inc.check_name === updated.check_name ? updated : inc)))
  }

  if (error) return <p class="state state-error">{error}</p>
  if (router == null) return <p class="state">Загрузка…</p>

  // The lamps' own set, in the lamps' own order (RouterDevice owns both). The
  // `tunnel_*` rows that `checks[]` also carries are filtered there -- they are the
  // antennas and the Туннели block above, and listing them here as well would show
  // every tunnel three times.
  const otherChecks = orderChecks(checks ?? [])

  return (
    <div class="screen">
      <div class="router-header">
        <h1 class="screen-title">{router.nickname}</h1>
        <span class={`badge badge-${router.status}`}>{STATUS_LABEL[router.status] ?? router.status}</span>
      </div>
      {/* The vitality the panel's PWR lamp would have carried, stated in words
          instead. See this task's report: `router.status` alone cannot honestly
          light that lamp (an incident makes it "alert" no matter how long the
          router has been dark), and the age threshold that could is backend
          policy the mini app is never told. A printed age needs no threshold. */}
      <p class="router-lastseen">
        {router.last_seen_age_sec != null
          ? `Последний ответ ${humanAge(router.last_seen_age_sec)} назад`
          : 'Роутер ещё ни разу не выходил на связь'}
      </p>

      <RouterDevice tunnels={tunnels} traffic={traffic} checks={checks ?? []} name={router.nickname} />

      <TunnelsSection tunnels={tunnels} traffic={traffic} />

      {/* onProbe is deliberately not passed yet -- Task 12 owns the dispatch. */}
      <TrafficSection traffic={traffic} />

      {incidents.length > 0 && (
        <section class="section">
          <h2 class="section-title">Активные тревоги</h2>
          <ul class="list-reset card-stack">
            {incidents.map((inc) => (
              <IncidentCard key={inc.check_name} routerID={id} incident={inc} onUpdate={updateIncident} />
            ))}
          </ul>
        </section>
      )}

      {otherChecks.length > 0 && (
        <section class="section">
          <h2 class="section-title">Прочие проверки</h2>
          <ul class="card list-reset">
            {otherChecks.map((c) => (
              <li key={c.check_name} class="row checks-row">
                <span class="row-title">{checkLabel(c.check_name)}</span>
                <span class={`checks-status checks-status-${c.status}`}>
                  {checkStateLabel(c.status)} · {formatDateTime(c.ts)}
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {isAdmin && <AccessSection routerID={id} />}
    </div>
  )
}
