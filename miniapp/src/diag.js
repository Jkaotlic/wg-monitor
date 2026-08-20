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

// --- Строки «что спросили и что ответили» ---------------------------------
//
// Экран диагностики отвечает не «какая из проверок моргнула», а «что из этого
// следует»: фраза о последствии, под ней имя проверки мелким, справа -- ответ
// и измерение. Измерения приезжают белым списком фактов (facts, см.
// internal/backend/miniapp_check_facts.go); их отсутствие -- признак агента
// постарше, и тогда честное измерение остаётся одно: когда мерили.

import { humanAge, pluralRu } from './labels.js'

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
  // sing-box отменяет сам вопрос: маршрут выбирает он, а HR Neo в этот момент
  // ни при чём -- и «нет» здесь было бы враньём о поломке, которой нет.
  if (f?.singbox_router_active) {
    return { answer: 'не нужен', tone: 'muted', value: 'маршрутом занят sing-box' }
  }
  if (!f || f.routes_hr_neo == null) return { answer: check.status === 'ok' ? 'да' : 'нет', value: measuredAt(check.ts) }
  const n = f.routes_hr_neo
  return {
    answer: check.status === 'ok' ? 'да' : 'нет',
    value: `${n} ${pluralRu(n, 'правило', 'правила', 'правил')} HR Neo`,
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
  tunnels: 'Туннели на связи',
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
    rows.push({
      key,
      title: ROW_TITLES[key] ?? key,
      code: key,
      answer,
      tone: silent ? 'muted' : body.tone ?? ANSWER_TONE[body.answer] ?? 'muted',
      value: body.value,
    })
  }
  // Проверка, которой в порядке нет, всё равно приехала от роутера, и молчать
  // о ней нельзя -- показываем в конце тем именем, что дал агент.
  for (const c of checks ?? []) {
    if (ROW_ORDER.includes(c.check_name) || c.check_name.startsWith('tunnel_')) continue
    rows.push({
      key: c.check_name,
      title: c.check_name,
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
      verdict: 'Снаружи виден тот же адрес, что и без туннеля: подмены нет, трафик идёт мимо VPN.',
    }
  }
  return {
    direct,
    viaTunnel,
    works: true,
    verdict: 'Адреса разные — подмена работает: через туннель наружу виден адрес VPN-сервера.',
  }
}
