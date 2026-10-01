import { useContext, useEffect, useRef, useState } from 'preact/hooks'
import { fetchRouter, fetchRouterChecks, fetchIncidentHistory, silenceIncident, ackIncident, muteIncident, fetchRouterVersions } from '../api.js'
import { orderChecks } from '../checksOrder.js'
import { maintenanceNotice } from '../maintenanceNotice.js'
import { TrafficPath } from '../components/TrafficPath.jsx'
import { pathState, reserveLine, backupCopy, deadReserveLine, heroCoversReserve } from '../trafficPath.js'
import { whenText, sinceText, untilText } from '../when.js'
import { errorText } from '../errorText.js'
import { ErrorLine } from '../ui/ErrorLine.jsx'
import { routerHeadline } from '../routerHeadline.js'
import { isStale } from '../staleness.js'
import { Hero } from '../ui/Hero.jsx'
import { Quoted } from '../ui/Q.jsx'
import { StateTag } from '../ui/StateTag.jsx'
import { Stat } from '../ui/Stat.jsx'
import { NavCard } from '../ui/NavCard.jsx'
import { PanelLine } from '../ui/PanelLine.jsx'
import { shouldPulse, freshnessLabel, PULSE_MS } from '../pulse.js'
import { useOnClose } from '../useOnClose.js'
import { useCommand } from '../useCommand.js'
import { confirmSheet, localSheet } from '../sheet.js'
import { AppContext } from '../appContext.js'
import {
  ACTION_LABELS,
  checkLabel,
  tunnelOf,
  checkState as checkStateOf,
  workingTunnelCount,
  workingTunnelNote,
  uncheckedTunnelCount,
  commandOutcomeLabel,
  incidentCopy,
  legendLabel,
  pingLabel,
  statusLabel,
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

function formatDateTime(iso) {
  if (!iso) return ''
  return whenText(iso)
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
      <button type="button" class={btnClass} disabled={busy} onClick={handleClick}>
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
function IncidentCard({ routerID, incident, onUpdate, asleep, onDone, openSheet, whySuppressed = false, tunnels = [], primary = false, onRepair }) {
  const [expanded, setExpanded] = useState(false)
  const [history, setHistory] = useState(null)
  const [historyTruncated, setHistoryTruncated] = useState(false)
  const [historyError, setHistoryError] = useState(null)

  function loadHistory() {
    setHistoryError(null)
    fetchIncidentHistory(routerID, incident.check_name)
      .then((data) => {
        setHistory(data.transitions ?? [])
        setHistoryTruncated(!!data.truncated)
      })
      .catch((err) => setHistoryError(errorText(err)))
  }

  function toggleHistory() {
    const next = !expanded
    setExpanded(next)
    if (next && history == null) loadHistory()
  }

  const suppressed = isSuppressed(incident)
  const { what, why, code } = incidentCopy(incident.check_name, tunnels)
  const lineName = tunnelOf(incident.check_name, tunnels)?.name || null

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
    <li class="card incident-card">
      <div class="incident-head">
        <span class="row-title">
          <Quoted text={what} />
        </span>
        {code && <u class="data-row-code">{code}</u>}
        {incident.hard_since && <span class="incident-since">{sinceText(incident.hard_since)}</span>}
      </div>

      {why && !whySuppressed && <p class="incident-why">{why}</p>}

      {/* Deliberately OUTSIDE the suppressed/!suppressed split below: silence/
          ack/mute only control whether this incident nags again, they never
          touch the router, so a muted tunnel incident must not lose the one
          button that can actually fix it. */}
      {/* Главная кнопка экрана -- «Починить» первой тревоги по VPN-туннелю:
          лайм на всю ширину (v0.50, спека п. 1.2). У второй такой тревоги
          она контурная -- лайм на экране один. Перезапуск и «Не
          беспокоить…» -- пара ниже, одной высоты. */}
      {tunnelID && (
        <button type="button" class={`btn ${primary ? 'btn-primary' : 'btn-ghost'} btn-wide repair-open`} onClick={() => onRepair?.({ checkName: incident.check_name, lineName: lineName || tunnelID })}>
          Починить
        </button>
      )}
      {(tunnelID || !suppressed) && (
        <div class="action-row action-row-pair incident-actions-row">
          {tunnelID && (
            <CommandButton
              routerID={routerID}
              action="tunnel_restart"
              args={{ tunnel_id: tunnelID }}
              label={ACTION_LABELS.restartTunnel}
              busyLabel="Перезапускаю…"
              mutatingText={`Перезапустить ${checkLabel(incident.check_name, tunnels)}? Связь через него на несколько секунд прервётся.`}
              asleep={asleep}
              wrapClass="restart-block"
              btnClass="btn btn-ghost"
              onDone={onDone}
              openSheet={openSheet}
              sheetTitle={ACTION_LABELS.restartTunnel}
            />
          )}
          {!suppressed && (
            <button type="button" class="btn btn-ghost" onClick={askSilence}>
              {ACTION_LABELS.silenceGroup}…
            </button>
          )}
        </div>
      )}

      {/* Wording mirrors the backend's own confirmation lines (alertaction.go's
          ApplyAck/ApplySilence): acked and silenced/muted are indistinguishable
          here, so the silenced branch uses ApplySilence's phrasing. */}
      {suppressed && (
        <span class="badge badge-offline incident-quiet">
          {incident.acked
            ? 'Вижу проблему — напомним после восстановления'
            : `Уведомления скрыты до ${untilText(incident.silenced_until)}`}
        </span>
      )}

      {/* История -- справка, а не действие: тихая ссылка, не кнопка. */}
      <button type="button" class="link-quiet incident-history-toggle" onClick={toggleHistory}>
        {expanded ? 'Скрыть историю' : 'История за 24ч'}
      </button>

      {expanded && (
        <div class="incident-history">
          <ErrorLine text={historyError} onRetry={loadHistory} />
          {historyError == null && history == null && <p class="state">Загрузка…</p>}
          {history != null && history.length === 0 && <p class="state">Нет событий за 24ч</p>}
          {history != null && history.length > 0 && (
            <>
              {historyTruncated && <p class="state">показаны только последние события</p>}
              <ul class="list-reset history-list">
                {history.map((t, i) => (
                  <li key={`${t.ts}-${i}`} class={`history-entry history-entry-${t.status}`}>
                    {whenText(t.ts)} · {t.label}
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

export function RouterDetail({ id, panelURL, reserveOnlyAlert, openSheet, onTab, openLayer, repairOpen = false, otherAlert = null, onOpenRouter, onOpenService }) {
  const { wide } = useContext(AppContext)
  const [router, setRouter] = useState(null)
  const [incidents, setIncidents] = useState([])
  const [checks, setChecks] = useState(null)
  const [tunnels, setTunnels] = useState([])
  const [traffic, setTraffic] = useState(null)
  const [error, setError] = useState(null)
  const loadSeq = useRef(0)
  // Версии -- один раз на роутер, не с пульсом: сервер отмечает новости
  // показанными, и дёргать его каждые 10 с незачем -- версии меняются раз в дни.
  const [versions, setVersions] = useState(null)
  useEffect(() => {
    let alive = true
    setVersions(null)
    fetchRouterVersions(id)
      .then((v) => {
        if (alive) setVersions(v)
      })
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [id])

  // Named rather than inlined so a completed command can call it again:
  // an "ok" force_recheck or tunnel_restart changes state that lives in
  // this same fetch (checks/tunnels/traffic, and possibly the incident
  // list), and the screen otherwise never refetches after the initial load.
  // Not guaranteed to show the change immediately -- the agent's own next
  // heartbeat is still the real source of truth -- but it costs one cheap
  // request and shows whatever is currently true rather than leaving stale
  // data on screen indefinitely after a button says "done".
  //
  // MINI-04: каждый ответ помечен номером запроса, и рисуется только самый
  // свежий -- поздний ответ по прошлому роутеру (или прошлому такту) не
  // ложится поверх нового. Удача снимает прошлую ошибку: раньше одна ошибка
  // навсегда подменяла экран своим текстом.
  function loadData() {
    const my = ++loadSeq.current
    return Promise.all([fetchRouter(id), fetchRouterChecks(id)])
      .then(([r, c]) => {
        if (my !== loadSeq.current) return
        setError(null)
        setRouter(r.router)
        setIncidents(r.incidents ?? [])
        setChecks(c.checks ?? [])
        setTunnels(c.tunnels ?? [])
        // A backend older than this phase sends no `traffic` at all; the
        // headline and the traffic path both read a missing one as "unknown",
        // which is the honest answer rather than a defaulted-away one.
        setTraffic(c.traffic ?? null)
      })
      .catch((err) => {
        if (my !== loadSeq.current) return
        setError(errorText(err))
      })
  }

  // Починка -- слой навигации: после её закрытия тревоги читаются заново.
  useOnClose(repairOpen, () => loadData())

  // Экран живёт сам. Раньше данные грузились ровно один раз при входе, и
  // строка «41 сек назад» через пять минут врала: человек смотрел на прошлое,
  // поданное как настоящее. Опрос идёт только пока вкладка открыта -- Telegram
  // держит мини-апп живым дольше, чем на него смотрят.
  useEffect(() => {
    // Новый роутер -- чистый экран: показания прошлого под чужим именем
    // были бы той же ложью, что и поздний ответ.
    setRouter(null)
    setIncidents([])
    setChecks(null)
    setTunnels([])
    setTraffic(null)
    setError(null)
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
  // Молчащий роутер: подписи проверок -- в прошедшем (MINI-02).
  const checksStale = isStale(router)
  const checkState = (c) => checkStateOf(c, { stale: checksStale })
  // Над спойлером -- только красное и жёлтое. Серое («сторож не следит»,
  // незнакомый статус) -- не поломка и остаётся внутри вместе с исправным.
  const isFailing = (c) => ['danger', 'warn'].includes(checkState(c).tone)
  const failingChecks = otherChecks.filter(isFailing)

  if (router == null) {
    return error ? (
      <div class="screen">
        <ErrorLine text={error} onRetry={loadData} />
      </div>
    ) : (
      <p class="state">Загрузка…</p>
    )
  }

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
  // v0.46: плюс молчащая тревога (stale от сервера) -- staleness.js.
  const asleep = isStale(router)

  // Шапка -- главная новость экрана, и порядок её веток задан в
  // routerHeadline: молчащий роутер перебивает любое другое показание.
  // reserveOnlyAlert -- из строки списка роутеров: «всё работает, резерва
  // нет» говорим только по слову сервера (он видит политики целиком).
  const headline = routerHeadline({ router, traffic, incidents, tunnels, reserveOnlyAlert })
  const path = pathState({ traffic, incidents, tunnels, stale: headline.stale })
  // Резерв -- живые запасные звенья политики несущего (reserve_tunnel_ids от
  // бэкенда), а у старых агентов -- любой живой VPN-туннель, кроме несущего.
  // Правила -- в trafficPath.reserveLine, рядом со схемой: они обязаны
  // говорить про тот же несущий туннель, что и она.
  const backupLine = reserveLine({ traffic, tunnels, incidents, via: path.via })
  // Запасной есть, но упал -- это не «запасного нет» (v0.50, спека п. 1.3).
  // Когда шапка уже говорит о тревоге по VPN-туннелю, плитка молчит.
  const deadReserve = backupLine ? null : deadReserveLine({ traffic, tunnels, incidents, via: path.via })
  const heroCovers = heroCoversReserve(deadReserve, headline.check)
  // Работающий -- поднятый интерфейс, чья проверка не провалена и по кому нет
  // тревоги. Одного «поднят» мало: на workrouter 18.09 интерфейс nl2 стоял
  // running с мёртвой удалённой стороной, и плитка писала «2 из 2».
  const liveCount = workingTunnelCount(tunnels, incidents)
  // «Не проверено» -- ни работающий, ни упавший (unknown, v0.46).
  const uncheckedCount = uncheckedTunnelCount(tunnels)

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
      {/* Обновление не удалось, а прошлые данные есть: экран остаётся, но
          говорит, что он не свежий (MINI-04). */}
      <ErrorLine text={error ? `Не удалось обновить: ${error}` : ''} onRetry={loadData} />
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
              ? workingTunnelNote(liveCount, tunnels.length, uncheckedCount)
              : 'роутер не сообщил ни одного'
        }
        tone={!headline.stale && tunnels.length && liveCount === 0 && uncheckedCount === 0 ? 'danger' : undefined}
      />
    </div>
  )

  // Резерв -- ответ на вопрос «а если этот VPN-туннель ляжет». Раньше его не
  // было нигде, и человек узнавал ответ в момент падения.
  const backup = backupCopy({ backupLine, carrierDown: path.tunnel === 'down', deadReserve, heroCovers })
  const backupBlock = backup && (
    <div class="card row" style="margin-top:12px">
      <div>
        <div class="row-title">
          <Quoted text={backup.title} />
        </div>
        <div class="row-note">
          <Quoted text={backup.note} />
        </div>
      </div>
      <span class={backup.tone === 'ok' ? 'dot dot-ok' : 'dot dot-warn'} />
    </div>
  )


  // Перезагрузка или обновление -- видно всем, кто открыл роутер (оператор
  // 18.09), а не только дошедшему до «Управления». Нажатие ведёт туда.
  const notice = maintenanceNotice(versions)
  const maintBlock = notice && (
    <button type="button" class={`card maint-notice maint-notice-${notice.tone}`} onClick={() => onOpenService?.()}>
      <span class="maint-notice-title">{notice.title}</span>
      {notice.note && <span class="maint-notice-note">{notice.note}</span>}
      {notice.lines.map((l) => (
        <span key={l} class="maint-notice-line">
          {l}
        </span>
      ))}
      <span class="maint-notice-go">Открыть «Настройки» → «Обслуживание»</span>
    </button>
  )

  const primaryCheck = incidents.find((i) => i.check_name.startsWith('tunnel_'))?.check_name
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
              whySuppressed={inc.check_name === headline.check}
              onUpdate={updateIncident}
              asleep={asleep}
              onDone={loadData}
              onRepair={(p) => openLayer?.('repair', p)}
              openSheet={openSheet}
              tunnels={tunnels}
              primary={inc.check_name === primaryCheck}
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

  const otherAlertBlock = otherAlert ? (
    <button type="button" class="card other-alert" onClick={() => onOpenRouter?.(otherAlert.id)}>
      {`На «${otherAlert.nickname}» тревога — открыть`}
    </button>
  ) : null

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

  // Спойлер «Проверки» ушёл (v0.52): он дублировал вкладку «Проверки». Здесь
  // остаётся только сломанное -- оно часть ответа «что не так».
  const checksBlock =
    failingChecks.length > 0 ? (
      <section class="section">
        <h2 class="section-title">Проверки не в порядке</h2>
        <ul class="card list-reset checks-failing">{failingChecks.map(checkRow)}</ul>
      </section>
    ) : null

  if (!wide) {
    // Порядок -- по срочности (v0.50, спека п. 1.1): при тревоге сразу под
    // шапкой -- тревоги с «Починить», чтобы кнопка была в первом экране на
    // 360 px; без тревоги incidentsBlock пуст, и порядок прежний.
    // Порядок блоков -- по срочности вопроса, а не по красоте: сначала то,
    // что сломано, потом куда идёт трафик, потом состояние туннелей, и
    // только затем действия. Тревога -- единственное, ради чего экран
    // вообще открывают в плохой день, поэтому она выше прибора.
    return (
      <div class="screen">
        {otherAlertBlock}
        {heroBlock}
        {incidentsBlock}
        {maintBlock}
        {statsBlock}
        {backupBlock}
        {tunnelsNavBlock}
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
        {otherAlertBlock}
        {heroBlock}
        {maintBlock}
        {statsBlock}
        {backupBlock}
        {tunnelsNavBlock}
        {checksBlock}
      </div>
      <div class="now-side">
        {incidentsBlock}
      </div>
    </div>
  )
}
