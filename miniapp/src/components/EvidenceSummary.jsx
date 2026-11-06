import { statusEvidenceCopy } from '../statusEvidence.js'

const SOURCE = {
  bot_check: 'Проверка бота',
  router: 'Роутер',
  awg_manager: 'Панель VPN',
  hydraroute: 'Маршрутизация',
}

const STATE = {
  ok: 'отвечает',
  running: 'поднят',
  up: 'поднят',
  connected: 'на связи',
  disconnected: 'нет связи',
  fail: 'не отвечает',
  stopped: 'остановлен',
  broken: 'сломался',
  unverified: 'не проверено',
  unknown: 'неизвестно',
}

export function EvidenceSummary({ evidence }) {
  const copy = statusEvidenceCopy(evidence)
  if (!copy) return null
  const sources = Array.isArray(evidence.sources) ? evidence.sources : []
  const title = `${copy.title}${copy.detail ? ` · ${copy.detail}` : ''}`
  if (sources.length === 0) {
    return <p class={`evidence-summary evidence-summary-${copy.tone}`}>{title}</p>
  }
  return (
    <details class={`evidence-summary evidence-summary-${copy.tone}`}>
      <summary>{title}</summary>
      <ul>
        {sources.map((source) => (
          <li key={source.source}>{SOURCE[source.source] ?? source.source}: {STATE[source.state] ?? source.state}</li>
        ))}
      </ul>
    </details>
  )
}
