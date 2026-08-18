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
  if (rest === '/history') return { events: HISTORY, days: 7, truncated: false }
  if (rest === '/access') {
    return {
      owner: { telegram_user_id: 42 },
      operators: [{ telegram_user_id: 777, granted_by: 42, granted_at: nowISO() }],
    }
  }
  return null
}
