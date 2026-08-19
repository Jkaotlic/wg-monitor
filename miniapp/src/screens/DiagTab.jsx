import { useEffect, useState } from 'preact/hooks'
import { useCommand } from '../useCommand.js'
import { parseDiag } from '../diag.js'
import { Section } from '../ui/Section.jsx'

function stamp(iso, durationMs) {
  if (!iso) return ''
  const when = new Date(iso).toLocaleString('ru-RU', { dateStyle: 'short', timeStyle: 'short' })
  return durationMs ? `${when} · сбор занял ${(durationMs / 1000).toFixed(1)} с` : when
}

// Диагностика: полный отчёт роутера, разобранный по проверкам. Отчёт не
// собирается сам при открытии -- это команда на роутер, и запускать её без
// спроса каждый раз, когда человек листает табы, незачем.
export function DiagTab({ routerID, asleep }) {
  const { busy, result, error, run } = useCommand(routerID)
  const [showRaw, setShowRaw] = useState(false)

  useEffect(() => {
    setShowRaw(false)
  }, [routerID])

  const report = result?.status === 'ok' ? parseDiag(result.output) : null

  return (
    <div class="screen">
      <div class="router-header">
        <h1 class="screen-title">Диагностика</h1>
        <button
          type="button"
          class="btn btn-primary"
          disabled={busy}
          onClick={() => run('diag_now', {}, { deadlineMs: asleep ? 6 * 60_000 : 90_000 })}
        >
          {busy ? 'Собираю…' : 'Собрать отчёт'}
        </button>
      </div>
      <p class="router-lastseen">
        {report ? stamp(report.generatedAt, report.durationMs) : 'Роутер проверит сам себя и пришлёт отчёт — обычно это несколько секунд.'}
      </p>

      {busy && <p class="state">Ждём ответа от роутера…</p>}
      {error && <p class="state state-error">{error}</p>}
      {result && result.status !== 'ok' && (
        <p class="state state-error">Роутер не собрал отчёт: {result.output || result.status}</p>
      )}

      {report && report.cards.length > 0 && (
        <div class="diag-cards">
          {report.cards.map((c) => (
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

      {report && report.cards.length === 0 && (
        <p class="state">
          Отчёт получен, но знакомых полей в нём нет — версия панели роутера отвечает иначе.
          Ниже он целиком.
        </p>
      )}

      {report && (
        <Section>
          <button type="button" class="btn btn-ghost raw-toggle" onClick={() => setShowRaw((v) => !v)}>
            {showRaw ? 'Скрыть ответ агента' : 'Ответ агента целиком'}
          </button>
          {showRaw && <pre class="raw-dump">{report.raw}</pre>}
        </Section>
      )}
    </div>
  )
}
