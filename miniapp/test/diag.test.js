import { describe, it, expect } from 'vitest'
import { parseDiag, checkRows, exitCompare } from '../src/diag.js'

// Форма ответа -- /api/diagnostics/result awg-manager, та же, что разбирает
// бэкенд в internal/backend/alerts/diag_report.go (version 1.0).
const REPORT = JSON.stringify({
  version: '1.0',
  generatedAt: '2026-08-18T09:00:00Z',
  durationMs: 2559,
  system: {
    appVersion: '2.16.4',
    keeneticOS: '4.1.7',
    uptime: '5 дней',
    totalMemoryMB: 256,
    kernelModule: { exists: true, loaded: true },
  },
  wan: {
    anyUp: true,
    interfaces: { ISP: { up: true, label: 'Провайдер' } },
  },
  tunnels: {
    awg12: { handshake: { status: 'ok' }, dns: { status: 'fail', reason: 'таймаут' } },
    awg10: { handshake: { status: 'ok' }, dns: { status: 'ok' } },
  },
})

describe('parseDiag', () => {
  it('вытаскивает время сбора и длительность', () => {
    const d = parseDiag(REPORT)
    expect(d.generatedAt).toBe('2026-08-18T09:00:00Z')
    expect(d.durationMs).toBe(2559)
  })

  it('система и WAN становятся карточками', () => {
    const cards = parseDiag(REPORT).cards
    expect(cards.find((c) => c.key === 'system').detail).toContain('2.16.4')
    expect(cards.find((c) => c.key === 'wan').tone).toBe('ok')
  })

  it('проверка, провалившаяся хоть на одном туннеле, красится тревожно', () => {
    const dns = parseDiag(REPORT).cards.find((c) => c.key === 'test:dns')
    expect(dns.tone).toBe('danger')
    expect(dns.detail).toContain('awg12')
  })

  it('проверка, прошедшая везде, зелёная', () => {
    expect(parseDiag(REPORT).cards.find((c) => c.key === 'test:handshake').tone).toBe('ok')
  })

  it('порядок карточек стабилен между разборами', () => {
    const a = parseDiag(REPORT).cards.map((c) => c.key)
    const b = parseDiag(REPORT).cards.map((c) => c.key)
    expect(a).toEqual(b)
  })

  it('нераспознанный ответ не выдумывает карточек, но сохраняет сырой текст', () => {
    const d = parseDiag('не json')
    expect(d.cards).toEqual([])
    expect(d.raw).toBe('не json')
    expect(d.parsed).toBe(false)
  })

  it('пустой ответ -- пустой разбор, а не падение', () => {
    expect(parseDiag('').cards).toEqual([])
    expect(parseDiag(null).cards).toEqual([])
  })
})

// --- Строки «что спросили и что ответили» ---------------------------------
//
// Экран диагностики отвечает не «какая проверка моргнула», а «что из этого
// следует для человека»: фраза о последствии, под ней код мелким, справа --
// ответ и измерение. Измерения приезжают белым списком фактов проверки
// (miniapp_check_facts.go).
describe('checkRows', () => {
  const CHECKS = [
    { check_name: 'dns', status: 'ok', ts: '2026-08-20T09:00:00Z', facts: { resolvers: 3, resolvers_failed: 1, rkn_probed: 2, rkn_suspect: 0 } },
    { check_name: 'hydraroute', status: 'ok', ts: '2026-08-20T09:00:00Z', facts: { routes_hr_neo: 26, routes_ndms: 2, routes_static: 0, active_backend: 'hydraroute' } },
    { check_name: 'awg_manager', status: 'ok', ts: '2026-08-20T09:00:00Z', facts: { version: '2.17.2', firmware: '4.3.7' } },
    { check_name: 'external_reach', status: 'fail', ts: '2026-08-20T09:00:00Z', facts: { targets_total: 3, targets_failed: 2 } },
  ]
  const ROUTER = { status: 'online', last_seen_age_sec: 42 }
  const TUNNELS = [
    { tunnel_id: 'awg12', status: 'ok' },
    { tunnel_id: 'awg7', status: 'fail' },
  ]

  const rowsByKey = (rows) => Object.fromEntries(rows.map((r) => [r.key, r]))

  it('каждая строка несёт ответ и измерение', () => {
    const rows = checkRows({ checks: CHECKS, tunnels: TUNNELS, router: ROUTER })
    const byKey = rowsByKey(rows)
    expect(byKey.dns.answer).toBe('да')
    expect(byKey.dns.value).toBe('2 из 3 резолверов')
    expect(byKey.hydraroute.value).toBe('26 правил HR Neo')
    expect(byKey.awg_manager.value).toBe('2.17.2')
    expect(byKey.external_reach.answer).toBe('нет')
    expect(byKey.external_reach.value).toBe('1 из 3 отвечает')
    expect(byKey.external_reach.tone).toBe('danger')
  })

  // Порядок строк -- порядок вопросов, а не алфавит имён проверок.
  it('строки идут в фиксированном порядке, последней — отчёт о себе', () => {
    const rows = checkRows({ checks: CHECKS, tunnels: TUNNELS, router: ROUTER })
    expect(rows.map((r) => r.key)).toEqual([
      'dns', 'external_reach', 'hydraroute', 'awg_manager', 'tunnels', 'agent_heartbeat',
    ])
  })

  // Живость туннелей приезжает своей проекцией, а не проверкой: строка
  // считает по ней.
  it('строка туннелей считает по самим туннелям', () => {
    const byKey = rowsByKey(checkRows({ checks: CHECKS, tunnels: TUNNELS, router: ROUTER }))
    expect(byKey.tunnels.value).toBe('1 из 2 на связи')
    expect(byKey.tunnels.answer).toBe('нет')
  })

  // Молчащий роутер -- это не «нет», а «мы не знаем»: последний отчёт был
  // час назад, и всё остальное на экране — данные на тот момент.
  it('молчащий роутер отвечает «не знаем», а не «нет»', () => {
    const rows = checkRows({
      checks: CHECKS,
      tunnels: TUNNELS,
      router: { status: 'offline', last_seen_age_sec: 3600 },
    })
    const byKey = rowsByKey(rows)
    expect(byKey.agent_heartbeat.answer).toBe('нет')
    expect(byKey.agent_heartbeat.value).toContain('1 ч')
    expect(byKey.dns.answer).toBe('не знаем')
    expect(byKey.dns.tone).toBe('muted')
  })

  // Проверка без фактов -- старый агент. Измерение тогда одно честное: когда
  // мерили.
  it('без фактов строка показывает время замера', () => {
    const rows = checkRows({
      checks: [{ check_name: 'dns', status: 'ok', ts: new Date(Date.now() - 90_000).toISOString() }],
      tunnels: [],
      router: ROUTER,
    })
    expect(rowsByKey(rows).dns.value).toBe('измерено 1 мин назад')
  })

  // sing-box отменяет вопрос про обход блокировок: механизм другой, и «нет»
  // тут было бы враньём.
  it('при sing-box обход блокировок не спрашивается, а объясняется', () => {
    const rows = checkRows({
      checks: [{ check_name: 'hydraroute', status: 'ok', ts: '2026-08-20T09:00:00Z', facts: { singbox_router_active: true } }],
      tunnels: [],
      router: ROUTER,
    })
    const hr = rowsByKey(rows).hydraroute
    expect(hr.answer).toBe('не нужен')
    expect(hr.value).toContain('sing-box')
  })

  // Подмена DNS от РКН: проверка формально ok, но ответ на вопрос «сайты
  // открываются по имени» -- нет.
  it('подмена ответов резолверами не выдаётся за успех', () => {
    const rows = checkRows({
      checks: [{ check_name: 'dns', status: 'ok', ts: '2026-08-20T09:00:00Z', facts: { resolvers: 2, resolvers_failed: 0, rkn_probed: 2, rkn_suspect: 2 } }],
      tunnels: [],
      router: ROUTER,
    })
    const dns = rowsByKey(rows).dns
    expect(dns.tone).toBe('warn')
    expect(dns.value).toContain('подмен')
  })
})

// --- Два адреса выхода ----------------------------------------------------
describe('exitCompare', () => {
  const DIRECT = '🇷🇺 Напрямую (через системный маршрут):\nExit IP: 203.0.113.7\n\n✅ ya.ru'
  const VIA = '🌍 Через туннель (awg12):\nExit IP: 203.0.113.19\n\n✅ google.com'

  it('разные адреса — подмена работает', () => {
    const c = exitCompare(DIRECT, VIA)
    expect(c.direct).toBe('203.0.113.7')
    expect(c.viaTunnel).toBe('203.0.113.19')
    expect(c.works).toBe(true)
    expect(c.verdict).toContain('подмена работает')
  })

  // Один и тот же адрес с обеих сторон -- туннель не несёт трафик, и молчать
  // об этом нельзя: снаружи человека видно тем же адресом, что и без VPN.
  it('одинаковые адреса — подмены нет', () => {
    const c = exitCompare(DIRECT, DIRECT)
    expect(c.works).toBe(false)
    expect(c.verdict).toContain('тот же адрес')
  })

  it('пока ответа нет — честное «неизвестно», а не догадка', () => {
    const c = exitCompare(DIRECT, null)
    expect(c.viaTunnel).toBe('')
    expect(c.works).toBe(null)
    expect(c.verdict).toContain('только один')
  })

  // Ни одного замера -- это не «измерен один»: до нажатия кнопки на экране
  // не измерено ничего, и звать это половиной ответа неправда.
  it('до первого замера так и говорит', () => {
    const c = exitCompare(null, null)
    expect(c.works).toBe(null)
    expect(c.verdict).toBe('Адреса ещё не измерены — нажмите «Сравнить адреса».')
  })
})
