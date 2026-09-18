import { useContext, useEffect, useState } from 'preact/hooks'
import {
  fetchRouter,
  fetchRouterChecks,
  fetchIncidentHistory,
  silenceIncident,
  ackIncident,
  muteIncident,
} from '../api.js'
import { orderChecks } from '../checksOrder.js'
import { TrafficPath } from '../components/TrafficPath.jsx'
import { pathState, reserveLine } from '../trafficPath.js'
import { routerHeadline } from '../routerHeadline.js'
import { Hero } from '../ui/Hero.jsx'
import { Quoted } from '../ui/Q.jsx'
import { StateTag } from '../ui/StateTag.jsx'
import { Stat } from '../ui/Stat.jsx'
import { NavCard } from '../ui/NavCard.jsx'
import { Section } from '../ui/Section.jsx'
import { ActionTile } from '../ui/ActionTile.jsx'
import { PanelLine } from '../ui/PanelLine.jsx'
import { tunnelHealth } from './tunnelHealth.js'
import { shouldPulse, freshnessLabel, PULSE_MS } from '../pulse.js'
import { RepairScreen } from './RepairScreen.jsx'
import { useCommand } from '../useCommand.js'
import { confirmSheet, localSheet } from '../sheet.js'
import { AppContext } from '../appContext.js'
import {
  ACTION_LABELS,
  checkLabel,
  checkState,
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

// Пять вариантов «не беспокоить» -- за одной кнопкой с листом (спека C2):
// раньше пять кнопок стояли в карточке тревоги рядом с починкой и спорили с
// ней за внимание. Порядок -- от мягкого к окончательному; «Больше не
// напоминать» -- опасное, оно одно красное.
export function silenceChoices() {
  return [
    ...SILENCE_OPTIONS.map((o) => ({ value: o.ttl, label: ACTION_LABELS[o.labelKey] })),
    { value: 'ack', label: ACTION_LABELS.ack },
    { value: 'mute', label: ACTION_LABELS.mute, danger: true },
  ]
}

// Локаль прибита к ru-RU, как в остальных экранах: с локалью браузера
// русский интерфейс показывал время тревоги как «8/21/26, 9:40 AM» --
// оператор сверяет эти отметки с логами роутера, и чужой формат тут не
// украшение, а лишний перевод в уме.
function formatTime(iso) {
  if (!iso) return ''
  return new Date(iso).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' })
}

function formatDateTime(iso) {
  if (!iso) return ''
  return new Date(iso).toLocaleString('ru-RU', { dateStyle: 'short', timeStyle: 'short' })
}

function isSuppressed(incident) {
  return incident.acked || (incident.silenced_until != null && new Date(incident.silenced_until) > new Date())
}

// One button that owns the whole asynchronous-command lifecycle for a single
// action: an optional confirm step, dispatch, the bounded poll (useCommand),
// and a plain-language rendering of whatever it settles on. Two call sites
// share this -- the restart button on
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
function CommandButton({ routerID, action, args = {}, label, busyLabel, mutatingText, asleep, wrapClass, btnClass = 'btn btn-primary', onDone, openSheet, sheetTitle }) {
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
      <button class={btnClass} disabled={busy} onClick={handleClick}>
        {busy ? (busyLabel ?? 'Выполняю…') : label}
      </button>
      {busy && <p class="state">Ждём ответа от роутера…</p>}
      {result && (
        <p class={`state${result.status === 'ok' ? '' : ' state-error'}`}>
          <Quoted text={commandOutcomeLabel(action, result)} />
        </p>
      )}
      {error && <p class="state state-error">{error}</p>}
    </div>
  )
}

// whySuppressed ставится для той тревоги, которую уже назвала шапка экрана.
// Повторять её объяснение слово в слово двумя блоками ниже -- это не
// «подчеркнуть», а заставить прочитать одно и то же дважды и потерять время
// в тот момент, когда его меньше всего.
function IncidentCard({ routerID, incident, onUpdate, asleep, onDone, openSheet, whySuppressed = false }) {
  const [repairOpen, setRepairOpen] = useState(false)
  const [expanded, setExpanded] = useState(false)
  const [history, setHistory] = useState(null)
  const [historyTruncated, setHistoryTruncated] = useState(false)
  const [historyError, setHistoryError] = useState(null)

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

  // Лист «Не беспокоить»: выбор уходит на сервер, карточка обновляется его
  // ответом. Без листа (экран без оболочки) -- вариантов нет вовсе: пять
  // кнопок в карточке и были тем, от чего уходили.
  function askSilence() {
    if (!openSheet) return
    openSheet(
      localSheet({
        title: ACTION_LABELS.silenceGroup,
        body: `«${what}» — когда напомнить снова? Роутер это не меняет: только уведомления.`,
        choices: silenceChoices(),
        perform: (_typed, _values, choice) => {
          if (choice === 'ack') return ackIncident(routerID, incident.check_name)
          if (choice === 'mute') return muteIncident(routerID, incident.check_name)
          return silenceIncident(routerID, incident.check_name, choice)
        },
        onDone: (data) => {
          if (data?.incident) onUpdate(data.incident)
        },
      }),
    )
  }

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

      {why && !whySuppressed && <p class="incident-why">{why}</p>}

      {/* Deliberately OUTSIDE the suppressed/!suppressed split below: silence/
          ack/mute only control whether this incident nags again, they never
          touch the router, so a muted tunnel incident must not lose the one
          button that can actually fix it. */}
      {/* Починка и перезапуск -- пара одного размера (спека C2), акцент --
          только у починки: она уводит трафик на резерв, перевыпускает конфиг и
          возвращает VPN-туннель на место. Перезапуск -- ручной инструмент:
          бесполезен, когда мертва удалённая сторона, полезен, когда подвис
          сам туннель. */}
      {tunnelID && (
        <div class="incident-pair">
          <button class="btn btn-accent repair-open" onClick={() => setRepairOpen(true)}>
            Починить
          </button>
          <CommandButton
            routerID={routerID}
            action="tunnel_restart"
            args={{ tunnel_id: tunnelID }}
            label={ACTION_LABELS.restartTunnel}
            busyLabel="Перезапускаю…"
            mutatingText={`Перезапустить ${checkLabel(incident.check_name)}? Связь через него на несколько секунд прервётся.`}
            asleep={asleep}
            wrapClass="restart-block"
            btnClass="btn btn-ghost"
            onDone={onDone}
            openSheet={openSheet}
            sheetTitle={ACTION_LABELS.restartTunnel}
          />
        </div>
      )}
      {repairOpen && (
        <RepairScreen
          routerID={routerID}
          checkName={incident.check_name}
          lineName={tunnelID}
          onClose={() => {
            setRepairOpen(false)
            onDone?.()
          }}
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
          <button class="btn btn-ghost" onClick={askSilence}>
            {ACTION_LABELS.silenceGroup}…
          </button>
        </div>
      )}

      {/* История -- справка, а не действие: тихая ссылка, не кнопка. */}
      <button type="button" class="link-quiet incident-history-toggle" onClick={toggleHistory}>
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
                  body: `Перезапустить VPN-туннель «${egress.name || egress.tunnel_id}»? Связь через него на несколько секунд прервётся.`,
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
        {/* Прежде эта кнопка жила в блоке "Куда идёт трафик" вместе с
            вердиктом. Вердикт уехал в шапку экрана, и держать ради одной
            кнопки целый раздел, повторяющий шапку словами, незачем. */}
        <ActionTile
          title={ACTION_LABELS.recheck}
          hint="роутер опросит себя заново"
          onClick={() =>
            openSheet(
              confirmSheet({
                routerID,
                title: ACTION_LABELS.recheck,
                body: 'Роутер прогонит свои проверки заново и пришлёт свежий отчёт.',
                action: 'force_recheck',
                buttonLabel: 'Проверить',
                asleep,
                onDone,
              }),
            )
          }
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
        {singboxMode && (
          <p class="compare-note compare-note-caution">
            На этом роутере маршрут выбирается для каждого сайта отдельно (sing-box) — два адреса ниже не складываются в
            общий ответ <Quoted text="«весь трафик идёт туда-то»" />.
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
            {/* Не "Повторить проверку": так называется опрос роутера в
                быстрых действиях, а здесь запускаются два зонда наружу.
                Одинаковые слова на кнопках, делающих разное, -- ловушка. */}
            {busy ? 'Сравниваю…' : 'Сравнить адреса выхода'}
          </button>
        )}

        {attempted && (
          <div class="compare-probes">
            <ExitProbeBlock label="Через VPN" action="check_via_tunnel" state={viaTunnel} />
            <ExitProbeBlock label="Напрямую" action="check_direct" state={direct} />
          </div>
        )}

        {!busy && !singboxMode && sameIP && (
          <p class="compare-note compare-note-alert">Адреса совпадают — трафик идёт мимо VPN-туннеля.</p>
        )}
        {!busy && !singboxMode && bothIPs && !sameIP && (
          <p class="compare-note compare-note-good">Адреса разные — трафик действительно идёт через VPN-туннель.</p>
        )}

        {/* Меньше текста до кнопки (спека C3): как устроена проверка --
            для того, кто спросит, а не для каждого, кто пришёл нажать. */}
        <details class="compare-how">
          <summary>Как это работает</summary>
          <p class="traffic-detail">
            Запускает оба зонда сразу и показывает, под каким адресом роутер выходит в интернет через VPN-туннель обхода и напрямую.
            Разные адреса — трафик идёт через VPN-туннель; одинаковые — мимо него. Ничего на роутере не меняет.
          </p>
        </details>
      </div>
    </section>
  )
}


export function RouterDetail({ id, panelURL, openSheet, onTab }) {
  const { wide } = useContext(AppContext)
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
        // and the traffic path both read a missing one as "unknown", which is
        // the honest answer rather than a defaulted-away one.
        setTraffic(c.traffic ?? null)
      })
      .catch((err) => setError(err.message))
  }

  // Экран живёт сам. Раньше данные грузились ровно один раз при входе, и
  // строка «41 сек назад» через пять минут врала: человек смотрел на прошлое,
  // поданное как настоящее. Опрос идёт только пока вкладка открыта -- Telegram
  // держит мини-апп живым дольше, чем на него смотрят.
  useEffect(() => {
    loadData()
    if (!shouldPulse({ visible: true, routerID: id })) return undefined
    const timer = setInterval(() => {
      if (document.visibilityState === 'visible') loadData()
    }, PULSE_MS)
    // Возврат к вкладке -- повод обновиться немедленно, а не ждать такта.
    const onVisibility = () => {
      if (document.visibilityState === 'visible') loadData()
    }
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      clearInterval(timer)
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [id])

  function updateIncident(updated) {
    setIncidents((prev) => prev.map((inc) => (inc.check_name === updated.check_name ? updated : inc)))
  }

  // Служебные проверки в общем порядке (checksOrder владеет им). Строки
  // `tunnel_*` отфильтрованы там же -- они уже есть на схеме и во вкладке
  // «VPN-туннели», и третий раз их здесь не показываем.
  //
  // v0.41 (спека C1): проваленные проверки -- отдельным списком над
  // спойлером, всегда на виду; в спойлере остаются только исправные. Раньше
  // сломанное пряталось в спойлере вместе с исправным, и спойлер приходилось
  // насильно раскрывать при каждой новой поломке.
  const otherChecks = orderChecks(checks ?? [])
  const okChecks = otherChecks.filter((c) => checkState(c).tone === 'ok')
  const failingChecks = otherChecks.filter((c) => checkState(c).tone !== 'ok')

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

  // Шапка -- главная новость экрана, и порядок её веток задан в
  // routerHeadline: молчащий роутер перебивает любое другое показание.
  const headline = routerHeadline({ router, traffic, incidents, tunnels })
  const path = pathState({ traffic, incidents, tunnels, stale: headline.stale })
  // Резерв -- живые запасные звенья политики несущего (reserve_tunnel_ids от
  // бэкенда), а у старых агентов -- любой живой VPN-туннель, кроме несущего.
  // Правила -- в trafficPath.reserveLine, рядом со схемой: они обязаны
  // говорить про тот же несущий туннель, что и она.
  const backupLine = reserveLine({ traffic, tunnels, incidents, via: path.via })
  // Работающий -- поднятый интерфейс, чья проверка не провалена и по кому нет
  // тревоги. Одного «поднят» мало: на workrouter 18.09 интерфейс nl2 стоял
  // running с мёртвой удалённой стороной, и плитка писала «2 из 2».
  const liveCount = tunnels.filter(
    (t) =>
      tunnelStateLabel(t) === 'работает' &&
      t.status !== 'fail' &&
      !incidents.some((i) => i.check_name === `tunnel_${t.tunnel_id}`),
  ).length

  // Схема живёт внутри шапки: рисунок и вывод под ним -- одно высказывание,
  // а не картинка и подпись к ней. Холодная подсветка включается тем же
  // признаком, что и тон метки. На широком экране имя роутера уже стоит в
  // шапке основной области, и второй раз его не пишем.
  const heroBlock = (
    <Hero cold={headline.cold}>
      <StateTag tone={headline.tone}>{headline.tag}</StateTag>
      {!wide && <h1 class="screen-title" style="margin:8px 0 0">{router.nickname}</h1>}
      {/* Адрес панели awg-manager (владельцу и админу): нажатие открывает её
          во внешнем браузере. На широком экране он стоит в шапке. */}
      {!wide && <PanelLine url={panelURL} />}
      <p class="traffic-detail" style="margin-top:6px">
        <Quoted text={headline.verdict} />
      </p>
      <TrafficPath traffic={traffic} incidents={incidents} tunnels={tunnels} stale={headline.stale} />
      {/* Число туннелей -- только в плитке «VPN-туннели» ниже (спека C1):
          здесь оно повторяло плитку. Остаётся лишь оговорка про устаревшие
          показания молчащего роутера. */}
      {headline.stale && (
        <div class="hero-bar">
          <span>показания на момент последнего отчёта</span>
        </div>
      )}
    </Hero>
  )

  // Два показания, ради которых экран открывают чаще всего.
  const statsBlock = (
    <div class="stat-grid" style="margin-top:12px">
      {/* На молчащем роутере число поднятых VPN-туннелей -- это данные на момент
          последнего отчёта, а не сейчас. Показать их как текущее показание
          значило бы соврать ровно тем способом, против которого написана
          половина этого приложения: цифра выглядит достоверной именно
          потому, что она цифра. */}
      {/* Задержка -- та, что меряется ЧЕРЕЗ туннель (матрица awg-manager),
          а не ping-check роутера: у того цель достижима и мимо туннеля.
          Её нет у роутеров с awg-manager старше 2.18, и тогда плитка честно
          говорит «роутер не сказал», а не рисует ноль. */}
      <Stat
        label="задержка"
        value={path.latencyMs != null && !headline.stale ? path.latencyMs : null}
        unit="мс"
        note={
          headline.stale
            ? 'данные устарели'
            : path.latencyMs == null
              ? 'роутер не сказал'
              : path.latencyMs < 150
                ? 'быстро'
                : 'медленно'
        }
        tone={path.latencyMs != null && path.latencyMs >= 300 ? 'warn' : undefined}
      />
      <Stat
        label="VPN-туннели"
        value={headline.stale || !tunnels.length ? null : liveCount}
        note={
          headline.stale
            ? 'роутер молчит — данные устарели'
            : tunnels.length
              ? `${liveCount === 1 ? 'работает' : 'работают'} из ${tunnels.length} настроенных`
              : 'роутер не сообщил ни одного'
        }
        tone={!headline.stale && tunnels.length && liveCount === 0 ? 'danger' : undefined}
      />
    </div>
  )

  // Резерв -- ответ на вопрос «а если этот VPN-туннель ляжет». Раньше его не
  // было нигде, и человек узнавал ответ в момент падения.
  const backupBlock = (
    <div class="card row" style="margin-top:12px">
      <div>
        <div class="row-title">{backupLine ? 'Запасной VPN-туннель готов' : 'Запасного VPN-туннеля нет'}</div>
        <div class="row-note">
          <Quoted
            text={
              backupLine
                ? backupLine.name
                  ? `«${backupLine.name}» подхватит, если этот замолчит`
                  : 'второй VPN-туннель подхватит, если один замолчит'
                : 'если VPN-туннель ляжет, обход блокировок пропадёт до починки'
            }
          />
        </div>
      </div>
      <span class={backupLine ? 'dot dot-ok' : 'dot dot-warn'} />
    </div>
  )

  const incidentsBlock =
    incidents.length > 0 ? (
      <section class="section">
        <h2 class="section-title">Активные тревоги</h2>
        <ul class="list-reset card-stack">
          {incidents.map((inc, i) => (
            <IncidentCard
              key={inc.check_name}
              routerID={id}
              incident={inc}
              whySuppressed={i === 0 && headline.tone === 'danger'}
              onUpdate={updateIncident}
              asleep={asleep}
              onDone={loadData}
              openSheet={openSheet}
            />
          ))}
        </ul>
      </section>
    ) : null

  const tunnelsNavBlock = (
    <div style="margin-top:20px">
      <NavCard title="VPN-туннели и резерв" onClick={() => onTab?.('tunnels')} />
    </div>
  )

  // Блоки -- общие для обеих раскладок, но стоят в разных родителях (одна
  // колонка на телефоне, .now-main/.now-side на широком). Смена ширины окна
  // через порог 1024 px пересоздаёт их: ход быстрой команды и сравнения
  // выходов на экране теряется. Сама команда уже ушла на роутер и
  // выполнится; её итог виден по следующему опросу экрана и в «Что было».
  const quickBlock = (
    <QuickActions
      routerID={id}
      tunnels={tunnels}
      traffic={traffic}
      asleep={asleep}
      onDone={loadData}
      openSheet={openSheet}
      onTab={onTab}
    />
  )

  const compareBlock = <ExitCompareSection routerID={id} traffic={traffic} asleep={asleep} />

  const checkRow = (c) => {
    const st = checkState(c)
    return (
      <li key={c.check_name} class="row checks-row">
        <span class="row-title">{checkLabel(c.check_name)}</span>
        <span class={`checks-status checks-status-${st.tone}`}>
          {st.label} · {formatDateTime(c.ts)}
        </span>
      </li>
    )
  }

  const checksBlock =
    otherChecks.length > 0 ? (
      <section class="section">
        {failingChecks.length > 0 && (
          <>
            <h2 class="section-title">Проверки не в порядке</h2>
            <ul class="card list-reset checks-failing">{failingChecks.map(checkRow)}</ul>
          </>
        )}
        {okChecks.length > 0 && (
          <details class="checks-spoiler">
            <summary class="section-title checks-spoiler-summary">
              {failingChecks.length > 0 ? 'Прочие проверки' : 'Проверки'} — {okChecks.length} в норме
            </summary>
            <ul class="card list-reset">{okChecks.map(checkRow)}</ul>
          </details>
        )}
      </section>
    ) : null

  if (!wide) {
    // Порядок блоков -- по срочности вопроса, а не по красоте: сначала то,
    // что сломано, потом куда идёт трафик, потом состояние туннелей, и
    // только затем действия. Тревога -- единственное, ради чего экран
    // вообще открывают в плохой день, поэтому она выше прибора.
    return (
      <div class="screen">
        {heroBlock}
        {statsBlock}
        {backupBlock}
        {incidentsBlock}
        {tunnelsNavBlock}
        {quickBlock}
        {compareBlock}
        {checksBlock}
      </div>
    )
  }

  // Широкий экран: слева -- что происходит (схема пути, плитки, резерв,
  // сравнение выходов, прочие проверки), справа -- что с этим делать
  // (тревоги, быстрые действия). Порядок внутри колонок тот же.
  return (
    <div class="screen now-grid">
      <div class="now-main">
        {heroBlock}
        {statsBlock}
        {backupBlock}
        {tunnelsNavBlock}
        {compareBlock}
        {checksBlock}
      </div>
      <div class="now-side">
        {incidentsBlock}
        {quickBlock}
      </div>
    </div>
  )
}
