import { useEffect, useState } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import { fetchRouter, fetchRouterChecks } from '../api.js'
import { parseDiag, checkRows, exitCompare } from '../diag.js'
import { humanAge } from '../labels.js'
import { Section } from '../ui/Section.jsx'
import { Stat } from '../ui/Stat.jsx'
import { DataRow } from '../ui/DataRow.jsx'

function stamp(iso, durationMs) {
  if (!iso) return ''
  const when = new Date(iso).toLocaleString('ru-RU', { dateStyle: 'short', timeStyle: 'short' })
  return durationMs ? `${when} · сбор занял ${(durationMs / 1000).toFixed(1)} с` : when
}

// Диагностика отвечает на вопрос «что из этого следует», а не «какая проверка
// моргнула»: пять строк данных, у каждой -- ответ и измерение. Числа берутся
// из того, что роутер уже прислал (checks/tunnels), поэтому экран открывается
// сразу, а не после команды на роутер.
//
// Три команды здесь -- три разных вопроса, и потому три отдельных useCommand:
// «спроси заново» (force_recheck), «покажи себя целиком» (diag_now) и «каким
// адресом меня видно снаружи» (check_direct + check_via_tunnel). Один хук на
// всех сделал бы ответ одной команды ответом любой другой.
export function DiagTab({ routerID, asleep }) {
  const deadline = { deadlineMs: asleep ? 6 * 60_000 : 90_000 }
  const [data, setData] = useState(null)
  const [error, setError] = useState(null)
  const [showRaw, setShowRaw] = useState(false)

  const recheck = useCommand(routerID)
  const report = useCommand(routerID)
  const direct = useCommand(routerID)
  const viaTunnel = useCommand(routerID)

  function load() {
    return Promise.all([fetchRouter(routerID), fetchRouterChecks(routerID)])
      .then(([r, c]) => {
        setData({
          router: r.router,
          checks: c.checks ?? [],
          tunnels: c.tunnels ?? [],
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
  const silent = data.router?.status === 'offline' || data.router?.status === 'sleeping'
  const tunnelsAlive = data.tunnels.filter((t) => t.status === 'ok').length
  const parsedReport = report.result?.status === 'ok' ? parseDiag(report.result.output) : null
  const exits = exitCompare(
    direct.result?.status === 'ok' ? direct.result.output : null,
    viaTunnel.result?.status === 'ok' ? viaTunnel.result.output : null,
  )
  const measuring = direct.busy || viaTunnel.busy

  return (
    <div class="screen">
      <div class="router-header">
        <h1 class="screen-title">Диагностика</h1>
        <button type="button" class="btn btn-ghost" disabled={recheck.busy} onClick={load}>
          Обновить
        </button>
      </div>
      <p class="router-lastseen">
        {rows.length} {rows.length === 1 ? 'вопрос' : 'вопросов'} роутеру
        {age != null ? ` · последний отчёт ${humanAge(age)} назад` : ''}
      </p>

      <div class="stat-grid">
        <Stat
          label="туннели"
          value={data.tunnels.length ? `${tunnelsAlive} из ${data.tunnels.length}` : null}
          note={data.tunnels.length ? 'на связи' : 'роутер не сообщил ни одного'}
          tone={data.tunnels.length && tunnelsAlive === 0 ? 'danger' : undefined}
        />
        <Stat
          label="отчёт о себе"
          value={age != null ? humanAge(age) : null}
          unit={age != null ? 'назад' : ''}
          note={silent ? 'роутер молчит' : 'роутер на связи'}
          tone={silent ? 'warn' : undefined}
        />
      </div>

      <Section title="Что спросили и что ответили">
        <div class="card">
          {rows.map((r) => (
            <DataRow
              key={r.key}
              dot={r.tone === 'ok' ? 'ok' : r.tone === 'danger' ? 'danger' : r.tone === 'warn' ? 'warn' : undefined}
              title={r.title}
              code={r.code}
              value={r.answer}
              valueSub={r.value}
              valueTone={r.tone === 'muted' ? undefined : r.tone}
            />
          ))}
          {/* Молчащий роутер -- не поломка сам по себе, но он делает всё выше
              вчерашним, и сказать это надо там же, где показания. */}
          <p class="card-foot">
            {silent
              ? 'Пока роутер молчит, всё выше — данные на момент последнего отчёта, а не на сейчас.'
              : 'Ответы собраны роутером при последнем отчёте. «Проверить сейчас» просит его спросить заново.'}
          </p>
        </div>
      </Section>

      <button
        type="button"
        class="btn btn-primary btn-wide"
        disabled={recheck.busy}
        onClick={() => recheck.run('force_recheck', {}, deadline).then((res) => {
          if (res?.status === 'ok') load()
        })}
      >
        {recheck.busy ? 'Спрашиваем роутер…' : 'Проверить сейчас'}
      </button>
      <p class="hint">
        Проверка ничего не меняет на роутере: он заново спрашивает те же вещи и присылает ответ.
      </p>
      {recheck.error && <p class="state state-error">{recheck.error}</p>}
      {recheck.result && recheck.result.status !== 'ok' && (
        <p class="state state-error">Роутер не переспросил: {recheck.result.output || recheck.result.status}</p>
      )}

      <Section title="Каким адресом видно снаружи">
        <div class="card">
          <DataRow
            title="Напрямую, мимо туннеля"
            code="check_direct"
            value={exits.direct || (direct.busy ? 'меряем…' : 'не измерен')}
            valueTone={exits.direct ? undefined : 'muted'}
          />
          <DataRow
            title="Через туннель"
            code="check_via_tunnel"
            value={exits.viaTunnel || (viaTunnel.busy ? 'меряем…' : 'не измерен')}
            valueTone={exits.viaTunnel ? undefined : 'muted'}
          />
          <p class={`card-foot${exits.works === false ? ' card-foot-bad' : ''}`}>{exits.verdict}</p>
        </div>
        <button
          type="button"
          class="btn btn-ghost btn-wide"
          disabled={measuring}
          onClick={() => {
            direct.run('check_direct', {}, deadline)
            viaTunnel.run('check_via_tunnel', {}, deadline)
          }}
        >
          {measuring ? 'Меряем оба адреса…' : 'Сравнить адреса'}
        </button>
        {(direct.error || viaTunnel.error) && (
          <p class="state state-error">{direct.error || viaTunnel.error}</p>
        )}
      </Section>

      <Section title="Отчёт роутера о себе">
        <button
          type="button"
          class="btn btn-ghost btn-wide"
          disabled={report.busy}
          onClick={() => report.run('diag_now', {}, deadline)}
        >
          {report.busy ? 'Собираем…' : 'Собрать отчёт'}
        </button>
        <p class="hint">
          {parsedReport
            ? stamp(parsedReport.generatedAt, parsedReport.durationMs)
            : 'Роутер проверит сам себя и пришлёт отчёт — обычно это несколько секунд.'}
        </p>

        {report.error && <p class="state state-error">{report.error}</p>}
        {report.result && report.result.status !== 'ok' && (
          <p class="state state-error">Роутер не собрал отчёт: {report.result.output || report.result.status}</p>
        )}

        {parsedReport && parsedReport.cards.length > 0 && (
          <div class="diag-cards">
            {parsedReport.cards.map((c) => (
              <div key={c.key} class={`card diag-card diag-card-${c.tone}`}>
                <div class="diag-card-head">
                  <span class="row-title">{c.title}</span>
                  <span class={`diag-verdict diag-verdict-${c.tone}`}>{c.verdict}</span>
                </div>
                {c.detail && <p class="tunnel-sub">{c.detail}</p>}
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
      </Section>
    </div>
  )
}
