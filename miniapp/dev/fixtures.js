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

const CHECKS = [
  { check_name: 'dns', status: 'ok', ts: agoISO(30) },
  { check_name: 'external_reach', status: 'ok', ts: agoISO(30) },
  { check_name: 'hydraroute', status: 'fail', ts: agoISO(90) },
  { check_name: 'awg_manager', status: 'ok', ts: agoISO(30) },
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

const ROUTE_SNAPSHOT = {
  hr_neo: { enabled: true },
  tunnels: [
    { id: 'awg12', name: 'Amsterdam', iface: 'OpkgTun11', enabled: true, default_route: true },
    { id: 'awg10', name: 'Frankfurt', iface: 'OpkgTun10', enabled: true },
    { id: 'awg7', name: 'Reserve', iface: 'Wireguard1', enabled: false },
  ],
  counts: {
    awg12: { dns: 32, static: 2, hr_neo: 32 },
    awg10: { dns: 0, static: 0, hr_neo: 0 },
    awg7: { dns: 3, static: 0, hr_neo: 0 },
  },
  policies: [
    {
      name: 'HydraRoute',
      dns: 32,
      hr_neo: 32,
      interfaces: [
        { bind: 'OpkgTun11', name: 'Amsterdam', role: 'active', available: true },
        { bind: 'OpkgTun10', name: 'Frankfurt', role: 'fallback', available: true },
        { bind: 'Wireguard1', name: 'Reserve', role: 'unavailable' },
      ],
    },
    { name: 'RU', dns: 3, hr_neo: 0, interfaces: [{ bind: 'GigabitEthernet1', name: 'Провайдер', role: 'active' }] },
  ],
  rules: [
    { id: '1', name: 'youtube', kind: 'dns', backend: 'hydraroute', enabled: true, bind: 'OpkgTun11', targets: [{ type: 'domain', value: 'youtube.com' }, { type: 'domain', value: 'ytimg.com' }] },
    { id: '2', name: 'instagram', kind: 'dns', backend: 'hydraroute', enabled: true, bind: 'OpkgTun11', targets: [{ type: 'domain', value: 'instagram.com' }] },
    { id: '3', name: 'банки', kind: 'static', enabled: true, bind: 'GigabitEthernet1', targets: [{ type: 'cidr', value: '185.71.76.0/22' }] },
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

// Последняя запрошенная команда: dev-сервер отвечает на опрос результата тем,
// что соответствует действию, а не одной заглушкой на все случаи.
let lastAction = null

export function setLastAction(action) {
  lastAction = action
}

function commandResult() {
  if (lastAction === 'route_status') {
    return { id: 'dev', status: 'ok', duration_ms: 120, output: JSON.stringify(ROUTE_SNAPSHOT) }
  }
  if (lastAction === 'diag_now') {
    return { id: 'dev', status: 'ok', duration_ms: 2559, output: JSON.stringify(DIAG_REPORT) }
  }
  if (lastAction === 'check_direct') {
    return { id: 'dev', status: 'ok', duration_ms: 900, output: '🇷🇺 Напрямую (через системный маршрут):\nExit IP: 203.0.113.7\n\n✅ ya.ru' }
  }
  if (lastAction === 'check_via_tunnel') {
    return { id: 'dev', status: 'ok', duration_ms: 1100, output: '🌍 Через туннель (awg12):\nExit IP: 203.0.113.19\n\n✅ google.com' }
  }
  return { id: 'dev', status: 'ok', duration_ms: 300, output: 'готово' }
}

export function respond(method, path) {
  if (method === 'POST' && path === '/v1/miniapp/session') {
    return { ok: true, telegram_user_id: 42, is_admin: true }
  }
  if (path === '/v1/miniapp/routers') return { routers: ROUTERS }

  const m = path.match(/^\/v1\/miniapp\/routers\/(\d+)(\/[a-z/]*)?/)
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
  if (rest === '/commands') return { cmd_id: 'dev' }
  if (rest.startsWith('/commands/')) return commandResult()
  if (rest === '/access') {
    return {
      owner: { telegram_user_id: 42 },
      operators: [{ telegram_user_id: 777, granted_by: 42, granted_at: nowISO() }],
    }
  }
  return null
}
