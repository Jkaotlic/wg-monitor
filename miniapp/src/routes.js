// Чтение снимка маршрутизации (wire.RouteSnapshot, pkg/wire/routing.go).
//
// Привязка правил к туннелям читается из политик доступа роутера и раскладке
// ниже можно верить. Управления маршрутами здесь всё равно нет, и это
// сознательно: перенос правил между туннелями -- отдельная операция с
// подтверждением и откатом, она относится к фазе рабочего места оператора.
// Пока экран только показывает.

import { rulesCount } from './labels.js'

// Жив ли туннель прямо сейчас. Судить об этом можно ТОЛЬКО по status:
// поле enabled в снимке отвечает на другой вопрос -- годится ли интерфейс
// как мишень для переноса правил, и агент дожимает его до true из каталога
// маршрутизации NDMS (route_status.go:190). Живой роутер отдаёт выключенный
// туннель как enabled:false/status:"disabled", а в снимке он приезжает как
// enabled:true -- и экран, поверивший enabled, назвал бы выключенный туннель
// работающим. Словарь состояний тот же, что у агента (routingStatusEnabled,
// route_targets.go:56); вторая формула для той же величины разошлась бы с
// первой. Пустой status -- честное "неизвестно": так отвечает снимок агента,
// который это поле ещё не присылал, и выдавать его за "выключен" нельзя.
export function tunnelLive(t) {
  const status = (t?.status ?? '').trim().toLowerCase()
  if (status === '') return 'unknown'
  return ['running', 'up', 'started', 'active'].includes(status) ? 'up' : 'down'
}

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
  // default_route -- это НАСТРОЙКА туннеля, а не факт о трафике: выключенный
  // туннель остаётся с ней и претендентом на основной маршрут быть не может.
  // Поэтому спорящими считаются только те, про кого не известно, что они
  // лежат; "неизвестно" остаётся в споре, иначе снимок старого агента, где
  // status не приходит вовсе, потерял бы всех претендентов разом.
  const claiming = tunnels.filter((t) => t.default_route)
  const carrying = claiming.filter((t) => tunnelLive(t) !== 'down')

  if (carrying.length === 1) {
    const t = carrying[0]
    return {
      mode: 'vpn',
      partial,
      title: `Трафик идёт через «${t.name || t.id}»`,
      detail: 'Этот туннель несёт основной маршрут.',
    }
  }
  if (carrying.length > 1) {
    // Каждый туннель может лишь ЗАЯВЛЯТЬ, что он главный. Назвать первого --
    // угадать; экран говорит "неизвестно" и перечисляет спорящих.
    return {
      mode: 'unknown',
      partial,
      title: 'Главный туннель не определён',
      detail: `Основным настроены сразу несколько: ${carrying.map((t) => t.name || t.id).join(', ')}. Пока их больше одного, кто именно несёт трафик — по снимку не видно.`,
    }
  }
  if (claiming.length > 0) {
    // Претенденты есть, но все выключены. Трафик при этом действительно идёт
    // напрямую -- и назвать причину важнее, чем повторить общий вывод: иначе
    // оператор ищет поломку маршрутизации там, где просто выключен туннель.
    const names = claiming.map((t) => t.name || t.id).join(', ')
    return {
      mode: 'direct',
      partial,
      title: 'Трафик идёт напрямую',
      detail: `Основным настроен ${claiming.length > 1 ? 'сразу несколько туннелей' : 'туннель'} «${names}», но ${claiming.length > 1 ? 'все они выключены' : 'он выключен'} — трафик уходит через провайдера.`,
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

// Куда уходит трафик, не заявленный ни одним правилом, -- вторая половина
// модели оператора: "в туннель только названное, остальное напрямую".
//
// Авторитетный ответ несёт сам снимок (default_egress, он же
// settings.download.routeTag у роутера), и он перебивает любые флаги. Флаг
// default_route описывает НАСТРОЙКУ туннеля: на живом роутере он стоит у всех
// трёх сразу, пока трафик уходит напрямую, так что экран, считающий умолчание
// по флагам, утверждает ровно обратное правде.
export function defaultDestination(snapshot) {
  const tunnels = Array.isArray(snapshot?.tunnels) ? snapshot.tunnels : []
  const egress = (snapshot?.default_egress ?? '').trim()

  if (egress === 'direct') {
    return { mode: 'direct', text: 'Всё, что не названо ниже, идёт напрямую через провайдера.' }
  }
  if (egress) {
    const named = tunnels.find((t) => t.id === egress)
    return { mode: 'vpn', text: `Всё, что не названо ниже, идёт через «${named?.name || egress}».` }
  }

  // Поля нет -- снимок агента старше этой фазы. Замолчать на нём было бы
  // хуже, чем ответить по тому, что есть: претендентом считается только
  // живой туннель, потому что выключенный основной маршрут не несёт.
  const carrying = tunnels.filter((t) => t.default_route && tunnelLive(t) !== 'down')
  if (carrying.length === 1) {
    const t = carrying[0]
    return { mode: 'vpn', text: `Всё, что не названо ниже, идёт через «${t.name || t.id}».` }
  }
  if (carrying.length > 1) {
    return { mode: 'unknown', text: 'Куда идёт всё остальное — по снимку не видно.' }
  }
  return { mode: 'direct', text: 'Всё, что не названо ниже, идёт напрямую через провайдера.' }
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
      live: tunnelLive(t),
      type: t.type ?? '',
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
    .map(([bind, items]) => ({ bind, label: bindLabel(bind), rules: items }))
}

// Заголовок группы правил. "policy:HydraRoute" -- системный ключ привязки, и
// человеку он не адресован; имя интерфейса, наоборот, уже имя и переписывать
// его нечем.
function bindLabel(bind) {
  const policy = /^policy:(.+)$/.exec(bind)
  return policy ? `Политика «${policy[1]}»` : bind
}

// Движок правила приезжает идентификатором ("hydraroute"). Показываем его тем
// же именем, что и на вкладке роутера; всё незнакомое -- эхом, а не молчанием.
export function ruleBackendLabel(backend) {
  return backend === 'hydraroute' ? HR_NEO : backend
}

// Бейдж основного маршрута. default_route -- настройка, а не факт: на живом
// роутере она стоит у всех трёх туннелей, из которых работает один. Зелёная
// пилюля на выключенном туннеле утверждала бы, что трафик идёт через него.
export function defaultRouteBadge(row) {
  if (!row?.defaultRoute) return null
  if (row.live === 'down') return { tone: 'muted', text: 'назначен основным, но выключен' }
  if (row.live === 'unknown') return { tone: 'muted', text: 'назначен основным' }
  return { tone: 'ok', text: 'основной маршрут' }
}

// Движок обхода блокировок и одна из политик роутера зовутся одинаково --
// "HydraRoute". В строке "2 правила (2 через HydraRoute)" под политикой RU
// это читается как "правила RU уходят в политику HydraRoute", то есть прямо
// наоборот. Поэтому движок здесь называется тем же именем, каким он подписан
// на вкладке самого роутера, -- "HR Neo": оператор увидит на обоих экранах
// одно слово, и с именем политики оно больше не совпадает.
const HR_NEO = 'HR Neo'

// Строка под туннелем. Правило одно: одно и то же число не повторяется.
// Было "26 правил ведут сюда · 26 из них через политику · 26 через
// HydraRoute" -- три одинаковых числа, из которых знание несёт первое.
export function tunnelRuleSummary(row) {
  const total = row?.total ?? 0
  if (total === 0) return 'правил на него нет'
  const policyRules = row.policyRules ?? 0
  const hrNeo = row.hrNeo ?? 0
  // Про движок говорим, только когда он покрывает ЧАСТЬ правил: "26 из 26"
  // не сообщает ничего сверх самого счётчика.
  const hr = hrNeo > 0 && hrNeo < total ? ` · ${hrNeo} через ${HR_NEO}` : ''
  const count = rulesCount(total)
  if (policyRules === 0) return `${count} ${total === 1 ? 'ведёт' : 'ведут'} сюда${hr}`
  if (policyRules === total) return `${count} — все через политику${hr}`
  return `${count}, из них ${policyRules} через политику${hr}`
}

// Строка под политикой -- то же правило про повтор числа и то же имя движка.
export function policyRuleSummary(row) {
  const rules = row?.rules ?? 0
  if (rules === 0) return 'правил нет'
  const hrNeo = row.hrNeo ?? 0
  const count = rulesCount(rules)
  return hrNeo > 0 && hrNeo < rules ? `${count} · ${hrNeo} через ${HR_NEO}` : count
}

// Что показывать в списке "Туннели в маршрутизации". Снимок несёт не только
// туннели: из каталога маршрутизации NDMS в него попадают WAN и системные
// интерфейсы (Wi-Fi клиент, Ethernet, серверный Wireguard) -- они нужны
// будущему переносу правил как мишени, но в списке туннелей это пять пустых
// строк подряд, и заголовок раздела на них врёт. Показываем свои туннели и
// всё, на чём реально висят правила: скрыть строку с правилами значило бы
// спрятать часть раскладки.
export function visibleTunnelRows(rows) {
  return (rows ?? []).filter((r) => r.type === 'managed' || r.total > 0)
}
