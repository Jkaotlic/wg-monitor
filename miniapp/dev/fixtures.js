// Данные для локальной отладки вёрстки. Это НЕ тесты и не часть бандла:
// плагин в vite.config.js подключает их только в режиме разработки, чтобы
// экраны можно было открыть в браузере без живого бэкенда и роутера.
//
// Формы ответов срисованы с реальных структур бэкенда:
// miniappRouterSummary/miniappIncident (miniapp_handler.go, miniapp_actions.go),
// miniappTunnel/miniappTraffic (miniapp_tunnels.go), miniappAccessResp
// (miniapp_access.go). Расходиться им нельзя -- иначе отладка врёт.

const nowISO = () => new Date().toISOString()
const agoISO = (sec) => new Date(Date.now() - sec * 1000).toISOString()

export const ROUTERS = [
  {
    id: 1,
    nickname: 'Дом',
    status: 'alert',
    last_seen_at: agoISO(12),
    last_seen_age_sec: 12,
    active_incidents: [{ check_name: 'hydraroute', fail_count: 3 }],
  },
  { id: 2, nickname: 'Дача', status: 'online', last_seen_at: agoISO(40), last_seen_age_sec: 40 },
  { id: 3, nickname: 'Офис', status: 'offline', last_seen_at: agoISO(7200), last_seen_age_sec: 7200 },
]

const TUNNELS = [
  {
    tunnel_id: 'awg12',
    name: 'Amsterdam',
    status: 'ok',
    run_state: 'running',
    enabled: true,
    handshake_age_sec: 45,
    ping_check_status: 'ok',
    ping_latency_ms: 38,
    default_route_intent: true,
    is_active_default: true,
    active_default_known: true,
  },
  {
    tunnel_id: 'awg10',
    name: 'Frankfurt',
    status: 'ok',
    run_state: 'running',
    enabled: true,
    handshake_age_sec: 120,
    ping_check_status: 'ok',
    ping_latency_ms: 51,
    default_route_intent: false,
    is_active_default: false,
    active_default_known: true,
  },
  {
    tunnel_id: 'awg7',
    name: 'Reserve',
    status: 'fail',
    run_state: 'stopped',
    enabled: false,
    ping_check_status: 'unknown',
    default_route_intent: false,
    is_active_default: false,
    active_default_known: true,
  },
]

// facts -- белый список измеримого (miniapp_check_facts.go). Формы срисованы
// с него же: без них экран диагностики показывал бы только «когда мерили».
const CHECKS = [
  { check_name: 'dns', status: 'ok', ts: agoISO(30), facts: { resolvers: 3, resolvers_failed: 1, rkn_probed: 2, rkn_suspect: 0 } },
  { check_name: 'external_reach', status: 'ok', ts: agoISO(30), facts: { targets_total: 3, targets_failed: 0 } },
  { check_name: 'hydraroute', status: 'fail', ts: agoISO(90), facts: { routes_hr_neo: 26, routes_ndms: 2, routes_static: 0, active_backend: 'hydraroute' } },
  { check_name: 'awg_manager', status: 'ok', ts: agoISO(30), facts: { version: '2.17.2', firmware: '4.3.7' } },
  { check_name: 'tunnels', status: 'ok', ts: agoISO(30) },
  { check_name: 'tunnel_awg12', status: 'ok', ts: agoISO(30) },
  { check_name: 'tunnel_awg10', status: 'ok', ts: agoISO(30) },
  { check_name: 'tunnel_awg7', status: 'fail', ts: agoISO(600) },
]

const INCIDENTS = [
  { check_name: 'hydraroute', hard_since: agoISO(1800), fail_count: 3, acked: false },
  { check_name: 'tunnel_awg7', hard_since: agoISO(5400), fail_count: 7, acked: false },
]

const HISTORY = [
  { check_name: 'hydraroute', status: 'fail', ts: agoISO(1800), details: '' },
  { check_name: 'tunnel_awg7', status: 'fail', ts: agoISO(5400), details: '' },
  { check_name: 'dns', status: 'ok', ts: agoISO(7200), details: '' },
  { check_name: 'external_reach', status: 'ok', ts: agoISO(90000), details: '' },
  { check_name: 'tunnel_awg12', status: 'ok', ts: agoISO(180000), details: '' },
]

// Настоящий снимок с домашнего роутера (awg-manager 2.17.2+r5), снят вручную
// командой route_status 2026-08-18. Это не выдумка -- смысл этой фикстуры в
// том, что на неё можно положиться при проверке цепочки политик, отметки
// "мимо VPN" и привязки правил к туннелю. Из 28 присланных правил оставлена
// показательная выборка (4 с bind=policy:HydraRoute, 2 с bind=policy:RU) --
// чтобы список на скриншоте оставался читаемым; в каждом правиле убраны
// повторы одного и того же target (сырой ответ дублирует их), значения не
// придуманы. Политики, туннели, counts, other и policy_model скопированы
// целиком. Чтобы переснять: agent route_status на живом роутере, тот же
// отбор правил.
const ROUTE_SNAPSHOT = {
  hr_neo: { installed: true, running: true },
  policy_model: true,
  counts: {},
  other: { dns: 0, hr_neo: 0, static: 0 },
  tunnels: [
    {
      id: 'awg10', name: 'awg3-main-work', iface: 'opkgtun10', type: 'managed',
      enabled: true, available: true, status: 'disabled', default_route: true,
      has_handshake: true, handshake_age_sec: 101262, ping_status: 'disabled',
      restart_method: 'control',
    },
    {
      id: 'awg11', name: 'awg3-work-via-ru1', iface: 'opkgtun11', type: 'managed',
      enabled: true, available: true, status: 'running', default_route: true,
      has_handshake: true, handshake_age_sec: 115, ping_status: 'alive', ping_fail_max: 3,
      restart_method: 'control',
    },
    {
      id: 'awg20', name: 'NetherlandsKerkradeS24', iface: 'nwg0', ndms_name: 'Wireguard0', type: 'managed',
      enabled: true, available: true, status: 'disabled', default_route: true,
      ping_status: 'disabled', restart_method: 'control',
    },
    {
      id: 'Wireguard4', name: 'AWGM WG Server', iface: 'Wireguard4', type: 'system',
      enabled: true, available: true, restart_method: 'none',
    },
    { id: 'apcli0', name: 'Wi-Fi клиент 2.4 ГГц', iface: 'apcli0', type: 'wan', enabled: false, restart_method: 'none' },
    { id: 'apclii0', name: 'Wi-Fi клиент 2.4 ГГц', iface: 'apclii0', type: 'wan', enabled: false, restart_method: 'none' },
    { id: 'cdc_br0', name: 'Huawei Mobile Broadband', iface: 'cdc_br0', type: 'wan', enabled: false, restart_method: 'none' },
    { id: 'eth3', name: 'Подключение Ethernet', iface: 'eth3', type: 'wan', enabled: true, available: true, restart_method: 'none' },
  ],
  policies: [
    {
      name: 'HydraRoute',
      dns: 26,
      hr_neo: 26,
      via_vpn: true,
      active_tunnel_id: 'awg11',
      // Цепочка -- приоритет: awg11 сейчас несёт трафик (role active), awg20
      // и awg10 -- резерв на случай, если awg11 ляжет (role unavailable).
      interfaces: [
        { bind: 'OpkgTun11', name: 'awg3-work-via-ru1', role: 'active', available: true, tunnel_id: 'awg11', via_vpn: true },
        { bind: 'Wireguard0', name: 'NetherlandsKerkradeS24', role: 'unavailable', order: 1, tunnel_id: 'awg20', via_vpn: true },
        { bind: 'OpkgTun10', name: 'awg3-main-work', role: 'unavailable', order: 2, tunnel_id: 'awg10', via_vpn: true },
      ],
    },
    {
      // У RU нет tunnel_id ни на политике, ни на её единственном звене --
      // трафик идёт через провайдера напрямую, мимо VPN, и это тоже правда.
      name: 'RU',
      dns: 2,
      hr_neo: 2,
      interfaces: [{ bind: 'GigabitEthernet1', name: 'Подключение Ethernet', role: 'active', available: true }],
    },
  ],
  rules: [
    {
      id: 'hr:AIgeo', name: 'AIgeo', kind: 'dns', backend: 'hydraroute', enabled: true, bind: 'policy:HydraRoute',
      targets: [
        { type: 'opaque', value: 'geosite:OPENAI' },
        { type: 'opaque', value: 'geosite:ANTHROPIC' },
        { type: 'opaque', value: 'geosite:CATEGORY-AI' },
        { type: 'opaque', value: 'geosite:CATEGORY-AI-CHAT' },
        { type: 'opaque', value: 'geoip:ANTHROPIC' },
        { type: 'opaque', value: 'geoip:AI' },
      ],
    },
    {
      id: 'hr:Amnezia', name: 'Amnezia', kind: 'dns', backend: 'hydraroute', enabled: true, bind: 'policy:HydraRoute',
      targets: [{ type: 'domain', value: 'amnezia.org' }],
    },
    {
      id: 'hr:ChatGPT', name: 'ChatGPT', kind: 'dns', backend: 'hydraroute', enabled: true, bind: 'policy:HydraRoute',
      targets: [
        { type: 'domain', value: 'chatgpt.com' },
        { type: 'domain', value: 'gpt3-openai.com' },
        { type: 'domain', value: 'oaistatic.com' },
        { type: 'domain', value: 'oaiusercontent.com' },
        { type: 'domain', value: 'openai.com' },
        { type: 'domain', value: 'openai.fund' },
        { type: 'domain', value: 'openai.org' },
      ],
    },
    {
      id: 'hr:GITHUB', name: 'GITHUB', kind: 'dns', backend: 'hydraroute', enabled: true, bind: 'policy:HydraRoute',
      targets: [{ type: 'opaque', value: 'geosite:GITHUB' }, { type: 'opaque', value: 'geoip:GITHUB' }],
    },
    {
      id: 'hr:geoip:ru', name: 'geoip:ru', kind: 'dns', backend: 'hydraroute', enabled: true, bind: 'policy:RU',
      targets: [{ type: 'opaque', value: 'geoip:ru' }],
    },
    {
      id: 'hr:Домены', name: 'Домены', kind: 'dns', backend: 'hydraroute', enabled: true, bind: 'policy:RU',
      targets: [{ type: 'domain', value: 'ru' }, { type: 'domain', value: 'su' }],
    },
  ],
}

const DIAG_REPORT = {
  version: '1.0',
  generatedAt: nowISO(),
  durationMs: 2559,
  system: {
    appVersion: '2.16.4',
    keeneticOS: '4.1.7',
    arch: 'aarch64',
    uptime: '5 дней 4 часа',
    totalMemoryMB: 256,
    kernelModule: { exists: true, loaded: true },
  },
  wan: { anyUp: true, interfaces: { ISP: { up: true, label: 'Провайдер' } } },
  tunnels: {
    awg12: { handshake: { status: 'ok' }, ping: { status: 'ok' }, mtu: { status: 'ok' } },
    awg10: { handshake: { status: 'ok' }, ping: { status: 'ok' }, mtu: { status: 'ok' } },
    awg7: { handshake: { status: 'fail', reason: 'нет рукопожатия 2 часа' }, ping: { status: 'skip', reason: 'туннель выключен' }, mtu: { status: 'ok' } },
  },
}

// Каталог наборов роутера (wire.RouteTemplates). На живом роутере их 87 в
// семи категориях; здесь десяток в трёх -- ровно чтобы экран показал
// группировку и разницу между набором с доменами и набором из одних
// гео-тегов, которые без HR Neo не разворачиваются.
const ROUTE_TEMPLATES = {
  templates: [
    { id: 'chatgpt', name: 'ChatGPT', category: 'ai', dns: ['chatgpt.com', 'openai.com', 'oaistatic.com'], hr_neo: ['geosite:OPENAI'] },
    { id: 'claude', name: 'Claude', category: 'ai', dns: ['claude.ai', 'anthropic.com'], hr_neo: ['geosite:ANTHROPIC'] },
    { id: 'ai-all', name: 'Весь ИИ', category: 'ai', hr_neo: ['geosite:CATEGORY-AI', 'geoip:AI'] },
    { id: 'github', name: 'GitHub', category: 'разработка', dns: ['github.com', 'githubusercontent.com'], hr_neo: ['geosite:GITHUB'] },
    { id: 'docker', name: 'Docker Hub', category: 'разработка', dns: ['docker.io', 'docker.com'] },
    { id: 'npm', name: 'npm', category: 'разработка', dns: ['npmjs.com', 'npmjs.org'] },
    { id: 'netflix', name: 'Netflix', category: 'видео', dns: ['netflix.com', 'nflxvideo.net'], hr_neo: ['geosite:NETFLIX'] },
    { id: 'youtube', name: 'YouTube', category: 'видео', dns: ['youtube.com', 'ytimg.com', 'googlevideo.com'] },
    { id: 'twitch', name: 'Twitch', category: 'видео', dns: ['twitch.tv', 'ttvnw.net'] },
    { id: 'vimeo', name: 'Vimeo', category: 'видео', dns: ['vimeo.com'] },
  ],
}

// Команды хранятся ПО СВОЕМУ ИДЕНТИФИКАТОРУ, а не одной «последней»: экран
// диагностики пускает check_direct и check_via_tunnel одновременно, и общая
// переменная отдала бы обоим ответ того, кто успел вторым, -- то есть один и
// тот же адрес выхода с обеих сторон. Ровно та ошибка, которую этот экран и
// должен ловить.
const commands = new Map()
let commandSeq = 0

export function registerCommand(command) {
  const id = `dev-${++commandSeq}`
  commands.set(id, {
    action: command && typeof command === 'object' ? command.action ?? null : command,
    args: (command && typeof command === 'object' && command.args) || {},
  })
  return id
}

function targetType(value) {
  if (value.includes(':')) return 'opaque'
  return /^[0-9.]+$/.test(value.split('/')[0]) ? 'cidr' : 'domain'
}

// План добавления (wire.RouteAddPlan). Пересечение показываем на том, что и
// на живом роутере: правило ChatGPT там уже есть, и второе такое же спорило
// бы с ним -- это единственный случай, когда план запрещает применение.
function routeAddPlan(args) {
  const tpl = ROUTE_TEMPLATES.templates.find((t) => t.id === args.template_id)
  const fromTemplate = [...(tpl?.dns ?? []), ...(args.use_hr_neo ? tpl?.hr_neo ?? [] : [])]
  const targets = (args.targets ?? []).length ? args.targets : fromTemplate
  const name = args.name || tpl?.name || 'Новое правило'
  const blocked = targets.some((v) => v === 'openai.com' || v === 'chatgpt.com')
  return {
    request: { kind: args.kind ?? 'dns', name, tunnel_id: args.tunnel_id, targets, use_hr_neo: Boolean(args.use_hr_neo) },
    route: {
      id: `new:${args.kind ?? 'dns'}:${name}`,
      name,
      kind: args.kind ?? 'dns',
      enabled: true,
      backend: args.use_hr_neo ? 'hydraroute' : 'ndms',
      bind: 'opkgtun11',
      targets: targets.map((value) => ({ type: targetType(value), value })),
    },
    overlaps: blocked
      ? [{
          severity: 'block',
          reason: 'уже есть правило «ChatGPT» на openai.com',
          existing: { id: 'hr:ChatGPT', name: 'ChatGPT', kind: 'dns', backend: 'hydraroute', enabled: true, bind: 'policy:HydraRoute' },
          target: { type: 'domain', value: 'openai.com' },
        }]
      : [],
    can_apply: !blocked,
    hash: 'devplanhash',
  }
}

function commandResult(id) {
  const { action: lastAction, args: lastArgs } = commands.get(id) ?? { action: null, args: {} }
  if (lastAction === 'version_audit') {
    return {
      id,
      status: 'ok',
      duration_ms: 1400,
      output: JSON.stringify({
        awgmgr_version: '2.17.2',
        awgmgr_running: true,
        hrneo_installed: true,
        hrneo_running: true,
        hrneo_version: '2.4.0',
        firmware_current: '4.3.7',
        firmware_avail: '4.3.8',
      }),
    }
  }
  if (lastAction === 'router_doctor') {
    return {
      id,
      status: 'ok',
      duration_ms: 2600,
      output: [
        '🩺 Проверка роутера',
        '✅ awg-manager API — 2.17.2, backend awg',
        '✅ туннели — 2 из 3 подняты',
        '⚠️ ping-check — выключен у awg10',
        '✅ wg-monitor agent — процесс жив',
        '✅ awg-manager daemon — процесс жив',
        '⚠️ маршрут по умолчанию — заявлен у трёх туннелей',
      ].join('\n'),
    }
  }
  if (lastAction === 'hrneo_doctor') {
    return {
      id,
      status: 'ok',
      duration_ms: 1900,
      output: ['🩺 HR Neo', '✅ служба — работает, 2.4.0', '✅ правила — 26 активны', '✅ dnsmasq — перезапущен 2 ч назад'].join('\n'),
    }
  }
  if (lastAction === 'tunnel_enable' || lastAction === 'tunnel_disable') {
    return {
      id,
      status: 'ok',
      duration_ms: 1200,
      output: `interface Wireguard0 -> ${lastAction === 'tunnel_enable' ? 'up' : 'down'}`,
    }
  }
  if (lastAction === 'pingcheck_now') {
    return { id, status: 'ok', duration_ms: 800, output: 'pingcheck-now triggered' }
  }
  if (lastAction === 'pingcheck_toggle') {
    return { id, status: 'ok', duration_ms: 700, output: `pingcheck ${lastArgs.enable ? 'enabled' : 'disabled'} for ${lastArgs.tunnel_id}` }
  }
  if (lastAction === 'route_templates') {
    return { id, status: 'ok', duration_ms: 310, output: JSON.stringify(ROUTE_TEMPLATES) }
  }
  if (lastAction === 'route_add_plan') {
    return { id, status: 'ok', duration_ms: 420, output: JSON.stringify(routeAddPlan(lastArgs)) }
  }
  if (lastAction === 'route_add') {
    const plan = routeAddPlan(lastArgs)
    return {
      id,
      status: 'ok',
      duration_ms: 780,
      output: JSON.stringify({ action: 'add', kind: plan.route.kind, route_id: 'hr:new', route_name: plan.route.name }),
    }
  }
  if (lastAction === 'route_rebind') {
    return {
      id,
      status: 'ok',
      duration_ms: 2100,
      output: JSON.stringify({
        src_tunnel_id: lastArgs.src_tunnel_id ?? '',
        dst_tunnel_id: lastArgs.dst_tunnel_id ?? '',
        dns: { ok: 26, failed: 0 },
        static: { ok: 0, failed: 0 },
        hr_neo: { ok: 26, failed: 0 },
      }),
    }
  }
  if (lastAction === 'route_policy_promote') {
    // Ответ агента -- свежая политика: повышенное звено стало первым, но
    // трафик несёт по-прежнему живое (awg11), потому что awg10 выключен.
    return {
      id,
      status: 'ok',
      duration_ms: 1600,
      output: JSON.stringify({
        name: lastArgs.policy_name ?? 'HydraRoute',
        dns: 26,
        hr_neo: 26,
        via_vpn: true,
        active_tunnel_id: 'awg11',
        interfaces: [
          { bind: 'OpkgTun10', name: 'awg3-main-work', role: 'unavailable', tunnel_id: 'awg10', via_vpn: true },
          { bind: 'OpkgTun11', name: 'awg3-work-via-ru1', role: 'active', available: true, tunnel_id: 'awg11', via_vpn: true },
        ],
      }),
    }
  }
  if (lastAction === 'route_delete_plan') {
    return {
      id,
      status: 'ok',
      duration_ms: 140,
      output: JSON.stringify({
        route: {
          id: 'hr:ChatGPT',
          name: 'ChatGPT',
          kind: 'dns',
          targets: [
            { type: 'domain', value: 'chatgpt.com' },
            { type: 'domain', value: 'openai.com' },
            { type: 'domain', value: 'oaistatic.com' },
          ],
        },
        warnings: [{ severity: 'warn', reason: 'последнее правило, ведущее в awg3-work-via-ru1' }],
        can_apply: true,
        hash: 'devhash',
      }),
    }
  }
  if (lastAction === 'route_delete') {
    return { id, status: 'ok', duration_ms: 260, output: 'правило удалено' }
  }
  if (lastAction === 'route_status') {
    return { id, status: 'ok', duration_ms: 120, output: JSON.stringify(ROUTE_SNAPSHOT) }
  }
  if (lastAction === 'diag_now') {
    return { id, status: 'ok', duration_ms: 2559, output: JSON.stringify(DIAG_REPORT) }
  }
  if (lastAction === 'check_direct') {
    return { id, status: 'ok', duration_ms: 900, output: '🇷🇺 Напрямую (через системный маршрут):\nExit IP: 203.0.113.7\n\n✅ ya.ru' }
  }
  if (lastAction === 'check_via_tunnel') {
    return { id, status: 'ok', duration_ms: 1100, output: '🌍 Через туннель (awg12):\nExit IP: 203.0.113.19\n\n✅ google.com' }
  }
  return { id, status: 'ok', duration_ms: 300, output: 'готово' }
}

export function respond(method, path) {
  if (method === 'POST' && path === '/v1/miniapp/session') {
    return { ok: true, telegram_user_id: 42, is_admin: true }
  }
  if (path === '/v1/miniapp/routers') return { routers: ROUTERS }

  // Идентификатор команды теперь не только из букв (dev-3), и хвост пути
  // обязан его пропускать -- иначе опрос результата уходит в никуда.
  const m = path.match(/^\/v1\/miniapp\/routers\/(\d+)(\/[a-z0-9/_-]*)?/i)
  if (!m) return null
  const id = Number(m[1])
  const rest = m[2] ?? ''
  const router = ROUTERS.find((r) => r.id === id) ?? ROUTERS[0]

  if (rest === '' || rest === '/') return { router, incidents: id === 1 ? INCIDENTS : [] }
  if (rest === '/events') {
    return {
      checks: CHECKS,
      tunnels: TUNNELS,
      traffic: {
        mode: 'vpn',
        egress_tunnel_id: 'awg12',
        egress_tunnel_name: 'Amsterdam',
        contested_default: false,
      },
    }
  }
  if (rest === '/timeline') return { events: HISTORY, days: 7, truncated: false }
  // Пороги живут в backend.yaml; на домашнем бэкенде это 120/180/3600.
  if (rest === '/settings') {
    return {
      silence_after_sec: 120,
      alert_after_fails: 3,
      recovery_after_oks: 2,
      agent_version: 'v0.16.0',
    }
  }
  if (rest.startsWith('/commands/')) return commandResult(rest.slice('/commands/'.length))
  if (rest === '/access') {
    return {
      owner: { telegram_user_id: 42 },
      operators: [{ telegram_user_id: 777, granted_by: 42, granted_at: nowISO() }],
    }
  }
  return null
}
