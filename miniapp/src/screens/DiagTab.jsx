import { agentReplyText } from '../errorText.js'
import { useEffect, useState } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import { fetchRouter, fetchRouterChecks } from '../api.js'
import { DIAG_SECTIONS, parseDiag, checkRows, reportHint } from '../diag.js'
import { dnsSplitView } from '../dnsSplit.js'
import { humanAge, tunnelCountSummary, workingTunnelCount, workingTunnelNote, uncheckedTunnelCount } from '../labels.js'
import { isStale } from '../staleness.js'
import { serverClockOffset } from '../serverClock.js'
import { agoText } from '../when.js'
import { Section, HeadingGroup } from '../ui/Section.jsx'
import { Stat } from '../ui/Stat.jsx'
import { DataRow } from '../ui/DataRow.jsx'
import { Quoted } from '../ui/Q.jsx'
import { ExitIPSection, WANSection } from './SignalSections.jsx'
import { PingCheckSection, InspectSection } from './CheckToolsSections.jsx'
import { ExitCompareSection } from './ExitCompare.jsx'
import { ErrorLine } from '../ui/ErrorLine.jsx'

// Диагностика отвечает на вопрос «что из этого следует», а не «какая проверка
// моргнула»: пять строк данных, у каждой -- ответ и измерение. Числа берутся
// из того, что роутер уже прислал (checks/tunnels), поэтому экран открывается
// сразу, а не после команды на роутер.
//
// Три команды здесь -- три разных вопроса, и потому три отдельных useCommand:
// «спроси заново» (force_recheck), «покажи себя целиком» (diag_now) и «каким
// адресом меня видно снаружи» (check_direct + check_via_tunnel). Один хук на
// всех сделал бы ответ одной команды ответом любой другой.
//
// Машинные имена проверок (dns, hydraroute, agent_heartbeat) -- для того, кто
// полезет в консоль, то есть для админа. Владельцу они ничего не говорят и
// только теснят вопрос: ему -- без них.
export function DiagGroup({ id, children }) {
  const section = DIAG_SECTIONS.find((s) => s.id === id)
  return (
    <section id={`dg-${id}`} class="diag-group">
      <h2 class="diag-group-title">{section.title}</h2>
      {/* Заголовок группы -- h2: разделы внутри -- h3 (вид -- по классам). */}
      <HeadingGroup>{children}</HeadingGroup>
    </section>
  )
}

export function DiagTab({ routerID, asleep, isAdmin = false, openSheet }) {
  const deadline = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }
  const [data, setData] = useState(null)
  const [error, setError] = useState(null)
  const [showRaw, setShowRaw] = useState(false)

  const recheck = useCommand(routerID)
  const report = useCommand(routerID)

  function load() {
    return Promise.all([fetchRouter(routerID), fetchRouterChecks(routerID)])
      .then(([r, c]) => {
        setData({
          router: r.router,
          checks: c.checks ?? [],
          tunnels: c.tunnels ?? [],
          incidents: r.incidents ?? [],
          // Сдвиг часов телефона снимается в момент ответа (MINI-10).
          clockOffsetMs: serverClockOffset(r.router),
          traffic: c.traffic ?? null,
        })
        setError(null)
      })
      .catch(() => setError('Не удалось прочитать состояние проверок. Откройте экран заново.'))
  }

  useEffect(() => {
    setData(null)
    setShowRaw(false)
    load()
  }, [routerID])

  if (error) return <p class="state state-error">{error}</p>
  if (data == null) return <p class="state">Загрузка…</p>

  const rows = checkRows(data)
  const age = data.router?.last_seen_age_sec
  const silent = isStale(data.router)
  // То же правило, что на «Сейчас» (MINI-07).
  const tunnelsAlive = workingTunnelCount(data.tunnels, data.incidents)
  const tunnelsUnchecked = uncheckedTunnelCount(data.tunnels)
  const parsedReport = report.result?.status === 'ok' ? parseDiag(report.result.output) : null
  const runRecheck = () => recheck.run('force_recheck', {}, deadline).then((res) => { if (res?.status === 'ok') load() })
  const split = dnsSplitView(data.checks, { silent, agentVersion: data.router?.agent_version })

  return (
    <div class="screen">
      <div class="router-header">
        <h1 class="screen-title">Проверки</h1>
        <button type="button" class="btn btn-ghost" disabled={recheck.busy} onClick={load}>
          Обновить
        </button>
      </div>
      <p class="router-lastseen">
        {rows.length} {rows.length === 1 ? 'вопрос' : 'вопросов'} роутеру
        {age != null ? ` · последний отчёт ${agoText(age)}` : ''}
      </p>

      <div class="stat-grid">
        <Stat
          label="VPN-туннели"
          value={silent || !data.tunnels.length ? null : tunnelsAlive}
          note={
            silent
              ? 'данные устарели'
              : data.tunnels.length
                ? workingTunnelNote(tunnelsAlive, tunnelCountSummary(data.tunnels, data.incidents).total, tunnelsUnchecked)
                : 'роутер не сообщил ни одного'
          }
          tone={!silent && data.tunnels.length && tunnelsAlive === 0 && tunnelsUnchecked === 0 ? 'danger' : undefined}
        />
        <Stat
          label="отчёт о себе"
          value={age != null ? humanAge(age) : null}
          unit={age != null ? 'назад' : ''}
          note={silent ? 'роутер молчит' : 'роутер на связи'}
          tone={silent ? 'warn' : undefined}
        />
      </div>

      <DiagGroup id="answers">
        <div class="card card-rows">
          {rows.map((r) => (
            <div key={r.key} class="data-row-group">
              <DataRow
                dot={r.tone === 'ok' ? 'ok' : r.tone === 'danger' ? 'danger' : r.tone === 'warn' ? 'warn' : undefined}
                title={r.title}
                code={isAdmin ? r.code : undefined}
                value={r.answer}
                valueSub={r.value}
                valueTone={r.tone === 'muted' ? undefined : r.tone}
              />
              {/* «Нет» напротив вопроса не говорит, ЧЕМ это грозит. Строка
                  последствия появляется только у сломанного: у живой
                  проверки она была бы шумом. */}
              {r.consequence && <p class="diag-consequence">{r.consequence}</p>}
            </div>
          ))}
          {/* Молчащий роутер -- не поломка сам по себе, но он делает всё выше
              вчерашним, и сказать это надо там же, где показания. */}
          <p class="card-foot">
            {silent
              ? 'Пока роутер молчит, всё выше — данные на момент последнего отчёта, а не на сейчас.'
              : 'Ответы собраны роутером при последнем отчёте. «Проверить заново» просит его спросить заново.'}
          </p>
        </div>
        <button type="button" class="btn btn-primary btn-wide" disabled={recheck.busy} onClick={runRecheck}>
          {recheck.busy ? 'Спрашиваем роутер…' : 'Проверить заново'}
        </button>
        <p class="hint">
          Проверка ничего не меняет на роутере: он заново спрашивает те же вещи и присылает ответ.
        </p>
        <ErrorLine text={recheck.error} busy={recheck.busy} onRetry={runRecheck} />
        {recheck.result && recheck.result.status !== 'ok' && (
          <p class="state state-error">{agentReplyText(recheck.result, 'Роутер не переспросил — попробуйте ещё раз через минуту.')}</p>
        )}
      </DiagGroup>

      <DiagGroup id="exit">
        <ExitCompareSection routerID={routerID} traffic={data.traffic} asleep={asleep} />
        <ExitIPSection routerID={routerID} tunnels={data.tunnels} deadline={deadline} />
      </DiagGroup>

      <DiagGroup id="net">
        <WANSection routerID={routerID} />
      {/* Кому роутер отдал русские зоны и как идут запросы к Яндексу. Ответ --
          по настройкам роутера, а не замер, и оговорка стоит здесь же. */}
      <Section title="Раздельный DNS">
        <div class="card card-rows">
          {split.missing && <p class="card-foot">{split.note}</p>}
          {split.rows.map((r) => (
            <DataRow
              key={r.key}
              dot={r.tone === 'muted' ? undefined : r.tone}
              title={r.lead}
              value={r.zones}
              valueTone={r.tone === 'ok' ? undefined : r.tone}
            />
          ))}
          {split.route && (
            <p class={`diag-consequence${split.route.tone === 'warn' ? ' card-foot-bad' : ''}`}>
              <Quoted text={split.route.text} />
            </p>
          )}
          {split.resolves && <p class="diag-consequence card-foot-bad">{split.resolves.text}</p>}
          {split.foot.map((line) => (
            <p key={line} class="card-foot">
              <Quoted text={line} />
            </p>
          ))}
        </div>
      </Section>
      </DiagGroup>

      <DiagGroup id="ping">
        <PingCheckSection routerID={routerID} asleep={asleep} openSheet={openSheet} tunnels={data.tunnels} onChanged={load} />
      </DiagGroup>

      <DiagGroup id="inspect">
        <InspectSection routerID={routerID} asleep={asleep}>
        <button
          type="button"
          class="btn btn-ghost btn-wide"
          disabled={report.busy}
          onClick={() => report.run('diag_now', {}, deadline)}
        >
          {report.busy ? 'Собираем…' : 'Собрать отчёт'}
        </button>
        <p class="hint">
          {reportHint(parsedReport).map((line) => (
            <span key={line} class="hint-line">
              {line}
            </span>
          ))}
        </p>

        <ErrorLine text={report.error} busy={report.busy} onRetry={() => report.run('diag_now', {}, deadline)} />
        {report.result && report.result.status !== 'ok' && (
          <p class="state state-error">{agentReplyText(report.result, 'Роутер не собрал отчёт — попробуйте ещё раз через минуту.')}</p>
        )}

        {parsedReport && parsedReport.cards.length > 0 && (
          <div class="diag-cards">
            {parsedReport.cards.map((c) => (
              <div key={c.key} class={`card diag-card diag-card-${c.tone}`}>
                <div class="diag-card-head">
                  <span class="row-title">{c.title}</span>
                  <span class={`diag-verdict diag-verdict-${c.tone}`}>{c.verdict}</span>
                </div>
                {/* Строки карточки -- «VPN-туннель «имя»: …»: имя через <Quoted>. */}
                {c.detail && (
                  <p class="tunnel-sub">
                    <Quoted text={c.detail} />
                  </p>
                )}
              </div>
            ))}
          </div>
        )}

        {parsedReport && parsedReport.cards.length === 0 && (
          <p class="state">
            Отчёт получен, но знакомых полей в нём нет — версия панели роутера отвечает иначе.
            Ниже он целиком.
          </p>
        )}

        {parsedReport && (
          <>
            <button type="button" class="btn btn-ghost raw-toggle" onClick={() => setShowRaw((v) => !v)}>
              {showRaw ? 'Скрыть ответ агента' : 'Ответ агента целиком'}
            </button>
            {showRaw && <pre class="raw-dump">{parsedReport.raw}</pre>}
          </>
        )}
        </InspectSection>
      </DiagGroup>
    </div>
  )
}
