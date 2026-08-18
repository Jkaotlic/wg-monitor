// Разбор отчёта awg-manager /api/diagnostics/result в карточки экрана.
//
// Форма ответа принадлежит awg-manager и между версиями меняется, поэтому
// здесь ровно та же защитная стратегия, что у бэкенда в
// internal/backend/alerts/diag_report.go: незнакомое поле молча пропускаем,
// нераспознанный ответ не выдумываем, а показываем сырым.

const TEST_LABELS = {
  handshake: 'Обмен ключами',
  dns: 'Определение адресов',
  ping: 'Проверка связи',
  route: 'Маршрутизация',
  mtu: 'Размер пакета (MTU)',
  external: 'Выход в интернет',
}

function testLabel(slug) {
  return TEST_LABELS[slug] ?? slug
}

// Провал на одном туннеле важнее успеха на остальных: экран должен показать
// худший исход, а не средний.
function aggregateTone(statuses) {
  if (statuses.includes('fail')) return 'danger'
  if (statuses.includes('skip') || statuses.includes('warn')) return 'warn'
  if (statuses.every((s) => s === 'ok')) return 'ok'
  return 'muted'
}

export function parseDiag(output) {
  const raw = typeof output === 'string' ? output : ''
  let report = null
  try {
    report = JSON.parse(raw)
  } catch {
    return { parsed: false, raw, cards: [], generatedAt: null, durationMs: null }
  }
  if (!report || typeof report !== 'object') {
    return { parsed: false, raw, cards: [], generatedAt: null, durationMs: null }
  }

  const cards = []

  const sys = report.system
  if (sys && typeof sys === 'object') {
    const parts = []
    if (sys.appVersion) parts.push(`awg-manager ${sys.appVersion}`)
    if (sys.keeneticOS) parts.push(`KeeneticOS ${sys.keeneticOS}`)
    if (sys.uptime) parts.push(`работает ${sys.uptime}`)
    if (sys.totalMemoryMB) parts.push(`память ${sys.totalMemoryMB} МБ`)
    const moduleLoaded = sys.kernelModule?.loaded
    cards.push({
      key: 'system',
      title: 'Роутер',
      verdict: moduleLoaded === false ? 'модуль ядра не загружен' : 'отвечает',
      tone: moduleLoaded === false ? 'danger' : 'ok',
      detail: parts.join(' · '),
    })
  }

  const wan = report.wan
  if (wan && typeof wan === 'object') {
    const ifaces = Object.entries(wan.interfaces ?? {})
    cards.push({
      key: 'wan',
      title: 'Канал провайдера',
      verdict: wan.anyUp ? 'есть связь' : 'связи нет',
      tone: wan.anyUp ? 'ok' : 'danger',
      detail: ifaces.length
        ? ifaces.map(([id, i]) => `${i.label || id}: ${i.up ? 'поднят' : 'опущен'}`).join(' · ')
        : 'интерфейсы не перечислены',
    })
  }

  // Тесты приходят по туннелям: { tunnels: { <id>: { <slug>: {status, reason} } } }.
  // Экран группирует их наоборот -- по проверке, потому что оператор ищет
  // "что сломалось", а не "что там у awg12".
  const bySlug = new Map()
  for (const [tunnelID, sections] of Object.entries(report.tunnels ?? {})) {
    for (const [slug, body] of Object.entries(sections ?? {})) {
      if (!bySlug.has(slug)) bySlug.set(slug, [])
      bySlug.get(slug).push({
        tunnelID,
        status: body?.status ?? 'unknown',
        reason: body?.reason ?? '',
      })
    }
  }
  const slugs = [...bySlug.keys()].sort()
  for (const slug of slugs) {
    const rows = bySlug.get(slug).sort((a, b) => a.tunnelID.localeCompare(b.tunnelID))
    const tone = aggregateTone(rows.map((r) => r.status))
    const bad = rows.filter((r) => r.status !== 'ok')
    cards.push({
      key: `test:${slug}`,
      title: testLabel(slug),
      verdict: bad.length === 0 ? 'всё в норме' : `проблема на ${bad.length} из ${rows.length}`,
      tone,
      detail: bad.length
        ? bad.map((r) => `${r.tunnelID}: ${r.reason || r.status}`).join(' · ')
        : rows.map((r) => r.tunnelID).join(' · '),
    })
  }

  return {
    parsed: true,
    raw,
    cards,
    generatedAt: report.generatedAt ?? null,
    durationMs: report.durationMs ?? null,
  }
}
