import { pluralRu } from './labels.js'

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
export function addPlanSummary(plan) {
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
  const raw = String(text ?? '')
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
// разворачивает только HR Neo, поэтому без него набор теряет их молча --
// а набор, кроме тегов ничего не содержащий, применить нельзя вовсе, и
// сказать об этом обязан экран: агент ответит на это ошибкой уже с роутера.
export function templateChoice(template, { hrNeoRunning = false } = {}) {
  const dns = (template?.dns ?? []).filter(Boolean)
  const hr = (template?.hr_neo ?? []).filter(Boolean)
  const useHRNeo = hrNeoRunning && hr.length > 0
  const parts = []
  if (dns.length > 0) parts.push(`${dns.length} ${pluralRu(dns.length, 'домен', 'домена', 'доменов')}`)
  if (useHRNeo) parts.push(`${hr.length} ${pluralRu(hr.length, 'гео-тег', 'гео-тега', 'гео-тегов')}`)
  const canApply = dns.length > 0 || useHRNeo
  return {
    id: template?.id ?? '',
    name: template?.name || template?.id || 'Набор',
    args: { template_id: template?.id ?? '', kind: 'dns', use_hr_neo: useHRNeo },
    summary: parts.join(' и '),
    canApply,
    reason: canApply
      ? ''
      : 'Набор состоит из гео-тегов, а их разворачивает только HR Neo — на этом роутере он не работает.',
  }
}

// Наборы, которые роутер описывает только правилами sing-box или ссылкой на
// подписку, агент в каталог не отдаёт: правилом DNS/HR-Neo их не выразить, и
// кнопка под ними обещала бы несуществующее действие. Но их число он
// присылает, и экран называет его словами -- каталог из 75 наборов там, где
// роутер знает 87, иначе выглядит полным.
export function skippedNote(skipped) {
  const n = Number(skipped) || 0
  if (n <= 0) return ''
  return `Ещё ${n} ${pluralRu(n, 'набор', 'набора', 'наборов')} роутер описывает правилами sing-box — их приложение применить не может.`
}
