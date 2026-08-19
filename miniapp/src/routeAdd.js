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
    canApply: Boolean(plan?.can_apply),
    hash: plan?.hash ?? '',
  }
}
