// Чтение снимка маршрутизации (wire.RouteSnapshot, pkg/wire/routing.go).
//
// Экран только показывает; управлять маршрутами отсюда нельзя до тех пор,
// пока система не научится верно читать привязку правил к политикам (раздел 3
// спеки 2026-08-02). Переключатель поверх неверной раскладки управлял бы
// выдумкой, а не роутером.

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

// Цепочка -- это приоритет: первое доступное звено несёт трафик политики,
// остальные ждут своей очереди. Экран показывает её звеньями, а не строкой:
// оператору важно, КАКОЕ звено активно, а не вся строка целиком.
export function policyRows(snapshot) {
  const policies = Array.isArray(snapshot?.policies) ? snapshot.policies : []
  // Старый агент не сопоставляет интерфейсы политики с туннелями. У его
  // снимка нет ни tunnel_id, ни active_tunnel_id -- доверять отсутствующему
  // via_vpn и заявлять "мимо VPN" значило бы гадать, поэтому метки просто нет.
  // Это снимко-широкий флаг: если хоть одна политика знает свои туннели,
  // значит снимок собрал новый агент, и остальным политикам можно верить так же.
  const tunnelKnown = policies.some(
    (p) => p.active_tunnel_id || (p.interfaces ?? []).some((i) => i.tunnel_id),
  )
  return policies.map((p) => {
    const interfaces = p.interfaces ?? []
    // А вот "нет активного звена" -- факт, а не догадка, как только сама
    // политика явно проставляет role хотя бы одному интерфейсу: старый агент
    // role вообще не присылает, поэтому это проверяется отдельно от туннелей.
    const roleKnown = interfaces.some((i) => i.role !== undefined)
    const chain = interfaces.map((i) => ({
      label: i.name || i.bind,
      bind: i.bind,
      role: i.role || 'unavailable',
      viaVPN: Boolean(i.via_vpn),
    }))
    const active = chain.find((i) => i.role === 'active')
    let egress = ''
    if (chain.length > 0) {
      if (roleKnown && !active) egress = 'нет доступного интерфейса'
      else if (tunnelKnown && active && !active.viaVPN) egress = 'мимо VPN'
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
