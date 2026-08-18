import { useEffect, useRef, useState } from 'preact/hooks'
import {
  fetchRouter,
  fetchRouterChecks,
  fetchIncidentHistory,
  silenceIncident,
  ackIncident,
  muteIncident,
} from '../api.js'
import { RouterDevice, orderChecks, lampKey } from '../components/RouterDevice.jsx'
import { Section } from '../ui/Section.jsx'
import { ActionTile } from '../ui/ActionTile.jsx'
import { ListRow } from '../ui/ListRow.jsx'
import { tunnelHealth } from './tunnelHealth.js'
import { useCommand } from '../useCommand.js'
import { confirmSheet } from '../sheet.js'
import {
  ACTION_LABELS,
  checkLabel,
  checkStateLabel,
  commandOutcomeLabel,
  humanAge,
  incidentCopy,
  legendLabel,
  pingLabel,
  statusLabel,
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
function CommandButton({ routerID, action, args = {}, label, busyLabel, mutatingText, asleep, wrapClass, onDone, openSheet, sheetTitle }) {
  const { busy, result, error, run } = useCommand(routerID)

  // Подтверждение и ход выполнения переехали в нижний шит: раньше каждая
  // кнопка изобретала своё подтверждение прямо внутри карточки, и они
  // расходились друг с другом. Читающая команда на живом роутере по-прежнему
  // запускается сразу -- подтверждать нечего.
  function handleClick() {
    if ((mutatingText || asleep) && openSheet) {
      openSheet(
        confirmSheet({
          routerID,
          title: sheetTitle ?? label,
          body: mutatingText ?? 'Роутер сейчас не на связи. Команда встанет в очередь и выполнится, когда он проснётся.',
          action,
          args,
          buttonLabel: mutatingText ? 'Да, выполнить' : 'Поставить в очередь',
          danger: Boolean(mutatingText),
          asleep,
          onDone,
        }),
      )
      return
    }
    run(action, args, { deadlineMs: asleep ? 6 * 60_000 : 90_000 }).then((res) => {
      if (res?.status === 'ok' && onDone) onDone()
    })
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

function IncidentCard({ routerID, incident, onUpdate, asleep, onDone, openSheet }) {
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
          openSheet={openSheet}
          sheetTitle={ACTION_LABELS.restartTunnel}
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
          <span class="incident-actions-label">{ACTION_LABELS.silenceGroup}</span>
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
  const health = tunnelHealth(tunnels)
  // Only a `vpn` verdict names an egress, and only a name we actually have may be
  // badged -- same guard the drawing uses (RouterDevice.jsx:424-430). A verdict
  // pointing at a tunnel we weren't given badges nothing rather than the first row.
  const egressID = traffic?.mode === 'vpn' ? traffic.egress_tunnel_id : null

  return (
    <section class="section">
      <h2 class="section-title">Целостность туннелей · {health.label}</h2>
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

// The operator's daily question, answered in one line. This button dispatches
// force_recheck via CommandButton -- read-only, so it needs no mutatingText,
// only the asleep gate every command gets -- and it only re-runs the recheck
// that produces the verdict above, refreshing the screen (onDone={loadData})
// once it settles ok. The two-exit-IP comparison that actually PROVES or
// disproves that verdict (check_via_tunnel/check_direct) is a separate block,
// ExitCompareSection below (Task 13) -- two independent read-only probes, not
// a rerun of this one.
function TrafficSection({ routerID, traffic, asleep, onDone, openSheet }) {
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
          openSheet={openSheet}
        />
      </div>
    </section>
  )
}


// Быстрые действия по макету: плитки вместо разрозненных кнопок внутри
// карточек. Набор -- ровно то, что мини-аппу разрешено (allowlist в
// miniapp_commands.go). "Сбросить DNS", "Обслужить пакеты" и "Перезагрузить
// роутер" из макета здесь сознательно отсутствуют: этих команд у мини-аппа
// нет, они админские и живут в дашборде.
function QuickActions({ routerID, tunnels, traffic, asleep, onDone, openSheet, onTab }) {
  // Перезапускать предлагаем тот туннель, которым сейчас идёт трафик: если
  // трафик идёт мимо туннелей, предлагать нечего -- плитка не рисуется.
  const egressID = traffic?.mode === 'vpn' ? traffic.egress_tunnel_id : null
  const egress = tunnels.find((t) => t.tunnel_id === egressID)

  return (
    <Section title="Быстрые действия">
      <div class="action-grid">
        {egress && (
          <ActionTile
            title={ACTION_LABELS.restartTunnel}
            hint={`${egress.tunnel_id} · связь прервётся на несколько секунд`}
            danger
            onClick={() =>
              openSheet(
                confirmSheet({
                  routerID,
                  title: ACTION_LABELS.restartTunnel,
                  body: `Перезапустить туннель ${egress.name || egress.tunnel_id}? Связь через него на несколько секунд прервётся.`,
                  action: 'tunnel_restart',
                  args: { tunnel_id: egress.tunnel_id },
                  buttonLabel: 'Да, выполнить',
                  danger: true,
                  asleep,
                  onDone,
                }),
              )
            }
          />
        )}
        <ActionTile
          title="Собрать диагностику"
          hint="полный отчёт от агента"
          onClick={() => onTab('diag')}
        />
      </div>
    </Section>
  )
}

// The agent's two connectivity probes return prose written for a chat message
// (actions/connectivity.go:47-104: "🌍 Через туннель (%s):" / "🇷🇺 Напрямую
// (через системный маршрут):", then an "Exit IP: %s" line, then a blank line,
// then one ✅/❌ line per site), not structured data. The exit IP is the only
// piece worth lifting out for the side-by-side compare below; everything else
// is shown verbatim in ExitProbeBlock rather than re-parsed, which would just
// fight a format this file doesn't own.
//
// The character class covers both IPv4 and IPv6 literals. It deliberately
// will NOT match connectivity.go's own "❓ не удалось определить (<reason>)"
// placeholder (printed when its internal cdn-cgi/trace lookup itself failed)
// -- that placeholder starts with an emoji, not a hex digit, so a failed
// trace correctly falls through to "no IP" instead of capturing the
// placeholder text as if it were an address.
function extractExitIP(output) {
  return output?.match(/Exit IP:\s*([0-9a-fA-F.:]+)/)?.[1] ?? null
}

// Only what the compare above needs: the parsed IP, or null. Pulled out so
// ExitCompareSection (comparing the two) and ExitProbeBlock (displaying one)
// can't disagree on what counts as "found an IP" -- both call this, neither
// re-derives it.
function probeIP(state) {
  return state.result ? extractExitIP(state.result.output) : null
}

// The honest reason a settled probe has no IP to show. Deliberately NOT keyed
// off `result.status` the way commandOutcomeLabel is elsewhere on this
// screen: classifyConnectivityStatus (connectivity.go:450-460) returns "err"
// merely because one of three site checks failed, while the exit-IP trace
// underneath it -- and the "Exit IP:" line in `output` -- can still have
// succeeded. So every settled result, "ok" or "err" alike, is searched for an
// IP first; only "locked"/"timeout" (the action never actually ran) skip
// straight to commandOutcomeLabel's existing fixed phrasing. A genuine parse
// miss gets one short, honest line of its own rather than echoing the full
// report a second time -- the raw report is already rendered verbatim right
// below by ExitProbeBlock.
function probeNote(action, state) {
  if (state.error) return state.error
  if (!state.result) return null
  if (extractExitIP(state.result.output)) return null
  if (state.result.status === 'locked' || state.result.status === 'timeout') {
    return commandOutcomeLabel(action, state.result)
  }
  return 'Не удалось определить адрес'
}

// One side of the comparison: a label, the parsed IP (or the honest reason
// there isn't one), and the agent's own report verbatim underneath --
// `white-space: pre-line` (see .compare-probe-detail) is what lets that text
// keep connectivity.go's own line breaks without this file re-splitting them.
function ExitProbeBlock({ label, action, state }) {
  const ip = probeIP(state)
  const note = state.busy ? null : probeNote(action, state)
  return (
    <div class="compare-probe">
      <p class="compare-probe-label">{label}</p>
      {state.busy && <p class="compare-probe-ip compare-probe-pending">Проверяю…</p>}
      {!state.busy && ip && <p class="compare-probe-ip">{ip}</p>}
      {!state.busy && !ip && note && <p class="compare-probe-ip compare-probe-unknown">{note}</p>}
      {state.result?.output && <p class="compare-probe-detail">{state.result.output}</p>}
    </div>
  )
}

// Task 13: the one comparison the bot has never made. check_via_tunnel and
// check_direct have each been their own button in the bot for as long as
// those checks have existed (actions/connectivity.go) -- nothing has ever put
// their two answers next to each other, and the difference (or lack of one)
// between them IS the answer to "does traffic actually go through the VPN".
// Two different exit IPs prove the tunnel carries traffic; the same IP twice
// proves it doesn't, which no per-site checkmark elsewhere on this screen
// could ever say on its own.
//
// Both actions are read-only and take no args (miniapp_commands.go's
// allowlist comment; connectivity.go does nothing but issue outbound HTTP
// HEAD/GETs). So this reuses CommandButton's asleep confirm gate -- queuing a
// command on a sleeping router is exactly as confusing here as anywhere else
// on this screen -- but not its mutatingText path, since neither probe
// changes anything on the router. It needs its own component rather than two
// CommandButtons because the two dispatches must fire from one click and the
// result has to render as a single comparison, not two independent cards.
function ExitCompareSection({ routerID, traffic, asleep }) {
  const viaTunnel = useCommand(routerID)
  const direct = useCommand(routerID)
  const [confirming, setConfirming] = useState(false)

  const busy = viaTunnel.busy || direct.busy
  const attempted = busy || !!(viaTunnel.result || viaTunnel.error || direct.result || direct.error)

  function dispatch() {
    setConfirming(false)
    const opts = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }
    // Independent probes: both fire from this one click, and neither awaits
    // or is cancelled by the other -- a slow or failed side must never block
    // the other side's answer from showing up.
    viaTunnel.run('check_via_tunnel', {}, opts)
    direct.run('check_direct', {}, opts)
  }

  function handleClick() {
    if (asleep && !confirming) {
      setConfirming(true)
      return
    }
    dispatch()
  }

  const viaIP = probeIP(viaTunnel)
  const directIP = probeIP(direct)
  const bothIPs = !!(viaIP && directIP)
  const sameIP = bothIPs && viaIP === directIP
  // traffic.mode is Task 3's own derivation (trafficLabel above reads it the
  // same way). On a sing-box router, the route is chosen per destination, so
  // these two probes -- hitting different sites for the via-tunnel and direct
  // checks -- were never guaranteed to take the same path in the first place.
  // Equal or different, neither answer generalizes to "all traffic", so this
  // mode gets a caveat instead of either verdict below, and gets it up front
  // (before the button, not just after a run) so the reader isn't primed to
  // expect a confident answer.
  const singboxMode = traffic?.mode === 'singbox'

  return (
    <section class="section">
      <h2 class="section-title">Проверить сейчас</h2>
      <div class="card">
        <p class="traffic-detail">
          Запускает оба зонда сразу и показывает, под каким адресом роутер выходит в интернет через туннель и напрямую.
        </p>

        {singboxMode && (
          <p class="compare-note compare-note-caution">
            На этом роутере маршрут выбирается для каждого сайта отдельно (sing-box) — два адреса ниже не складываются в
            общий ответ «весь трафик идёт туда-то».
          </p>
        )}

        {confirming ? (
          <div class="compare-confirm">
            <p class="state">Роутер сейчас не на связи. Команда выполнится, когда он проснётся.</p>
            <div class="command-actions">
              {/* Both probes here are read-only (check_via_tunnel/check_direct issue
                  no mutation) -- this step only ever exists for the asleep gate, never
                  for a destructive confirm, so it stays primary rather than danger;
                  same reasoning as CommandButton's confirm button above. */}
              <button class="btn btn-primary" onClick={dispatch}>
                Да, выполнить
              </button>
              <button class="btn btn-ghost" onClick={() => setConfirming(false)}>
                Отмена
              </button>
            </div>
          </div>
        ) : (
          <button class="btn btn-primary compare-run" disabled={busy} onClick={handleClick}>
            {busy ? 'Сравниваю…' : ACTION_LABELS.recheck}
          </button>
        )}

        {attempted && (
          <div class="compare-probes">
            <ExitProbeBlock label="Через VPN" action="check_via_tunnel" state={viaTunnel} />
            <ExitProbeBlock label="Напрямую" action="check_direct" state={direct} />
          </div>
        )}

        {!busy && !singboxMode && sameIP && (
          <p class="compare-note compare-note-alert">Адреса совпадают — трафик идёт мимо туннеля.</p>
        )}
        {!busy && !singboxMode && bothIPs && !sameIP && (
          <p class="compare-note compare-note-good">Адреса разные — трафик действительно идёт через туннель.</p>
        )}
      </div>
    </section>
  )
}


// Легенда панели: пять ламп подписаны на корпусе четырьмя буквами, и без
// расшифровки они читаются как шифр. Порядок и набор -- те же, что у ламп
// (orderChecks), чтобы легенда не разошлась с прибором.
function DeviceLegend({ checks }) {
  const rows = orderChecks(checks ?? [])
  if (rows.length === 0) return null
  return (
    <ul class="legend card list-reset">
      {rows.map((c) => (
        <li key={c.check_name} class={`legend-item${c.status === 'fail' ? ' legend-item-fail' : ''}`}>
          <span class="legend-dot" />
          <span class="legend-key">{lampKey(c.check_name)}</span>
          <span class="legend-label">{legendLabel(c.check_name)}</span>
        </li>
      ))}
    </ul>
  )
}

export function RouterDetail({ id, isAdmin, onOpenAdmin, openSheet, onTab }) {
  const [router, setRouter] = useState(null)
  const [incidents, setIncidents] = useState([])
  const [checks, setChecks] = useState(null)
  const [tunnels, setTunnels] = useState([])
  const [traffic, setTraffic] = useState(null)
  const [error, setError] = useState(null)

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

  // The lamps' own set, in the lamps' own order (RouterDevice owns both). The
  // `tunnel_*` rows that `checks[]` also carries are filtered there -- they are the
  // antennas and the Туннели block above, and listing them here as well would show
  // every tunnel three times. Computed up here (ahead of the error/loading early
  // returns below) because the disclosure state hooks that follow must run on
  // every render in the same order -- see the rules-of-hooks note below.
  const otherChecks = orderChecks(checks ?? [])
  const otherChecksOkCount = otherChecks.filter((c) => c.status === 'ok').length
  // Sorted names of the currently-failing internal checks -- a signature the
  // effect below compares across renders to detect the set *growing*, as
  // opposed to just being non-empty (see that effect for why "non-empty" is
  // not the right question to ask on every render).
  const otherChecksFailing = otherChecks
    .filter((c) => c.status !== 'ok')
    .map((c) => c.check_name)
    .sort()
  const otherChecksFailingKey = otherChecksFailing.join(',')

  // §3.6: these four checks are the system's own plumbing vocabulary, not
  // something the router's owner opens this screen to read -- so the spoiler
  // defaults to collapsed unless something is already failing on first load.
  //
  // This used to be `open={otherChecksNeedAttention}` with `otherChecksNeedAttention`
  // recomputed fresh every render. That is provably unsafe: Preact's generic prop
  // diff for `<details>` compares the new `open` value against the *vnode's own
  // previous* prop value, not the live DOM (unlike its special-cased `value`/
  // `checked`). A native click on <summary> flips the DOM `open` attribute directly,
  // bypassing Preact entirely -- so if the reader collapses the spoiler while
  // `hydraroute` is failing, and `dns` *also* starts failing on a later refresh,
  // the recomputed boolean is still `true === true` from Preact's point of view and
  // it leaves the (now closed) DOM alone. A newly-red check would sit hidden behind
  // a spoiler the reader has every reason to believe they already dealt with.
  //
  // The fix: make this a genuinely controlled disclosure. `spoilerOpen` is real
  // component state, and `onToggle` mirrors every native toggle (collapse OR
  // expand, mouse or keyboard) back into that state -- so Preact's tracked
  // previous `open` prop always matches what's actually on screen, and the
  // desync above cannot happen. The effect below is the other half: it compares
  // the failing-check signature across renders and forces `spoilerOpen` back to
  // `true` whenever the set *grew*, even if the reader had manually collapsed it
  // -- a new problem overrides a manual dismissal. A refresh that reports the
  // exact same check(s) still failing does not touch `spoilerOpen`, so a reader
  // who already closed it is not re-annoyed by every poll.
  const [spoilerOpen, setSpoilerOpen] = useState(otherChecksFailing.length > 0)
  const prevFailingRef = useRef(new Set(otherChecksFailing))

  useEffect(() => {
    const prevFailing = prevFailingRef.current
    const grew = otherChecksFailing.some((name) => !prevFailing.has(name))
    if (grew) setSpoilerOpen(true)
    prevFailingRef.current = new Set(otherChecksFailing)
    // otherChecksFailingKey is the primitive form of otherChecksFailing (a new
    // array/closure every render) and is the real dependency here -- comparing
    // the array itself would rerun this every render regardless of content.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [otherChecksFailingKey])

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

  return (
    <div class="screen">
      <div class="router-header">
        <h1 class="screen-title">{router.nickname}</h1>
        <span class={`badge badge-${router.status}`}>{statusLabel(router.status)}</span>
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

      {/* Порядок блоков -- по срочности вопроса, а не по красоте: сначала то,
          что сломано, потом куда идёт трафик, потом состояние туннелей, и
          только затем действия. Тревога -- единственное, ради чего экран
          вообще открывают в плохой день, поэтому она выше прибора. */}
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
                openSheet={openSheet}
              />
            ))}
          </ul>
        </section>
      )}

      <RouterDevice tunnels={tunnels} traffic={traffic} checks={checks ?? []} name={router.nickname} />
      <DeviceLegend checks={checks} />

      <TrafficSection routerID={id} traffic={traffic} asleep={asleep} onDone={loadData} openSheet={openSheet} />

      <TunnelsSection tunnels={tunnels} traffic={traffic} />

      <QuickActions
        routerID={id}
        tunnels={tunnels}
        traffic={traffic}
        asleep={asleep}
        onDone={loadData}
        openSheet={openSheet}
        onTab={onTab}
      />

      <ExitCompareSection routerID={id} traffic={traffic} asleep={asleep} />

      {otherChecks.length > 0 && (
        <section class="section">
          {/* Controlled disclosure: `open` is driven by `spoilerOpen` state, and
              `onToggle` mirrors every native toggle back into it, so Preact's
              tracked previous `open` prop never desyncs from the live DOM (see
              the long comment above `spoilerOpen`'s declaration). The effect up
              there forces this back open whenever the failing-check set grows,
              even if the reader had manually collapsed it. */}
          <details
            class="checks-spoiler"
            open={spoilerOpen}
            onToggle={(e) => setSpoilerOpen(e.currentTarget.open)}
          >
            <summary class="section-title checks-spoiler-summary">
              Прочие проверки — {otherChecksOkCount} в норме
            </summary>
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
          </details>
        </section>
      )}

      {isAdmin && (
        <Section title="Администрирование">
          <ul class="card list-reset">
            <ListRow
              title="Обслуживание и доступы"
              sub="владелец, операторы; обслуживание пока в дашборде"
              onClick={onOpenAdmin}
            />
          </ul>
        </Section>
      )}
    </div>
  )
}
