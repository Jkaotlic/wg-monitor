import { pluralRu } from './labels.js'
import { plainHyphens } from './text.js'

// Превью изменений маршрутизации: что именно уйдёт в туннель или исчезнет
// из него. Чистые функции -- вся работа с планом агента живёт здесь, экран
// только рисует.

// Каталог роутера -- 87 наборов; плоским списком по нему не попасть пальцем,
// поэтому экран показывает их категориями. Порядок категорий по имени, а не
// по частоте: частота -- догадка, имя -- факт.
export function templateGroups(templates) {
  const list = Array.isArray(templates) ? templates : []
  const byCategory = new Map()
  for (const t of list) {
    const key = (t.category ?? '').trim() || 'прочее'
    if (!byCategory.has(key)) byCategory.set(key, [])
    byCategory.get(key).push(t)
  }
  return [...byCategory.entries()]
    .sort((a, b) => a[0].localeCompare(b[0]))
    .map(([category, items]) => ({
      category,
      items: items.slice().sort((a, b) => (a.name ?? '').localeCompare(b.name ?? '')),
    }))
}

function targetLine(route) {
  const targets = (route?.targets ?? []).map((t) => t.value).filter(Boolean)
  return targets.length ? targets.join(', ') : ''
}

// Превью обязано называть цели ЦЕЛИКОМ: добавление и удаление правила меняют
// то, что уходит в туннель, и "и ещё 4" здесь -- скрытая часть последствия.
export function addPlanSummary(plan, { hrNeoRunning = false } = {}) {
  const route = plan?.route ?? {}
  const lines = []
  const targets = targetLine(route)
  if (targets) lines.push(targets)
  for (const o of plan?.overlaps ?? []) {
    lines.push(`${o.severity === 'block' ? 'Мешает' : 'Внимание'}: ${o.reason}`)
  }
  return {
    title: route.name || route.id || 'Новое правило',
    lines,
    // Те же данные, но разложенные по назначению: цели экран печатает
    // моноширинным (это код), а предупреждения -- обычной фразой.
    targets,
    notes: lines.slice(targets ? 1 : 0),
    canApply: Boolean(plan?.can_apply),
    // Хеш едет обратно с подтверждением: агент откажется применять план,
    // чьё превью устарело, и без хеша эта защита мертва.
    hash: plan?.hash ?? '',
    ...planEngine(route, hrNeoRunning),
  }
}

// Куда пойдёт правило, решает агент по живому роутеру; экран читает это из
// плана. Оператор 28.10.2026: работает HydraRoute Neo -- всё в него, нет --
// всё в стандартную маршрутизацию роутера, но с предупреждением: без Neo
// правило работает иначе (NDMS не знает гео-тегов и подсетей в DNS-правиле).
//
// Neo работает, а план всё равно мимо него -- это старый агент (до выбора
// движка на стороне агента): сказать «Neo не работает» было бы неправдой.
function planEngine(route, hrNeoRunning) {
  if (String(route?.backend ?? '').toLowerCase() === 'hydraroute') {
    return { engine: 'Через HydraRoute Neo', engineWarning: '' }
  }
  if (hrNeoRunning) {
    return {
      engine: 'Через стандартную маршрутизацию роутера',
      engineWarning:
        'HydraRoute Neo на роутере работает, но агент этого роутера старый и ведёт правило в стандартную маршрутизацию — обновите агента, и новые правила пойдут в HydraRoute Neo.',
    }
  }
  return {
    engine: 'Через стандартную маршрутизацию роутера',
    engineWarning:
      'HydraRoute Neo — движок умной раздельной маршрутизации — на этом роутере не работает, поэтому правило уйдёт в стандартную маршрутизацию роутера.',
  }
}

// План удаления несёт warnings, а не overlaps -- поле другое, смысл тот же:
// назвать последствие до нажатия, а не после.
export function deletePlanSummary(plan) {
  const route = plan?.route ?? {}
  const lines = []
  const targets = targetLine(route)
  if (targets) lines.push(targets)
  for (const w of plan?.warnings ?? []) lines.push(`Внимание: ${w.reason}`)
  return {
    title: route.name || route.id || 'Правило',
    lines,
    targets,
    notes: lines.slice(targets ? 1 : 0),
    canApply: Boolean(plan?.can_apply),
    hash: plan?.hash ?? '',
  }
}

// Ручной ввод. Правило на роутере -- либо про имена сайтов (DNS), либо про
// адреса сетей (static); агент отказывает смешанному первым же условием
// (buildRouteAddPlan, route_add_delete.go), и узнавать об этом после похода
// на роутер незачем. Тип не спрашиваем: он читается из того, что человек
// написал, и вопрос "DNS или static" -- вопрос про механизм, а не про
// последствие.
export function parseManualTargets(text) {
  // Цель, скопированная из текста приложения, может нести U+2011 -- роутер
  // такого дефиса в имени не примет.
  const raw = plainHyphens(text)
    .split(/[\s,;]+/)
    .map((v) => v.trim())
    .filter(Boolean)
  const targets = [...new Set(raw)]
  if (targets.length === 0) return { targets: [], kind: '', error: '' }
  const nets = targets.filter(looksLikeNet)
  if (nets.length === targets.length) return { targets, kind: 'static', error: '' }
  if (nets.length === 0) return { targets, kind: 'dns', error: '' }
  return {
    targets,
    kind: '',
    error: 'Имена сайтов и адреса сетей в одном правиле не уживаются — заведите два правила.',
  }
}

// Адрес сети узнаётся по цифрам и точкам/двоеточиям: разбирать IPv4/IPv6
// целиком здесь не нужно -- это делает агент, а экрану хватает отличить
// "10.0.0.0/8" от "openai.com", чтобы не смешать их в одном правиле.
function looksLikeNet(value) {
  const bare = value.split('/')[0]
  return /^[0-9.]+$/.test(bare) || /^[0-9a-f:]+$/i.test(bare) && bare.includes(':')
}

// Что уйдёт в правило из набора каталога. Гео-теги (geosite:OPENAI)
// разворачивает только HydraRoute Neo, поэтому без него набор теряет их молча --
// а набор, кроме тегов ничего не содержащий, применить нельзя вовсе, и
// сказать об этом обязан экран: агент ответит на это ошибкой уже с роутера.
//
// Движок правила -- свойство роутера, а не набора: работает HR Neo -- правило
// идёт в него. Флаг раньше зависел ещё и от гео-тегов набора, а с awg-manager
// 2.17.2 их в наборах нет -- и каталог молча заводил всё в NDMS (жалоба
// оператора 27.10.2026).
export function templateChoice(template, { hrNeoRunning = false } = {}) {
  const dns = (template?.dns ?? []).filter(Boolean)
  const hr = (template?.hr_neo ?? []).filter(Boolean)
  const useHRNeo = hrNeoRunning
  const parts = []
  if (dns.length > 0) parts.push(`${dns.length} ${pluralRu(dns.length, 'домен', 'домена', 'доменов')}`)
  if (useHRNeo && hr.length > 0) parts.push(`${hr.length} ${pluralRu(hr.length, 'гео-тег', 'гео-тега', 'гео-тегов')}`)
  const canApply = dns.length > 0 || (useHRNeo && hr.length > 0)
  return {
    id: template?.id ?? '',
    name: template?.name || template?.id || 'Набор',
    args: { template_id: template?.id ?? '', kind: 'dns', use_hr_neo: useHRNeo },
    summary: parts.join(' и '),
    canApply,
    reason: canApply
      ? ''
      : 'Набор состоит из гео-тегов, а их разворачивает только HydraRoute Neo — движок умной раздельной маршрутизации, а на этом роутере он не работает.',
  }
}

// Наборы, которые роутер описывает только правилами sing-box или ссылкой на
// подписку, агент в каталог не отдаёт: правилом DNS/HydraRoute Neo их не выразить, и
// кнопка под ними обещала бы несуществующее действие. Но их число он
// присылает, и экран называет его словами -- каталог из 75 наборов там, где
// роутер знает 87, иначе выглядит полным.
export function skippedNote(skipped) {
  const n = Number(skipped) || 0
  if (n <= 0) return ''
  return `Ещё ${n} ${pluralRu(n, 'набор', 'набора', 'наборов')} роутер описывает правилами sing-box — их приложение применить не может.`
}

// Аргументы плана для ручного ввода. Сайты при работающем HR Neo идут в него
// по той же причине, что и наборы каталога; сети -- статический маршрут
// роутера, к HR Neo отношения не имеют.
export function manualPlanArgs({ parsed, tunnelID, name, hrNeoRunning = false }) {
  const ruleName = (name ?? '').trim() || parsed.targets[0]
  return {
    kind: parsed.kind,
    tunnel_id: tunnelID,
    targets: parsed.targets,
    name: ruleName,
    use_hr_neo: Boolean(hrNeoRunning) && parsed.kind === 'dns',
  }
}

// Цель правила из гео-тега -- ровно так её пишет роутер: geosite:YOUTUBE.
export function geoTarget(tag) {
  return `${tag.kind}:${tag.name}`
}

// Поиск по гео-тегам роутера (их ~1800). Пустой запрос -- пустая выдача:
// листать такой список пальцем некому. Порядок: точное имя, начало имени,
// вхождение; внутри -- сначала сайты (geosite), потом сети (geoip), затем по
// имени. Запрос "geoip:you" сужает поиск до типа.
export function geoSearch(tags, query, { limit = 40 } = {}) {
  const list = Array.isArray(tags) ? tags : []
  let q = plainHyphens(query ?? '').trim().toUpperCase()
  let kind = ''
  const m = /^(GEOSITE|GEOIP):(.*)$/.exec(q)
  if (m) {
    kind = m[1].toLowerCase()
    q = m[2].trim()
  }
  if (!q) return []
  const scored = []
  for (const t of list) {
    const name = String(t?.name ?? '').toUpperCase()
    if (!name || (kind && t.kind !== kind)) continue
    const rank = name === q ? 0 : name.startsWith(q) ? 1 : name.includes(q) ? 2 : -1
    if (rank < 0) continue
    scored.push({ t, rank })
  }
  scored.sort(
    (a, b) =>
      a.rank - b.rank ||
      (a.t.kind === b.t.kind ? 0 : a.t.kind === 'geosite' ? -1 : 1) ||
      a.t.name.localeCompare(b.t.name),
  )
  return scored.slice(0, limit).map((x) => x.t)
}

// Имя правила по умолчанию -- имена выбранных тегов без повторов
// (geosite:YOUTUBE и geoip:YOUTUBE -- один сервис). Сервер пропускает имя
// до 64 символов, длиннее -- обрезается с многоточием.
export function geoRuleName(targets) {
  const names = [...new Set((targets ?? []).map((t) => String(t).split(':').pop()).filter(Boolean))]
  const joined = names.join(', ')
  return joined.length <= 64 ? joined : `${joined.slice(0, 63)}…`
}

// Подпись тега: что он ведёт (сайты или сети) и сколько за ним записей.
export function geoCountLabel(tag) {
  const n = Number(tag?.count) || 0
  const what = tag?.kind === 'geoip' ? 'сети' : 'сайты'
  return `${what} · ${n} ${pluralRu(n, 'запись', 'записи', 'записей')}`
}

// Кнопка «Перенести в HydraRoute Neo» -- только у статического маршрута и
// только при работающем Neo: переносить больше некуда (оператор 28.10.2026:
// «если включен гидра роут -- все туда»).
export function canMigrateRule(rule, snapshot) {
  return rule?.kind === 'static' && Boolean(snapshot?.hr_neo?.installed && snapshot?.hr_neo?.running)
}

// Превью переноса (wire.RouteMigratePlan): какие сети переезжают и куда.
// Сети названы целиком -- по той же причине, что и при добавлении.
export function migratePlanSummary(plan) {
  const route = plan?.route ?? {}
  const targets = targetLine(route)
  const notes = (plan?.overlaps ?? []).map((o) => `${o.severity === 'block' ? 'Мешает' : 'Внимание'}: ${o.reason}`)
  return {
    title: route.name || plan?.from?.name || 'Маршрут',
    targets,
    notes,
    body: `Статический маршрут станет правилом HydraRoute Neo с теми же сетями и тем же VPN-туннелем${targets ? `: ${targets}` : ''}. Сначала появится новое правило, потом исчезнет старое — трафик не прервётся.`,
    canApply: Boolean(plan?.can_apply),
    hash: plan?.hash ?? '',
  }
}
