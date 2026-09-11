import { describe, it, expect } from 'vitest'
import { parseDiag, checkRows, exitCompare } from '../src/diag.js'

// Форма ответа -- /api/diagnostics/result awg-manager, проверенная на живом
// 2.18.2 (10.09.2026): проверки лежат плоским списком tests[], у проверок
// VPN-туннеля есть tunnelId и tunnelName. Прежний разбор ждал выдуманную
// форму tunnels → {id → {проверка}} и не находил ни одной -- экран
// показывал только «Роутер» и «Канал провайдера» с версией панели.
// Бэкенд разбирает ту же форму в internal/backend/alerts/diag_report.go.
const test = (name, status, extra = {}) => ({ name, description: name, status, detail: '', ...extra })
const REPORT = JSON.stringify({
  version: '1.0',
  generatedAt: '2026-08-18T09:00:00Z',
  durationMs: 16416,
  system: { appVersion: '2.18.2+r1', kernelModule: { exists: true, loaded: true } },
  wan: { anyUp: true, interfaces: { eth3: { up: true, label: 'Провайдер' } } },
  tests: [
    test('wan_connectivity', 'pass', { detail: 'default via 10.0.0.1' }),
    test('kernel_module', 'skip'),
    test('awg_handshake', 'pass', { tunnelId: 'awg10', tunnelName: 'Дача' }),
    test('awg_handshake', 'pass', { tunnelId: 'awg12', tunnelName: 'Работа' }),
    test('tunnel_connectivity', 'fail', { tunnelId: 'awg12', tunnelName: 'Работа', detail: 'timeout after 5s' }),
    test('tunnel_connectivity', 'pass', { tunnelId: 'awg10', tunnelName: 'Дача' }),
  ],
})
const ALL_GOOD = JSON.stringify({
  ...JSON.parse(REPORT),
  tests: JSON.parse(REPORT).tests.map((t) => (t.status === 'fail' ? { ...t, status: 'pass' } : t)),
})

describe('parseDiag', () => {
  it('вытаскивает время сбора и длительность', () => {
    const d = parseDiag(REPORT)
    expect(d.generatedAt).toBe('2026-08-18T09:00:00Z')
    expect(d.durationMs).toBe(16416)
  })

  // Сверху -- ответ «всё ли в порядке», как на остальных экранах. Версия
  // панели, модуль ядра и интерфейсы WAN владельцу не адресованы: они в
  // полном отчёте ниже.
  it('первая карточка -- вердикт, а не «Роутер» с версией панели', () => {
    const cards = parseDiag(ALL_GOOD).cards
    expect(cards[0].key).toBe('summary')
    expect(cards[0].tone).toBe('ok')
    expect(cards[0].verdict).toContain('всё в порядке')
    const text = cards.map((c) => `${c.title} ${c.verdict} ${c.detail}`).join('\n')
    expect(text).not.toMatch(/2\.18\.2|awg-manager|WAN|Канал провайдера/)
  })

  it('провал на одном VPN-туннеле: вердикт тревожный, карточка называет проверку и VPN-туннель по имени', () => {
    const cards = parseDiag(REPORT).cards
    expect(cards[0].tone).toBe('danger')
    expect(cards[0].verdict).toContain('нашлись проблемы')
    const tc = cards.find((c) => c.key === 'test:tunnel_connectivity')
    expect(tc.tone).toBe('danger')
    expect(tc.title).toBe('Интернет через VPN-туннель')
    expect(tc.detail).toContain('VPN-туннель «Работа»')
    expect(tc.detail).toContain('timeout after 5s')
    expect(tc.detail).not.toContain('awg12')
  })

  it('прошедшие проверки отдельными карточками не шумят', () => {
    expect(parseDiag(REPORT).cards.find((c) => c.key === 'test:awg_handshake')).toBeUndefined()
  })

  it('незагруженный модуль AmneziaWG -- совет перезагрузить роутер', () => {
    const d = parseDiag(JSON.stringify({ version: '1.0', system: { kernelModule: { exists: true, loaded: false } } }))
    const text = d.cards.map((c) => `${c.verdict} ${c.detail}`).join('\n')
    expect(text).toContain('перезагруз')
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
    expect(byKey.hydraroute.value).toBe('26 правил HydraRoute Neo')
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

  // Проверка вне порядка вопросов показывается в конце -- но человеческим
  // именем, если оно есть: «resolver_guard» владельцу ничего не говорит.
  // Незнакомое имя по-прежнему печатается как есть.
  it('проверка вне порядка подписана человеческим именем, если оно есть', () => {
    const rows = checkRows({
      checks: [
        { check_name: 'resolver_guard', status: 'fail', ts: '2026-09-11T10:00:00Z' },
        { check_name: 'wifi_band', status: 'ok', ts: '2026-09-11T10:00:00Z' },
      ],
      tunnels: [],
      router: ROUTER,
    })
    const byKey = rowsByKey(rows)
    expect(byKey.resolver_guard.title).toBe('Свой DNS-сервер')
    expect(byKey.resolver_guard.answer).toBe('нет')
    expect(byKey.wifi_band.title).toBe('wifi_band')
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
    expect(c.verdict).toContain('обход работает')
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

// Вкладка «Проверки» -- единственная, где машинное имя уместно: сюда идут
// разбираться. Но «нет» напротив вопроса не говорит, ЧЕМ это грозит, а
// человек рядом с инженером читает тот же экран.
describe('checkRows -- что означает провал', () => {
  it('упавшая проверка объясняет последствие человеческими словами', () => {
    const rows = checkRows({ checks: [{ check_name: 'dns', status: 'fail' }], tunnels: [] })
    const dns = rows.find((r) => r.key === 'dns')
    expect(dns.consequence).toBe('Не определяются адреса сайтов')
    expect(dns.code).toBe('dns')
  })

  // У работающей проверки последствия нет: дописывать «а если сломается,
  // будет плохо» к зелёной строке -- это шум, а не ответ.
  it('живая проверка последствия не несёт', () => {
    const rows = checkRows({ checks: [{ check_name: 'dns', status: 'ok' }], tunnels: [] })
    const dns = rows.find((r) => r.key === 'dns')
    expect(dns.consequence).toBe('')
  })

  // Молчащий роутер -- это «не знаем», а не «сломано»: пугать человека
  // последствием поломки, которой, может, и нет, нельзя.
  it('у молчащего роутера последствий не выдумывается', () => {
    const rows = checkRows({
      checks: [{ check_name: 'dns', status: 'fail' }],
      tunnels: [],
      router: { status: 'offline' },
    })
    const dns = rows.find((r) => r.key === 'dns')
    expect(dns.consequence).toBe('')
  })
})
