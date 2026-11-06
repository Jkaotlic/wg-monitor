const COPY = {
  sources_agree: ['ok', 'Источники согласны'],
  source_conflict: ['warn', 'Источники расходятся'],
  evidence_stale: ['muted', 'Данные устарели'],
  evidence_time_missing: ['muted', 'Время проверки неизвестно'],
  clock_skew: ['warn', 'Часы источника идут неверно'],
  carrier_failed: ['danger', 'Несущий VPN-туннель не отвечает'],
  source_unverified: ['warn', 'Не все источники проверены'],
  address_routed: ['ok', 'Маршрут выбирается по адресу'],
  direct_path: ['ok', 'Трафик идёт напрямую'],
  route_control_failed: ['danger', 'Правила маршрутизации не работают'],
  route_control_unknown: ['muted', 'Состояние маршрутизации неизвестно'],
  routing_evidence_missing: ['muted', 'Нет свежей проверки маршрутизации'],
  bot_check_unknown: ['muted', 'Проверка туннеля не завершена'],
  router_state_unknown: ['muted', 'Роутер не подтвердил состояние'],
  carrier_unknown: ['muted', 'Несущий VPN-туннель не определён'],
}

// Evidence is a ceiling, never an optimistic replacement. A healthy backend
// verdict allows the screen to keep its more specific local result, but cannot
// turn a locally failed branch green.
export function capTunnelState(base, evidence) {
  if (!evidence?.state || evidence.state === 'healthy') return base
  if (evidence.state === 'failed') return 'down'
  return 'unknown'
}

export function statusEvidenceCopy(evidence) {
  if (!evidence?.reason_code) return null
  const [tone, title] = COPY[evidence.reason_code] ?? ['muted', 'Статус требует проверки']
  const routes = Number(evidence.affected_routes)
  return {
    tone,
    title,
    detail: routes > 0 ? `${routes} правил маршрутизации требуют проверки` : '',
  }
}
