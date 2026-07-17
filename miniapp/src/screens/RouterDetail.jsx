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
import { useCommand } from '../useCommand.js'
import {
  ACTION_LABELS,
  checkLabel,
  checkStateLabel,
  commandOutcomeLabel,
  humanAge,
  incidentCopy,
  pingLabel,
  trafficLabel,
  tunnelStateLabel,
} from '../labels.js'

// TTLs the backend accepts (miniapp_actions.go's miniappSilenceTTLs); the
// button text itself comes from ACTION_LABELS so this card and any other
// screen that offers the same three durations cannot drift on wording.
const SILENCE_OPTIONS = [
  { ttl: '1h', labelKey: 'silence1h' },
  { ttl: '4h', labelKey: 'silence4h' },
  { ttl: '24h', labelKey: 'silence24h' },
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

// One button that owns the whole asynchronous-command lifecycle for a single
// action: an optional confirm step, dispatch, the bounded poll (useCommand),
// and a plain-language rendering of whatever it settles on. Two call sites
// share this -- TrafficSection's "Проверить сейчас" and the restart button on
// a tunnel_* incident -- so neither has to reimplement the confirm gate or
// the pending/ok/err/locked/timeout wording.
//
// Two independent reasons can require confirmation before dispatch, and both
// route through the same `confirming` step rather than two separate dialogs:
//   - `mutatingText` set: this action changes something on the router
//     (tunnel_restart is the only one reachable from this screen -- see
//     miniapp_commands.go's allowlist comment). Always confirmed, online or
//     not, same precedent as the dashboard confirming dns_reset.
//   - `asleep` true: the router is offline/sleeping right now (computed by
//     the caller from router.status -- see RouterDetail's own comment on
//     why that excludes `alert`), so the command will simply sit queued
//     until it wakes. Read-only actions get this same gate when asleep,
//     because "the button appears to do nothing for 90 seconds" is exactly
//     the confusion this task exists to remove -- better to say so up front
//     and let the caller choose to queue it anyway.
function CommandButton({ routerID, action, args = {}, label, busyLabel, mutatingText, asleep, wrapClass, onDone }) {
  const { busy, result, error, run } = useCommand(routerID)
  const [confirming, setConfirming] = useState(false)

  function dispatch() {
    setConfirming(false)
    // A router already known asleep was just told (via the confirm copy
    // below) that its command will simply run late -- so this wait gets far
    // more room than the default 90s before giving up on a reply, matching
    // the promise just made rather than contradicting it a few seconds later.
    run(action, args, { deadlineMs: asleep ? 6 * 60_000 : 90_000 }).then((res) => {
      if (res?.status === 'ok' && onDone) onDone()
    })
  }

  function handleClick() {
    if ((mutatingText || asleep) && !confirming) {
      setConfirming(true)
      return
    }
    dispatch()
  }

  if (confirming) {
    return (
      <div class={wrapClass}>
        {asleep && <p class="state">Роутер сейчас не на связи. Команда выполнится, когда он проснётся.</p>}
        {mutatingText && <p class="state">{mutatingText}</p>}
        <div class="command-actions">
          <button class="btn btn-danger" onClick={dispatch}>
            Да, выполнить
          </button>
          <button class="btn btn-ghost" onClick={() => setConfirming(false)}>
            Отмена
          </button>
        </div>
      </div>
    )
  }

  return (
    <div class={wrapClass}>
      <button class="btn btn-primary" disabled={busy} onClick={handleClick}>
        {busy ? (busyLabel ?? 'Выполняю…') : label}
      </button>
      {busy && <p class="state">Ждём ответа от роутера…</p>}
      {result && (
        <p class={`state${result.status === 'ok' ? '' : ' state-error'}`}>{commandOutcomeLabel(action, result)}</p>
      )}
      {error && <p class="state state-error">{error}</p>}
    </div>
  )
}

function IncidentCard({ routerID, incident, onUpdate, asleep, onDone }) {
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
  const { what, why } = incidentCopy(incident.check_name)

  // tunnel_<id> incidents get a restart button; the four plain checks
  // (external_reach/dns/hydraroute/awg_manager) have no per-router command
  // that fixes them from here, so they get none.
  const tunnelID = incident.check_name.startsWith('tunnel_') ? incident.check_name.slice('tunnel_'.length) : null

  return (
    <li class="card">
      <div class="incident-head">
        <span class="row-title">{what}</span>
        {incident.hard_since && <span class="incident-since">с {formatDateTime(incident.hard_since)}</span>}
      </div>

      {why && <p class="incident-why">{why}</p>}

      {/* Deliberately OUTSIDE the suppressed/!suppressed split below: silence/
          ack/mute only control whether this incident nags again, they never
          touch the router, so a muted tunnel incident must not lose the one
          button that can actually fix it. */}
      {tunnelID && (
        <CommandButton
          routerID={routerID}
          action="tunnel_restart"
          args={{ tunnel_id: tunnelID }}
          label={ACTION_LABELS.restartTunnel}
          busyLabel="Перезапускаю…"
          mutatingText={`Перезапустить ${checkLabel(incident.check_name).toLowerCase()}? Связь через него на несколько секунд прервётся.`}
          asleep={asleep}
          wrapClass="restart-block"
          onDone={onDone}
        />
      )}

      {suppressed ? (
        // Wording mirrors the backend's own confirmation lines for these two
        // actions (alertaction.go's ApplyAck/ApplySilence/ApplyMute status
        // strings, minus emoji and the admin/MSK footer this screen doesn't
        // need) rather than the old bare "квитирован" -- acked and
        // silenced/muted are indistinguishable here (both just set
        // silenced_until; the incident carries no separate "was this a mute"
        // flag), so the silenced branch uses ApplySilence's phrasing, which
        // is honest for either origin.
        <span class="badge badge-offline">
          {incident.acked
            ? 'Вижу проблему — напомним после восстановления'
            : `Уведомления скрыты до ${formatTime(incident.silenced_until)}`}
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
              {ACTION_LABELS[opt.labelKey]}
            </button>
          ))}
          <button
            class="btn btn-ghost"
            disabled={busy}
            onClick={() => runAction(() => ackIncident(routerID, incident.check_name))}
          >
            {ACTION_LABELS.ack}
          </button>
          <button
            class="btn btn-danger"
            disabled={busy}
            onClick={() => runAction(() => muteIncident(routerID, incident.check_name))}
          >
            {ACTION_LABELS.mute}
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

// The operator's daily question, answered in one line. "Проверить сейчас"
// dispatches force_recheck via CommandButton -- read-only, so it needs no
// mutatingText, only the asleep gate every command gets. The two-exit-IP
// comparison (check_via_tunnel/check_direct) that would render alongside the
// verdict is Task 13's; this button only re-runs the recheck that produces
// the verdict above and refreshes the screen (onDone={loadData}) once it's ok.
function TrafficSection({ routerID, traffic, asleep, onDone }) {
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
        <CommandButton
          routerID={routerID}
          action="force_recheck"
          label={ACTION_LABELS.recheck}
          busyLabel="Проверяю…"
          asleep={asleep}
          wrapClass="traffic-probe"
          onDone={onDone}
        />
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

  // Named rather than inlined so a completed command can call it again:
  // an "ok" force_recheck or tunnel_restart changes state that lives in
  // this same fetch (checks/tunnels/traffic, and possibly the incident
  // list), and the screen otherwise never refetches after the initial load.
  // Not guaranteed to show the change immediately -- the agent's own next
  // heartbeat is still the real source of truth -- but it costs one cheap
  // request and shows whatever is currently true rather than leaving stale
  // data on screen indefinitely after a button says "done".
  function loadData() {
    return Promise.all([fetchRouter(id), fetchRouterChecks(id)])
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
  }

  useEffect(() => {
    loadData()
  }, [id])

  function updateIncident(updated) {
    setIncidents((prev) => prev.map((inc) => (inc.check_name === updated.check_name ? updated : inc)))
  }

  if (error) return <p class="state state-error">{error}</p>
  if (router == null) return <p class="state">Загрузка…</p>

  // Say it before dispatching, not after a timeout: router.status already
  // distinguishes reachability from alerting (dashboard_handler.go:780-796),
  // so a spinner that runs 90 seconds to reach the same conclusion the header
  // already states would teach the operator nothing. Keyed on offline/sleeping
  // ONLY -- `alert` means an open incident, and an incident does not imply
  // unreachability: a router can be fully reachable and mid-alert (e.g. an
  // external_reach failure) at the same time, in which case its commands
  // dispatch and answer normally. Warning "may take a while" on a router that
  // is actually sitting there answering would be the same false-confidence
  // failure this whole phase exists to avoid, just pointed the other way.
  const asleep = router.status === 'offline' || router.status === 'sleeping'

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

      <TrafficSection routerID={id} traffic={traffic} asleep={asleep} onDone={loadData} />

      {incidents.length > 0 && (
        <section class="section">
          <h2 class="section-title">Активные тревоги</h2>
          <ul class="list-reset card-stack">
            {incidents.map((inc) => (
              <IncidentCard
                key={inc.check_name}
                routerID={id}
                incident={inc}
                onUpdate={updateIncident}
                asleep={asleep}
                onDone={loadData}
              />
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
