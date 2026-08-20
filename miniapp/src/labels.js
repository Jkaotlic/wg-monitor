// The single place system vocabulary becomes human vocabulary.
//
// Wording is ported from the bot rather than invented: the operator reads those
// exact phrases every day, and two dialects for one fleet would be worse than
// either. Sources: internal/backend/alerts/smart_reply.go,
// internal/backend/alerts/format.go:700-710, internal/backend/tg/tunnels_panel.go:50-90.
//
// Pure module: no imports from screens, no DOM, no side effects. Every export
// below is a plain function or constant so later screens can pull in exactly
// the label they need.

const CHECK_LABELS = {
  dns: 'Определение адресов сайтов',
  external_reach: 'Доступ в интернет',
  hydraroute: 'Обход блокировок',
  awg_manager: 'Панель управления роутером',
  tunnels: 'Связь с панелью роутера',
}

// Check names are identifiers, not prose. Anything we don't have a human name
// for is shown as-is rather than mangled -- an honest unknown beats a wrong guess.
export function checkLabel(name) {
  if (CHECK_LABELS[name]) return CHECK_LABELS[name]
  if (name?.startsWith('tunnel_')) return `Туннель ${name.slice('tunnel_'.length)}`
  return name
}

// What a HARD incident means and what to do about it -- the incident card's
// second line, next to the plain check name from checkLabel above. Ported
// where the bot's own copy translates directly (categoryHeadline and the
// writeXWhatBroke bodies in alerts/format.go, the per-category hints in
// alerts/smart_reply.go's smartReplyActionHints); where the bot writes for an
// operator who already knows what DNS/HydraRoute/awg-manager are and this
// screen's reader may not, the "why" line explains the mechanism in plain
// words instead. See this task's report for the line-by-line comparison and
// every place the wording below departs from the bot's.
const INCIDENT_COPY = {
  external_reach: {
    what: 'Нет доступа в интернет',
    why: 'Роутер на связи и туннели подняты, но сайты снаружи не открываются. Обычно это провайдер или сторона VPN-сервера.',
  },
  dns: {
    what: 'Не определяются адреса сайтов',
    why: 'Роутер не может превратить имя сайта в адрес. Сайты не откроются, даже если интернет есть.',
  },
  hydraroute: {
    what: 'Не работает обход блокировок',
    why: 'Служба обхода не работает — заблокированные сайты будут открываться напрямую, как будто обхода нет.',
  },
  awg_manager: {
    what: 'Нет связи с панелью роутера',
    why: 'Агент не достучался до панели управления. Данные о туннелях могут устареть.',
  },
}

// checkName is either one of the four plain checks above or a `tunnel_<id>`
// incident; both are handled here so IncidentCard never has to branch on the
// name shape itself. Reuses checkLabel for the tunnel-id fallback (same
// "Туннель <id>" text the id gets everywhere else) and for the
// never-seen-this-check fallback -- an incident this function has no specific
// copy for gets checkLabel's honest name and an empty "why" rather than a
// guessed explanation, the same rule checkLabel itself follows for a name it
// doesn't recognize.
export function incidentCopy(checkName) {
  if (INCIDENT_COPY[checkName]) return INCIDENT_COPY[checkName]
  if (checkName?.startsWith('tunnel_')) {
    return {
      what: `${checkLabel(checkName)} не отвечает`,
      why: 'Обмен ключами не проходит — трафик через этот туннель не пойдёт.',
    }
  }
  return { what: checkLabel(checkName), why: '' }
}

// Восстановление -- это тоже новость, и говорить её надо тем же языком
// последствий, что и поломку: incidentCopy отвечает на «что сломалось»,
// эта пара -- на «что снова работает». Без неё журнал писал бы «dns: ok»,
// то есть имя механизма и его внутренний статус.
const RECOVERY_COPY = {
  external_reach: 'Сайты снаружи снова отвечают',
  dns: 'Адреса сайтов снова определяются',
  hydraroute: 'Обход блокировок снова работает',
  awg_manager: 'Связь с панелью роутера восстановлена',
  tunnels: 'Туннели снова опрашиваются',
}

export function eventPhrase(checkName, status) {
  if (status === 'ok') {
    if (RECOVERY_COPY[checkName]) return RECOVERY_COPY[checkName]
    if (checkName?.startsWith('tunnel_')) return `${checkLabel(checkName)} снова поднят`
    return `${checkLabel(checkName)} — снова в норме`
  }
  if (status === 'fail') return incidentCopy(checkName).what
  // Неизвестное состояние -- это ответ, а не пробел: проверка что-то
  // прислала, но словаря на это у нас нет.
  return `${checkLabel(checkName)} — состояние «${status}»`
}

// Spoken form of a check's status, for the instrument's <desc> and for the
// legend plate that names the same lamps. "не работает" is the spec's wording
// for a failed check (§3.7). The fallback echoes an unrecognized status rather
// than swallowing it -- same honesty rule as checkLabel above, and the one that
// matters most here: a status this function hasn't seen yet must not be spoken
// as either "работает" or "не работает".
export function checkStateLabel(status) {
  if (status === 'fail') return 'не работает'
  if (status === 'ok') return 'работает'
  return status ?? 'неизвестно'
}

// Mirrors alerts/format.go:700-710 (humanPingStatus) exactly, including its
// fallback: an unrecognized status is echoed as-is rather than swallowed into
// blank, same honesty rule as checkLabel above -- a status this function
// hasn't seen yet is still information, not nothing.
export function pingLabel(status) {
  switch (status) {
    case 'alive':
    case 'ok':
    case 'running':
      return 'живая'
    case 'dead':
    case 'fail':
    case 'failed':
      return 'падает'
    case 'disabled':
      return 'выключена'
    default:
      return status ?? ''
  }
}

// Failure and disabled wording is ported verbatim from humanTunnelStatus
// (tg/tunnels_panel.go:79-90) -- "остановлен" / "выключен" / "не на связи"
// mean the same thing to the bot's audience and this one, so they get the
// same words. The healthy branch is NOT a mirror, though: the bot's healthy
// row prints only "обмен ключами " + humanAgeShort(...) (tg/tunnels_panel.go:
// 60-61) -- a bare duration, no verdict -- because whoever reads the bot
// already has the context to judge whether that duration is fine. The mini
// app's reader can't be assumed to, so this branch adds a verdict word the
// bot never says; the consuming screen renders both together (e.g. "работает
// · обмен ключами 45 сек назад").
//
// One further departure, forced rather than chosen for clarity: `t.enabled`
// here is miniapp_tunnels.go's `*bool` (json `enabled,omitempty`), a NULLABLE
// tri-state -- true, false, or absent because an old agent (or an unparseable
// details blob) never reported it. tunnels_panel.go's `Enabled` is a plain
// bool sourced from a live awg-manager call, so it can safely treat "not
// enabled" as the first branch. We cannot: collapsing "we don't know" into
// `!t.enabled` would render an unknown as "выключен" (disabled) -- a guess
// presented as fact, which is the exact failure mode this whole phase exists
// to prevent. So "unknown" gets its own branch, checked before we assume
// either true or false.
export function tunnelStateLabel(t) {
  if (t.enabled === false) return 'выключен'
  if (t.enabled == null) return 'неизвестно'
  if (t.run_state && t.run_state !== 'running') {
    return { stopped: 'остановлен', disabled: 'выключен', dead: 'не на связи', fail: 'не на связи', failed: 'не на связи' }[t.run_state] ?? t.run_state
  }
  if (t.handshake_age_sec == null) return 'обмена ключами ещё не было'
  return 'работает'
}

// Not sourced from a single bot function: alerts/format.go:1178 (humanAgeSec)
// and tg/tunnels_panel.go:92 (humanAgeShort) already disagree with each other
// (no day bucket in either, different remainder handling), and neither is one
// of this file's cited sources. This is a fresh, simpler single-unit
// formatter -- the mini app needs a "дн" bucket the bot's helpers don't have
// (a router can sit offline for days, not just hours).
export function humanAge(sec) {
  if (sec == null) return ''
  if (sec < 60) return `${sec} сек`
  if (sec < 3600) return `${Math.floor(sec / 60)} мин`
  if (sec < 86400) return `${Math.floor(sec / 3600)} ч`
  return `${Math.floor(sec / 86400)} дн`
}

// The screen's headline. `unknown` is a real answer, not a failure to compute:
// an agent older than the routeTag change genuinely cannot tell us, and saying
// so beats naming whichever tunnel happens to be listed first. Field names
// verified against miniappTraffic's json tags (miniapp_tunnels.go:123-132):
// mode, egress_tunnel_id, egress_tunnel_name.
export function trafficLabel(traffic) {
  switch (traffic?.mode) {
    case 'vpn':
      return {
        title: 'Трафик идёт через VPN',
        detail: `Весь исходящий трафик уходит через «${traffic.egress_tunnel_name || traffic.egress_tunnel_id}»`,
      }
    case 'direct':
      return {
        title: 'Трафик идёт напрямую',
        detail: 'Ни один туннель не несёт основной маршрут — трафик уходит через провайдера',
      }
    case 'singbox':
      return {
        title: 'Маршрут выбирает sing-box',
        detail: 'Для каждого сайта отдельно — единого ответа «напрямую или через VPN» тут нет',
      }
    default:
      return {
        title: 'Куда идёт трафик — неизвестно',
        detail: 'Роутер пока не сообщает, какой туннель основной. Нажмите «Повторить проверку».',
      }
  }
}

// UI action vocabulary for buttons the mini app offers. Not a 1:1 mirror of
// backend action strings (miniapp_actions.go/miniapp_commands.go use
// "force_recheck", "tunnel_restart", ttl values "1h"/"4h"/"24h" -- a future
// screen maps those to these keys explicitly rather than by string equality).
// Where a matching bot button exists, its wording wins: "Перезапустить
// туннель" mirrors "🔁 Перезапустить туннель" (alerts/smart_reply.go:285,332)
// and "Повторить проверку" mirrors "🩺 Повторить проверку"
// (callbacks/notifier.go:142), both minus the emoji (this UI has its own
// icons). "ack"/"mute" have no equivalent standalone Telegram button caption
// to port from -- the bot spells these "Квитировать" / "Заглушить";
// RouterDetail.jsx renders the plainer wording below instead (Task 11), for
// the same reason this whole file exists.
export const ACTION_LABELS = {
  ack: 'Понятно, вижу',
  mute: 'Больше не напоминать',
  // Три варианта тишины подписаны одним словом каждый: на экране 390 px
  // "Не беспокоить 4 часа" занимает строку целиком, и три такие кнопки
  // превращают карточку тревоги в столбик. Смысл несёт подпись группы
  // ("Не беспокоить"), а кнопка -- только срок.
  silence1h: 'Час',
  silence4h: '4 часа',
  silence24h: 'Сутки',
  silenceGroup: 'Не беспокоить',
  recheck: 'Повторить проверку',
  restartTunnel: 'Перезапустить туннель',
}

// What a dispatched agent command's result means, once the agent has
// actually answered (busy/pending has its own copy at the call site -- this
// is only the resolved four: pkg/wire/types.go's CommandResult.Status is
// ok/err/locked/timeout).
//
// "err" surfaces the agent's own diagnostic (result.output) rather than a
// canned phrase -- swallowing it would hide the one thing the operator needs
// to decide what to do next, and every other check/incident string in this
// file already prefers an honest specific over a vague generic. tunnel_restart
// is the one exception, handled before this branch is reached -- see below.
//
// "ok" deliberately does NOT echo result.output, unlike err. For
// tunnel_restart specifically, Output is the agent's raw "restarted <ndms>
// ..." line (humanRestartResult, alerts/command_result.go:134-148), and
// ndms_name is router-internal topology the mini app is never otherwise
// shown (miniappResolveTunnelNDMSName, miniapp_commands.go, resolves it
// server-side and never attaches it to any response type a client sees).
// Printing it here on success would leak that same value back out through
// the command-result endpoint instead of the tunnel list -- a side door,
// not a fix, so success gets a fixed phrase per action instead.
//
// The failure side has the identical leak and needs the identical guard:
// tunnel_restart's error Output is the agent's ndmc failure string (runner.go
// actions/runner.go:523/526/530, e.g. "ndmc interface Wireguard0 down: exit
// status 1") -- same ndms_name/interface topology as the success line above,
// just spelled differently. Echoing result.output here would undo the ok
// branch's suppression through the other half of the same switch, so
// tunnel_restart gets a fixed failure phrase too, checked before the generic
// echo below. force_recheck/check_via_tunnel/check_direct are unaffected:
// their Output is exit IPs and per-site checkmarks, not router topology, so
// they keep the honest echo.
export function commandOutcomeLabel(action, result) {
  switch (result.status) {
    case 'locked':
      return 'Такая команда уже выполняется'
    case 'timeout':
      return 'Роутер не ответил вовремя'
    case 'err': {
      if (action === 'tunnel_restart') return 'Не удалось перезапустить туннель'
      const output = result.output?.trim() || ''
      // Агент отвечает "unknown action: X", когда на роутере стоит версия
      // старше приложения. Человеку это читается как поломка приложения, а
      // на деле это разница версий -- и чинится она обновлением агента.
      if (/^unknown action:/i.test(output)) {
        return 'Агент на этом роутере старше приложения и такого пока не умеет — обновите агента.'
      }
      return output || 'Команда завершилась с ошибкой'
    }
    default:
      if (action === 'tunnel_restart') return 'Туннель перезапущен'
      return routeOutcomeLabel(action, result.output) || 'Готово'
  }
}

// Маршрутные команды отвечают не строкой, а payload'ом из pkg/wire/routing.go,
// и "Готово" на нём -- не краткость, а потеря: перенос умеет пройти
// наполовину (CategoryResult.Failed), повышение звена -- сменить порядок, не
// сдвинув трафик, а add/delete -- применить правку и не суметь обновить
// маршрутизацию после неё (RouteApplyResult.Warning). Каждый из трёх случаев
// человек обязан увидеть до того, как решит, что дело сделано.
function routeOutcomeLabel(action, output) {
  const payload = parseJSON(output)
  if (!payload) return ''
  switch (action) {
    case 'route_rebind':
      return rebindOutcome(payload)
    case 'route_policy_promote':
      return promoteOutcome(payload)
    case 'route_add':
    case 'route_delete':
      return applyOutcome(action, payload)
    default:
      return ''
  }
}

function parseJSON(output) {
  if (typeof output !== 'string' || output.trim() === '') return null
  try {
    const value = JSON.parse(output)
    return value && typeof value === 'object' ? value : null
  } catch {
    return null
  }
}

// hr_neo -- подмножество dns (комментарий у wire.RouteRebindResult), поэтому
// в сумму оно не идёт: сложить их значило бы посчитать одни и те же правила
// дважды.
function rebindOutcome(res) {
  const cats = [res.dns, res.static]
  if (cats.every((c) => !c || typeof c.ok !== 'number')) return ''
  const moved = cats.reduce((n, c) => n + (c?.ok ?? 0), 0)
  const failed = cats.reduce((n, c) => n + (c?.failed ?? 0), 0)
  if (failed > 0) {
    const why = (res.dns?.errors ?? res.static?.errors ?? [])[0]
    const tail = why ? ` (${why})` : ''
    return `Перенесено ${moved} ${pluralRu(moved, 'правило', 'правила', 'правил')}, не удалось ${failed}${tail}`
  }
  if (moved === 0) return 'Переносить было нечего: правил на этом туннеле нет'
  return `Перенесено ${rulesCount(moved)}`
}

// Ответ -- свежая политика (wire.RoutePolicySummary). Первое звено цепочки и
// звено, несущее трафик, -- разные вещи: повысить можно выключённый туннель,
// и тогда порядок сменился, а трафик остался там же. Сказать про это обязан
// тот же экран, который повышение и запустил.
function promoteOutcome(policy) {
  const links = Array.isArray(policy.interfaces) ? policy.interfaces : []
  const first = links[0]
  if (!policy.name || !first) return ''
  const linkName = (l) => l.name || l.bind
  const active = links.find((l) => l.tunnel_id && l.tunnel_id === policy.active_tunnel_id)
  if (!active) {
    return `Первым звеном политики «${policy.name}» стал «${linkName(first)}», но доступного звена в цепочке сейчас нет`
  }
  if (active.bind !== first.bind) {
    return `Первым звеном политики «${policy.name}» стал «${linkName(first)}», но трафик пока идёт через «${linkName(active)}»`
  }
  return `Правила политики «${policy.name}» идут через «${linkName(first)}»`
}

function applyOutcome(action, res) {
  const name = (res.route_name || res.route_id || '').trim()
  if (!name) return ''
  const base = action === 'route_add' ? `Правило «${name}» создано` : `Правило «${name}» убрано`
  const warning = (res.warning || '').trim()
  return warning ? `${base}, но с оговоркой: ${warning}` : base
}

// Четыре состояния роутера из dashboard_handler.go:780-796 -- единственный
// словарь состояний в приложении. Второй развалился бы с этим при первой же
// правке бэкенда.
const STATUS_LABEL = {
  online: 'В сети',
  sleeping: 'Спит',
  offline: 'Офлайн',
  alert: 'Тревога',
}

export function statusLabel(status) {
  return STATUS_LABEL[status] ?? status
}

// Подписи для легенды панели: на корпусе лампа подписана четырьмя буквами, и
// рядом нужна расшифровка в два-три слова, а не полное имя проверки -- иначе
// легенда перестаёт быть легендой и превращается во второй список проверок.
const LEGEND_LABEL = {
  dns: 'адреса сайтов',
  external_reach: 'интернет',
  hydraroute: 'обход блокировок',
  awg_manager: 'панель роутера',
  tunnels: 'связь с ботом',
}

export function legendLabel(name) {
  return LEGEND_LABEL[name] ?? checkLabel(name)
}

// Русское числительное с существительным: 1 правило, 2 правила, 5 правил.
// Нужно потому, что "2 правил" на экране маршрутов читается как опечатка, а
// не как счётчик, и подрывает доверие к самим цифрам.
export function pluralRu(n, one, few, many) {
  const mod100 = Math.abs(n) % 100
  const mod10 = mod100 % 10
  if (mod100 >= 11 && mod100 <= 14) return many
  if (mod10 === 1) return one
  if (mod10 >= 2 && mod10 <= 4) return few
  return many
}

// Живость туннеля словами. Словарь один на приложение: второй разошёлся бы
// с первым на первой же правке, а строки эти стоят рядом на одном экране.
export const TUNNEL_LIVE_LABEL = {
  up: 'работает',
  down: 'выключен',
  unknown: 'состояние неизвестно',
}

export function tunnelLiveLabel(live) {
  return TUNNEL_LIVE_LABEL[live] ?? live ?? ''
}

export function rulesCount(n) {
  return `${n} ${pluralRu(n, 'правило', 'правила', 'правил')}`
}
