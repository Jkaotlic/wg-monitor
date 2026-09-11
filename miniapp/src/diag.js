// Разбор отчёта awg-manager /api/diagnostics/result в карточки экрана.
//
// Форма проверена на живом awg-manager 2.18.2 (10.09.2026): проверки лежат
// плоским списком tests[], у проверок VPN-туннеля есть tunnelId и tunnelName.
// Прежний разбор ждал выдуманную форму tunnels → {id → {проверка}} и не
// находил ни одной проверки -- экран показывал только «Роутер» и «Канал
// провайдера» с версией панели. Разбор тот же, что у бэкенда в
// internal/backend/alerts/diag_report.go: сверху вердикт «всё ли в порядке»,
// ниже -- только то, что не так, по VPN-туннелям с их именами. Нераспознанный
// ответ не выдумываем, а показываем сырым.

// Проверки awg-manager словами владельца -- те же, что у бэкенда.
const TEST_LABELS = {
  wan_connectivity: 'Интернет у провайдера',
  ndms_health: 'Система роутера отвечает',
  kernel_module: 'Модуль AmneziaWG',
  clock_skew: 'Часы роутера',
  direct_connectivity: 'Интернет напрямую',
  singbox_runtime: 'Прокси-движок',
  singbox_tunnel_connectivity: 'Связь через прокси-движок',
  dns_resolve: 'Адрес сервера VPN-туннеля находится',
  endpoint_reachable: 'Сервер VPN-туннеля отвечает',
  endpoint_route_check: 'Путь до сервера VPN-туннеля',
  awg_handshake: 'Обмен ключами свежий',
  tunnel_connectivity: 'Интернет через VPN-туннель',
  firewall_rules: 'Правила пропуска трафика',
  config_parse: 'Настройки VPN-туннеля читаются',
  interface_state_consistency: 'Состояние VPN-туннеля согласовано',
  mtu_check: 'Размер пакета (MTU)',
  proxy_health: 'Прокси-модуль AmneziaWG',
  pingcheck_health: 'Проверка связи',
  rp_filter: 'Фильтр обратного пути',
  route_leak_check: 'Лишние маршруты',
  dns_leak_check: 'Запросы имён не утекают мимо VPN-туннеля',
  restart_cycle: 'Перезапуск VPN-туннеля',
}

function testLabel(name, description) {
  return TEST_LABELS[name] ?? (description || name)
}

// awg-manager пишет pass, экран говорит ok.
function normStatus(s) {
  const v = String(s ?? '').trim().toLowerCase()
  if (v === 'pass' || v === 'ok' || v === 'success') return 'ok'
  if (v === 'fail' || v === 'failed' || v === 'error') return 'fail'
  if (v === 'warn' || v === 'warning') return 'warn'
  if (v === 'skip' || v === 'skipped') return 'skip'
  return v
}

// Имя VPN-туннеля, каким его назвал владелец; без имени -- идентификатор.
function tunnelLabel(t) {
  return String(t.tunnelName ?? '').trim() || String(t.tunnelId ?? '').trim()
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
  const tests = Array.isArray(report.tests) ? report.tests.filter((t) => t && t.name) : []

  if (tests.length) {
    let passed = 0
    let failed = 0
    let skipped = 0
    for (const t of tests) {
      const s = normStatus(t.status)
      if (s === 'fail') failed++
      else if (s === 'skip') skipped++
      else passed++
    }
    const checked = passed + failed
    cards.push({
      key: 'summary',
      title: 'Итог',
      verdict: failed ? `нашлись проблемы: ${failed} из ${checked}` : 'всё в порядке',
      tone: failed ? 'danger' : 'ok',
      detail:
        `проверено ${checked} ${pluralRu(checked, 'пункт', 'пункта', 'пунктов')}` +
        (skipped ? ` · пропущено ${skipped} — на этом роутере не нужны` : ''),
    })
  }

  // Незагруженный модуль -- единственное из системной части, с чем владелец
  // может что-то сделать сам.
  const km = report.system?.kernelModule
  if (km && km.exists && km.loaded === false) {
    cards.push({
      key: 'kernel',
      title: 'Модуль AmneziaWG',
      verdict: 'не загружен',
      tone: 'danger',
      detail: 'Может понадобиться перезагрузка роутера.',
    })
  }

  // Только то, что не так, по проверке: человек ищет «что сломалось», а
  // прошедшие проверки уже сосчитаны в итоге.
  const bad = new Map()
  for (const t of tests) {
    const s = normStatus(t.status)
    if (s !== 'fail' && s !== 'warn') continue
    if (!bad.has(t.name)) bad.set(t.name, { label: testLabel(t.name, t.description), rows: [], worst: s })
    const g = bad.get(t.name)
    if (s === 'fail') g.worst = 'fail'
    const where = tunnelLabel(t)
    const what = String(t.detail ?? '').trim()
    g.rows.push(where ? `VPN-туннель «${where}»${what ? `: ${what}` : ''}` : what)
  }
  for (const [name, g] of bad) {
    cards.push({
      key: `test:${name}`,
      title: g.label,
      verdict: g.worst === 'fail' ? 'сбой' : 'есть замечание',
      tone: g.worst === 'fail' ? 'danger' : 'warn',
      detail: g.rows.filter(Boolean).join(' · '),
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

// --- Строки «что спросили и что ответили» ---------------------------------
//
// Экран диагностики отвечает не «какая из проверок моргнула», а «что из этого
// следует»: фраза о последствии, под ней имя проверки мелким, справа -- ответ
// и измерение. Измерения приезжают белым списком фактов (facts, см.
// internal/backend/miniapp_check_facts.go); их отсутствие -- признак агента
// постарше, и тогда честное измерение остаётся одно: когда мерили.

import { humanAge, pluralRu, incidentCopy, checkLabel } from './labels.js'

// Порядок вопросов, а не алфавит имён: сначала то, что человек замечает
// первым (сайты не открываются), потом механизмы, и только в конце -- сам
// роутер, отчитывающийся о себе.
const ROW_ORDER = ['dns', 'external_reach', 'hydraroute', 'awg_manager', 'tunnels', 'agent_heartbeat']

const ANSWER_TONE = { да: 'ok', нет: 'danger', 'не знаем': 'muted' }

function measuredAt(ts) {
  if (!ts) return 'измерено — когда, роутер не сказал'
  const sec = Math.max(0, Math.round((Date.now() - new Date(ts).getTime()) / 1000))
  return `измерено ${humanAge(sec)} назад`
}

function dnsRow(check) {
  const f = check.facts
  if (!f || f.resolvers == null) return { answer: check.status === 'ok' ? 'да' : 'нет', value: measuredAt(check.ts) }
  const total = f.resolvers
  const alive = total - (f.resolvers_failed ?? 0)
  // Подмена ответов важнее счётчика живых резолверов: резолвер отвечает, но
  // отвечает не то, и «2 из 2» тут читалось бы как «всё хорошо».
  if (f.rkn_suspect > 0) {
    const n = f.rkn_suspect
    return {
      answer: 'нет',
      tone: 'warn',
      value: `${n} ${pluralRu(n, 'подмена', 'подмены', 'подмен')} ответа`,
    }
  }
  return {
    answer: check.status === 'ok' ? 'да' : 'нет',
    value: `${alive} из ${total} ${pluralRu(total, 'резолвера', 'резолверов', 'резолверов')}`,
  }
}

function reachRow(check) {
  const f = check.facts
  if (!f || f.targets_total == null) return { answer: check.status === 'ok' ? 'да' : 'нет', value: measuredAt(check.ts) }
  const total = f.targets_total
  const alive = total - (f.targets_failed ?? 0)
  return {
    answer: check.status === 'ok' ? 'да' : 'нет',
    value: `${alive} из ${total} ${pluralRu(alive, 'отвечает', 'отвечают', 'отвечают')}`,
  }
}

function hydraRow(check) {
  const f = check.facts
  // sing-box отменяет сам вопрос: маршрут выбирает он, а HydraRoute Neo в этот момент
  // ни при чём -- и «нет» здесь было бы враньём о поломке, которой нет.
  if (f?.singbox_router_active) {
    return { answer: 'не нужен', tone: 'muted', value: 'маршрутом занят sing-box' }
  }
  if (!f || f.routes_hr_neo == null) return { answer: check.status === 'ok' ? 'да' : 'нет', value: measuredAt(check.ts) }
  const n = f.routes_hr_neo
  return {
    answer: check.status === 'ok' ? 'да' : 'нет',
    value: `${n} ${pluralRu(n, 'правило', 'правила', 'правил')} HydraRoute Neo`,
  }
}

function awgmRow(check) {
  const version = check.facts?.version
  return {
    answer: check.status === 'ok' ? 'да' : 'нет',
    value: version || measuredAt(check.ts),
  }
}

// Живость туннелей считается по самим туннелям, а не по проверке: проекция
// tunnels[] и есть ответ роутера про каждый из них, а сводная проверка знает
// только «всё хорошо / не всё».
function tunnelsRow(check, tunnels) {
  const list = Array.isArray(tunnels) ? tunnels : []
  if (list.length === 0) {
    return { answer: check?.status === 'ok' ? 'да' : 'не знаем', value: measuredAt(check?.ts) }
  }
  const alive = list.filter((t) => t.status === 'ok').length
  return {
    answer: alive === list.length ? 'да' : 'нет',
    value: `${alive} из ${list.length} на связи`,
  }
}

const ROW_TITLES = {
  dns: 'Сайты открываются по имени',
  external_reach: 'Сайты снаружи отвечают',
  hydraroute: 'Обход блокировок работает',
  awg_manager: 'Панель роутера отвечает',
  tunnels: 'VPN-туннели на связи',
  agent_heartbeat: 'Роутер отчитался о себе',
}

export function checkRows({ checks = [], tunnels = [], router = null } = {}) {
  const byName = new Map((checks ?? []).map((c) => [c.check_name, c]))
  // Молчащий роутер делает устаревшими ВСЕ показания: то, что показано ниже,
  // измерено до того, как он замолчал, и выдавать это за ответ «сейчас»
  // нельзя. Поэтому «не знаем» -- не про поломку проверки, а про давность.
  const silent = router?.status === 'offline' || router?.status === 'sleeping'
  const rows = []
  for (const key of ROW_ORDER) {
    if (key === 'agent_heartbeat') {
      const age = router?.last_seen_age_sec
      rows.push({
        key,
        title: ROW_TITLES[key],
        code: 'agent_heartbeat',
        answer: silent ? 'нет' : 'да',
        tone: silent ? 'danger' : 'ok',
        value: age != null ? `${humanAge(age)} назад` : 'ни разу не отчитывался',
      })
      continue
    }
    const check = byName.get(key)
    // Строка туннелей держится на проекции tunnels[], а не на сводной
    // проверке: роутер, не приславший её, всё равно прислал сами туннели.
    if (!check && !(key === 'tunnels' && tunnels?.length)) continue
    let body
    if (key === 'dns') body = dnsRow(check)
    else if (key === 'external_reach') body = reachRow(check)
    else if (key === 'hydraroute') body = hydraRow(check)
    else if (key === 'awg_manager') body = awgmRow(check)
    else body = tunnelsRow(check ?? null, tunnels)
    const answer = silent ? 'не знаем' : body.answer
    const tone = silent ? 'muted' : body.tone ?? ANSWER_TONE[body.answer] ?? 'muted'
    rows.push({
      key,
      title: ROW_TITLES[key] ?? key,
      code: key,
      answer,
      tone,
      // Последствие пишется только у сломанного и только когда роутер на
      // связи. «Нет» напротив вопроса не говорит, чем это грозит, а на
      // молчащем роутере поломки может и не быть -- пугать ею нельзя.
      consequence: tone === 'danger' ? incidentCopy(key).what : '',
      value: body.value,
    })
  }
  // Проверка, которой в порядке нет, всё равно приехала от роутера, и молчать
  // о ней нельзя -- показываем в конце человеческим именем, если оно есть
  // (resolver_guard -- «Свой DNS-сервер»), иначе тем, что дал агент.
  for (const c of checks ?? []) {
    if (ROW_ORDER.includes(c.check_name) || c.check_name.startsWith('tunnel_')) continue
    rows.push({
      key: c.check_name,
      title: checkLabel(c.check_name),
      code: c.check_name,
      answer: silent ? 'не знаем' : c.status === 'ok' ? 'да' : 'нет',
      tone: silent ? 'muted' : c.status === 'ok' ? 'ok' : 'danger',
      value: measuredAt(c.ts),
    })
  }
  return rows
}

// --- Два адреса выхода ----------------------------------------------------
//
// check_direct и check_via_tunnel отвечают текстом агента, а не JSON: адрес
// вынимается из строки "Exit IP: ...". Сравнение двух адресов -- это и есть
// ответ на вопрос "подмена работает?": один и тот же адрес с обеих сторон
// значит, что снаружи человека видно ровно так же, как без VPN.
const EXIT_IP = /Exit IP:\s*([0-9a-f.:]+)/i

export function exitAddress(output) {
  if (typeof output !== 'string') return ''
  const m = EXIT_IP.exec(output)
  return m ? m[1] : ''
}

export function exitCompare(directOutput, tunnelOutput) {
  const direct = exitAddress(directOutput)
  const viaTunnel = exitAddress(tunnelOutput)
  if (!direct && !viaTunnel) {
    return {
      direct,
      viaTunnel,
      works: null,
      verdict: 'Адреса ещё не измерены — нажмите «Сравнить адреса».',
    }
  }
  if (!direct || !viaTunnel) {
    return {
      direct,
      viaTunnel,
      works: null,
      verdict: 'Пока измерен только один адрес — сравнивать не с чем.',
    }
  }
  if (direct === viaTunnel) {
    return {
      direct,
      viaTunnel,
      works: false,
      verdict: 'Снаружи виден тот же адрес, что и без VPN-туннеля: подмены нет, трафик идёт мимо VPN.',
    }
  }
  return {
    direct,
    viaTunnel,
    works: true,
    verdict: 'Адреса разные — обход работает: через VPN-туннель наружу виден адрес VPN-сервера.',
  }
}
