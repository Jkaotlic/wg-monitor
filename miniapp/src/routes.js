// Чтение снимка маршрутизации (wire.RouteSnapshot, pkg/wire/routing.go).
//
// Привязка правил к туннелям читается из политик доступа роутера и раскладке
// ниже можно верить. Управления маршрутами здесь всё равно нет, и это
// сознательно: перенос правил между туннелями -- отдельная операция с
// подтверждением и откатом, она относится к фазе рабочего места оператора.
// Пока экран только показывает.

export function parseRouteSnapshot(output) {
  if (typeof output !== 'string' || output === '') return null
  try {
    const snap = JSON.parse(output)
    return snap && typeof snap === 'object' ? snap : null
  } catch {
    return null
  }
}

export function routingVerdict(snapshot) {
  const partial = Boolean(snapshot?.warnings?.length)

  // Порядок важен: sing-box выбирает маршрут для каждого адреса отдельно,
  // поэтому единого ответа "напрямую или через VPN" тут не существует, и
  // любой другой вердикт был бы враньём.
  if (snapshot?.singbox_router?.enabled) {
    return {
      mode: 'unknown',
      partial,
      title: 'Маршрут выбирает sing-box',
      detail:
        'Для каждого адреса отдельно. Правила HR-Neo и статические маршруты на этом роутере не действуют — картинка ниже показывает их, но не то, куда реально идёт трафик.',
    }
  }

  const tunnels = Array.isArray(snapshot?.tunnels) ? snapshot.tunnels : []
  const claiming = tunnels.filter((t) => t.default_route)

  if (claiming.length === 1) {
    const t = claiming[0]
    return {
      mode: 'vpn',
      partial,
      title: `Трафик идёт через «${t.name || t.id}»`,
      detail: 'Этот туннель несёт основной маршрут.',
    }
  }
  if (claiming.length > 1) {
    // Каждый туннель может лишь ЗАЯВЛЯТЬ, что он главный. Назвать первого --
    // угадать; экран говорит "неизвестно" и перечисляет спорящих.
    return {
      mode: 'unknown',
      partial,
      title: 'Главный туннель не определён',
      detail: `Основным настроены сразу несколько: ${claiming.map((t) => t.name || t.id).join(', ')}. Пока их больше одного, кто именно несёт трафик — по снимку не видно.`,
    }
  }
  if (tunnels.length > 0) {
    return {
      mode: 'direct',
      partial,
      title: 'Трафик идёт напрямую',
      detail: 'Ни один туннель не несёт основной маршрут — трафик уходит через провайдера.',
    }
  }
  return {
    mode: 'unknown',
    partial,
    title: 'Снимок маршрутизации пуст',
    detail: 'Роутер не сообщил ни одного туннеля. Соберите снимок заново.',
  }
}

// Читал ли агент политики роутера. policy_model -- факт, который агент знает
// о СЕБЕ, и вывести его из данных нельзя: у политики, чья цепочка целиком
// уходит мимо VPN, нет ни одного tunnel_id, и роутер с единственной такой
// политикой неотличим от агента, который политики читать не умеет вовсе.
// Флаг авторитетен; перебор политик -- запасной путь для снимков, собранных
// до его появления. Та же логика в панели бота: routeSnapshotHasPolicyIdentity.
function hasPolicyIdentity(snapshot) {
  if (snapshot?.policy_model) return true
  const policies = Array.isArray(snapshot?.policies) ? snapshot.policies : []
  return policies.some(
    (p) => p.active_tunnel_id || (p.interfaces ?? []).some((i) => i.tunnel_id),
  )
}

// Правила, привязанные политикой, лежат ТОЛЬКО в policies[]: в counts они не
// попадают, иначе панель бота, складывающая оба источника в общий итог,
// удвоила бы цифру. Поэтому раскладка "по туннелям" обязана свести их
// обратно сама -- к активному туннелю политики и ни к какому другому.
// Референс -- routePolicyDNSByTunnelID в internal/backend/tg/routes_panel.go.
function policyRulesByTunnel(snapshot) {
  const policies = Array.isArray(snapshot?.policies) ? snapshot.policies : []
  const out = new Map()
  const credit = (id, dns, hrNeo) => {
    const prev = out.get(id) ?? { dns: 0, hrNeo: 0 }
    out.set(id, { dns: prev.dns + dns, hrNeo: prev.hrNeo + hrNeo })
  }
  if (hasPolicyIdentity(snapshot)) {
    for (const p of policies) {
      const dns = p.dns ?? 0
      if (dns === 0 || !p.active_tunnel_id) continue
      credit(p.active_tunnel_id, dns, p.hr_neo ?? 0)
    }
    return out
  }
  // Снимок старого агента: active_tunnel_id в нём нет, и звенья цепочки
  // приходится сопоставлять с туннелями по iface и имени. Раскладка при
  // этом множит правила по всей цепочке -- неточно, но ровно так же, как в
  // панели бота, и лучше, чем потерять привязку целиком.
  const tunnels = Array.isArray(snapshot?.tunnels) ? snapshot.tunnels : []
  if (policies.length === 0 || tunnels.length === 0) return out
  const byBind = new Map()
  const byName = new Map()
  for (const t of tunnels) {
    const bind = (t.iface ?? '').trim()
    if (bind) byBind.set(bind.toLowerCase(), t.id)
    const name = (t.name ?? '').trim()
    if (name) byName.set(name.toLowerCase(), t.id)
  }
  for (const p of policies) {
    const dns = p.dns ?? 0
    if (dns === 0) continue
    const seen = new Set()
    for (const iface of p.interfaces ?? []) {
      const bind = (iface.bind ?? '').trim()
      const name = (iface.name ?? '').trim()
      const id = (bind && byBind.get(bind.toLowerCase())) || (name && byName.get(name.toLowerCase()))
      if (!id || seen.has(id)) continue
      seen.add(id)
      credit(id, dns, p.hr_neo ?? 0)
    }
  }
  return out
}

// Строка туннеля в раскладке: собственные правила плюс правила политики,
// которую он несёт прямо сейчас.
export function tunnelRows(snapshot) {
  const tunnels = Array.isArray(snapshot?.tunnels) ? snapshot.tunnels : []
  const policyRules = policyRulesByTunnel(snapshot)
  return tunnels.map((t) => {
    const counts = snapshot?.counts?.[t.id]
    const own = (counts?.dns ?? 0) + (counts?.static ?? 0)
    const viaPolicy = policyRules.get(t.id) ?? { dns: 0, hrNeo: 0 }
    return {
      id: t.id,
      name: t.name || t.id,
      defaultRoute: Boolean(t.default_route),
      total: own + viaPolicy.dns,
      policyRules: viaPolicy.dns,
      hrNeo: (counts?.hr_neo ?? 0) + viaPolicy.hrNeo,
    }
  })
}

// Цепочка -- это приоритет: первое доступное звено несёт трафик политики,
// остальные ждут своей очереди. Экран показывает её звеньями, а не строкой:
// оператору важно, КАКОЕ звено активно, а не вся строка целиком.
export function policyRows(snapshot) {
  const policies = Array.isArray(snapshot?.policies) ? snapshot.policies : []
  const identity = hasPolicyIdentity(snapshot)
  return policies.map((p) => {
    const interfaces = p.interfaces ?? []
    const chain = interfaces.map((i) => ({
      label: i.name || i.bind,
      bind: i.bind,
      role: i.role || 'unavailable',
      viaVPN: Boolean(i.via_vpn),
    }))
    const active = chain.find((i) => i.role === 'active')
    // Гейт ровно тот же, что у панели бота (routePolicyEgressNote): обе
    // пометки -- и "мимо VPN", и "нет доступного интерфейса" -- держатся на
    // том, что роли и via_vpn расставил агент, читающий политики. Старая
    // ветка снимка тоже присылает role на каждом звене, но по другой логике,
    // так что и "нет активного" на её данных было бы догадкой.
    let egress = ''
    if (chain.length > 0 && identity) {
      if (!active) egress = 'нет доступного интерфейса'
      else if (!active.viaVPN) egress = 'мимо VPN'
    }
    return {
      name: p.name,
      chain,
      rules: (p.dns ?? 0) + (p.static ?? 0),
      hrNeo: p.hr_neo ?? 0,
      viaVPN: Boolean(p.via_vpn),
      egress,
    }
  })
}

// Правила группируются по привязке: оператор спрашивает "что уходит в этот
// туннель", а не "какое правило под каким номером".
export function rulesByBind(snapshot) {
  const rules = Array.isArray(snapshot?.rules) ? snapshot.rules : []
  const byBind = new Map()
  for (const r of rules) {
    const key = r.bind || 'без привязки'
    if (!byBind.has(key)) byBind.set(key, [])
    byBind.get(key).push(r)
  }
  return [...byBind.entries()]
    .sort((a, b) => a[0].localeCompare(b[0]))
    .map(([bind, items]) => ({ bind, rules: items }))
}
