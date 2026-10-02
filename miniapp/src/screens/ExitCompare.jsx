import { useState } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import { isRussianText } from '../errorText.js'
import { commandOutcomeLabel } from '../labels.js'
import { Quoted } from '../ui/Q.jsx'

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
      {isRussianText(state.result?.output) && <p class="compare-probe-detail">{String(state.result.output).trim()}</p>}
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
export function ExitCompareSection({ routerID, traffic, asleep }) {
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
  // traffic.mode is Task 3's own derivation. On a sing-box router, the route is chosen per destination, so
  // these two probes -- hitting different sites for the via-tunnel and direct
  // checks -- were never guaranteed to take the same path in the first place.
  // Equal or different, neither answer generalizes to "all traffic", so this
  // mode gets a caveat instead of either verdict below, and gets it up front
  // (before the button, not just after a run) so the reader isn't primed to
  // expect a confident answer.
  const singboxMode = traffic?.mode === 'singbox'

  return (
    <div class="card exit-compare">
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
              {/* Оба зонда только читают (check_via_tunnel / check_direct ничего
                  не меняют): шаг подтверждения есть лишь для спящего роутера, а
                  не для опасного действия -- поэтому кнопка обычная, не красная. */}
              <button class="btn btn-ghost" onClick={dispatch}>
                Да, выполнить
              </button>
              <button class="btn btn-ghost" onClick={() => setConfirming(false)}>
                Отмена
              </button>
            </div>
          </div>
        ) : (
          <button class="btn btn-ghost compare-run" disabled={busy} onClick={handleClick}>
            {/* Не «Проверить заново»: так называется опрос роутера выше на
                «Проверках», а здесь запускаются два зонда наружу. Одинаковые
                слова на кнопках, делающих разное, -- ловушка. */}
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
  )
}
